package main

// Claiming a todo over MCP, against a real store.
//
// The store's own guard is proved in internal/store/claimtodo_test.go. What is
// worth asserting HERE is that the guard is reachable from the door the fleet
// actually knocks on: every agent claims through MCP, so a CAS that only POST
// /api/todo/{id}/assignee could ask for was a CAS on the door nobody uses, and
// the contested claims went on being answered "yes" to both callers. Seven
// collisions in one night came through this surface.
//
// So these race the tools rather than the store: by name, out of allTools, which
// is the same lookup the server does. A guard wired into a function no tool calls
// looks exactly like a working one from the source.
//
// They need a database, so they sit out a plain `go test ./...` and run under
// ./run-tests.sh, the same way the chat tools' live tests do.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// claimCall runs a tool by name and hands its error BACK rather than failing the
// test with it. The racers below run in goroutines, where t.Fatal stops one
// goroutine and reports nothing, so every failure here has to travel home as a
// value.
func claimCall(
	ctx context.Context, db *store.DB, p *store.Principal, name string, args any,
) (map[string]any, error) {
	tl, ok := toolByName(name)
	if !ok {
		return nil, errors.New(name + " is not in allTools, so no agent can call it")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := tl.call(ctx, &mcpServer{db: db, node: "test-node"}, p, raw)
	if err != nil {
		return nil, err
	}
	answer, _ := out.(map[string]any)
	return answer, nil
}

// claimRow raises one unowned todo through mem_write - the door the fleet raises
// them through - and hands back its id.
func claimRow(t *testing.T, ctx context.Context, db *store.DB, p *store.Principal, room, title string) string {
	t.Helper()

	out, err := claimCall(ctx, db, p, "mem_write", map[string]any{
		"title": title, "body": "raised by the claim test", "kind": "todo",
		"scope": "project", "room": room,
	})
	if err != nil {
		t.Fatalf("the test could not raise the todo it races for: %v", err)
	}
	art, ok := out["item"].(*store.Artifact)
	if !ok {
		t.Fatalf("mem_write handed back %T, which carries no id", out["item"])
	}
	return art.ID
}

// claimArgs is what one racer sends, per tool. The two doors spell the todo's id
// differently and mean the same thing by expect, and both are raced because both
// are how an agent takes a row: todo_assign is the verb, and mem_write {id,
// assignee} is what an agent updating a row it is picking up actually sends.
func claimArgs(tool, id, name string) map[string]any {
	if tool == "todo_assign" {
		return map[string]any{"todo": id, "assignee": name, "expect": ""}
	}
	return map[string]any{"id": id, "assignee": name, "expect": ""}
}

// THE ONE THAT MATTERS, on the surface the collisions came through: six agents
// claim one unowned row at the same moment, through MCP. Exactly one may come
// away holding it, and the other five must be told who did.
func TestTwoAgentsClaimingOneTodoOverMCPAndOnlyOneWins(t *testing.T) {
	for _, tool := range []string{"todo_assign", "mem_write"} {
		t.Run(tool, func(t *testing.T) {
			ctx, db := chatStore(t)
			project, room := declaredRoom(t, ctx, db, "mcpclaim")
			raiser := newSeat(t, ctx, db, project, "raiser")
			id := claimRow(t, ctx, db, raiser.p, room, "port build-sut.sh")

			const racers = 6
			seats := make([]seat, racers)
			for i := range seats {
				seats[i] = newSeat(t, ctx, db, project, "racer")
			}

			var (
				wg    sync.WaitGroup
				mu    sync.Mutex
				won   []string
				lost  []error
				start = make(chan struct{})
			)
			for _, s := range seats {
				wg.Add(1)
				go func(s seat) {
					defer wg.Done()
					<-start
					// Everybody read the row as unowned, which is what
					// expect:"" says, and everybody is wrong but one.
					_, err := claimCall(ctx, db, s.p, tool, claimArgs(tool, id, s.handle))
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						won = append(won, s.handle)
					} else {
						lost = append(lost, err)
					}
				}(s)
			}
			close(start)
			wg.Wait()

			if len(won) != 1 {
				t.Fatalf("%d of %d claimers won through %s, exactly one may: %v",
					len(won), racers, tool, won)
			}
			art, err := db.GetArtifact(ctx, id)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got := store.AssigneeOf(art); got != won[0] {
				t.Fatalf("the row is carried by %q and the winner was %q", got, won[0])
			}
			// A refusal that does not name the winner is one the loser can only
			// retry against. Naming them is what makes it actionable.
			for _, err := range lost {
				var held store.ErrHeldBy
				if !errors.As(err, &held) {
					t.Fatalf("a loser got %v, want ErrHeldBy naming the winner", err)
				}
				if held.Holder != won[0] {
					t.Errorf("a loser was told %q holds it, the holder is %q", held.Holder, won[0])
				}
			}
		})
	}
}

// A claim against a row that has moved is refused over MCP too, with nothing
// racing: the caller read "nobody", somebody took it, and acting on the stale
// reading is exactly what this prevents. And an honest takeover still lands,
// because taking over from a named holder is legal - it just has to be
// deliberate.
func TestAStaleClaimOverMCPIsRefusedAndAnHonestTakeoverIsNot(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "mcpclaim")
	first := newSeat(t, ctx, db, project, "first")
	second := newSeat(t, ctx, db, project, "second")
	id := claimRow(t, ctx, db, first.p, room, "repair the layer")

	if _, err := claimCall(ctx, db, first.p, "todo_assign", claimArgs("todo_assign", id, first.handle)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := claimCall(ctx, db, second.p, "todo_assign", claimArgs("todo_assign", id, second.handle))
	var held store.ErrHeldBy
	if !errors.As(err, &held) {
		t.Fatalf("a stale claim was answered %v, want ErrHeldBy", err)
	}
	if held.Holder != first.handle {
		t.Errorf("the refusal names %q as the holder, want %s", held.Holder, first.handle)
	}
	if _, err := claimCall(ctx, db, second.p, "todo_assign", map[string]any{
		"todo": id, "assignee": second.handle, "expect": first.handle,
	}); err != nil {
		t.Fatalf("an honest takeover over MCP was refused: %v", err)
	}
}

// A HANDOVER IS NOT A RACE, and an absent argument keeps today's behaviour. Both
// tools without expect stay last-write-wins, so the operator handing work out and
// an agent picking up an abandoned row are untouched by any of this - which is
// the whole reason the guard is an argument rather than the default.
func TestMCPAssignmentWithoutExpectIsStillLastWriteWins(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "mcpclaim")
	op := newSeat(t, ctx, db, project, "operator")
	id := claimRow(t, ctx, db, op.p, room, "hand this around")

	for _, hand := range []struct {
		tool string
		args map[string]any
	}{
		{"todo_assign", map[string]any{"todo": id, "assignee": "a"}},
		{"mem_write", map[string]any{"id": id, "assignee": "b"}},
		{"todo_assign", map[string]any{"todo": id, "assignee": "c"}},
	} {
		if _, err := claimCall(ctx, db, op.p, hand.tool, hand.args); err != nil {
			t.Fatalf("%s handing the row on: %v", hand.tool, err)
		}
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := store.AssigneeOf(art); got != "c" {
		t.Errorf("after three handovers the row reads %q, want c", got)
	}
}

// A claim decides ONE thing, and mem_write refuses to make it half of an edit.
// The general write rebuilds the whole fields blob from a read taken before any
// guard could run, so a claim bolted onto a title change would be a guard whose
// answer the next statement overwrites. Refused, and nothing of it made - not the
// claim, and not the title either.
func TestAnMCPClaimCarryingAnEditIsRefusedWhole(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "mcpclaim")
	me := newSeat(t, ctx, db, project, "author")
	id := claimRow(t, ctx, db, me.p, room, "the title as raised")

	_, err := claimCall(ctx, db, me.p, "mem_write", map[string]any{
		"id": id, "assignee": me.handle, "expect": "", "title": "rewritten on the way past",
	})
	if err == nil {
		t.Fatal("a claim that also rewrote the title was allowed")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("the refusal is %q and never says which field it is about", err)
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if art.Title != "the title as raised" {
		t.Errorf("the refused write still renamed the row to %q", art.Title)
	}
	if got := store.AssigneeOf(art); got != "" {
		t.Errorf("the refused write still handed the row to %q", got)
	}
}

// expect without an assignee says what the caller read and not what it wants,
// which is half a claim. Refused, rather than read as a handover to nobody -
// putting work down is assignee:"" and it is a different sentence.
func TestAnMCPClaimThatNamesNobodyIsRefused(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "mcpclaim")
	me := newSeat(t, ctx, db, project, "author")
	id := claimRow(t, ctx, db, me.p, room, "keep this")

	if _, err := claimCall(ctx, db, me.p, "mem_write", map[string]any{
		"id": id, "expect": "",
	}); err == nil {
		t.Fatal("a claim that never said who was taking the row was allowed")
	}
}
