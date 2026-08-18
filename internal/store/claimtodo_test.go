package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// todoRow writes one unowned todo and hands back its id.
func todoRow(t *testing.T, ctx context.Context, db *DB, project, title string) string {
	t.Helper()

	art := &Artifact{
		Type:       MemoryType,
		Kind:       "todo",
		Project:    &project,
		OwnerUser:  "01USER-OPERATOR",
		Title:      title,
		Visibility: VisibilityShared,
	}
	if err := db.WriteMemory(ctx, art); err != nil {
		t.Fatalf("write todo: %v", err)
	}
	return art.ID
}

// THE ONE THAT MATTERS, and the one the old door failed five times in a night:
// two agents claim the same unowned row at the same moment. Exactly one may come
// away holding it, and the other must be told who did.
func TestTwoAgentsClaimingOneTodoAndOnlyOneWins(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "port build-sut.sh")

	const racers = 6
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   []string
		lost  []error
		start = make(chan struct{})
	)
	for i := 0; i < racers; i++ {
		name := "agent-" + string(rune('a'+i))
		p := &Principal{UserID: "01USER-" + name, AgentID: "01AGENT-" + name, Project: project}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Everybody read the row as unowned, which is what expect:"" says.
			_, _, err := db.ClaimTodo(ctx, p, id, name, "")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won = append(won, name)
			} else {
				lost = append(lost, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(won) != 1 {
		t.Fatalf("%d of %d claimers won, exactly one may: %v", len(won), racers, won)
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := AssigneeOf(art); got != won[0] {
		t.Fatalf("the row is carried by %q and the winner was %q", got, won[0])
	}
	for _, err := range lost {
		var held ErrHeldBy
		if !errors.As(err, &held) {
			t.Fatalf("a loser got %v, want ErrHeldBy naming the winner", err)
		}
		if held.Holder != won[0] {
			t.Errorf("a loser was told %q holds it, the holder is %q", held.Holder, won[0])
		}
	}
}

// A claim against a row that has moved is refused even when nothing is racing:
// the caller read "nobody", somebody took it, and acting on the stale reading is
// exactly what this prevents.
func TestAClaimAgainstAStaleReadingIsRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "repair the layer")
	first := &Principal{UserID: "01USER-FIRST", AgentID: "01AGENT-FIRST", Project: project}
	second := &Principal{UserID: "01USER-SECOND", AgentID: "01AGENT-SECOND", Project: project}

	if _, _, err := db.ClaimTodo(ctx, first, id, "first", ""); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, _, err := db.ClaimTodo(ctx, second, id, "second", "")
	var held ErrHeldBy
	if !errors.As(err, &held) {
		t.Fatalf("a stale claim was answered %v, want ErrHeldBy", err)
	}
	if held.Holder != "first" {
		t.Errorf("refusal names %q as the holder, want first", held.Holder)
	}
	// And a claim that states the truth still wins - taking over from a named
	// holder is legal, it just has to be deliberate.
	if _, _, err := db.ClaimTodo(ctx, second, id, "second", "first"); err != nil {
		t.Fatalf("an honest takeover was refused: %v", err)
	}
}

// This was "plain assignment is still last-write-wins", and the contract it
// encoded is the one the guard replaces: a write with no expect moved a held
// row, which is how a claim written through the unguarded door landed over a
// guarded one, twice in one morning. The deliberate handover still exists - it
// names the holder it takes from - and what the plain door does now is refuse.
func TestPlainAssignmentRefusesAHeldRowAndNamesTheHolder(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "hand this around")
	p := &Principal{UserID: "01USER-OP", AgentID: "01AGENT-OP", Project: project}

	// An unheld row takes any write: the first assignment is not a takeover.
	if _, _, err := db.AssignTodo(ctx, p, id, "a", nil); err != nil {
		t.Fatalf("assign to a: %v", err)
	}
	_, _, err := db.AssignTodo(ctx, p, id, "b", nil)
	if err == nil {
		t.Fatal("an unguarded write moved a held row")
	}
	msg := err.Error()
	for _, want := range []string{"carried by a", "expect:a"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not say %q", msg, want)
		}
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := AssigneeOf(art); got != "a" {
		t.Errorf("after the refused write the row reads %q, want a", got)
	}
	// The handover, done as a handover: naming who it takes from.
	if _, _, err := db.ClaimTodo(ctx, p, id, "b", "a"); err != nil {
		t.Fatalf("handover with expect: %v", err)
	}
	if art, err = db.GetArtifact(ctx, id); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := AssigneeOf(art); got != "b" {
		t.Errorf("after the handover the row reads %q, want b", got)
	}
	// And on down the line: the deliberate path is one field longer, always.
	if _, _, err := db.ClaimTodo(ctx, p, id, "c", "b"); err != nil {
		t.Fatalf("handover with expect: %v", err)
	}
}

// The holder moves their own row: releasing it and handing it on are both
// theirs, because holding it is what earned that. What nobody else may do is
// move it without saying whose it was.
func TestTheHolderMovesTheirOwnRowUnchallenged(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "keep this moving")
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO users (id, handle) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		"01USER-HOLD", "holder-seat"); err != nil {
		t.Fatalf("seat the holder: %v", err)
	}
	holder := &Principal{UserID: "01USER-HOLD", Project: project}
	other := &Principal{UserID: "01USER-OTH", Project: project}

	if _, _, err := db.AssignTodo(ctx, holder, id, "holder-seat", nil); err != nil {
		t.Fatalf("the holder takes the unheld row: %v", err)
	}
	// Releasing it to nobody, as the holder, and the row really is empty.
	if _, _, err := db.AssignTodo(ctx, holder, id, "", nil); err != nil {
		t.Fatalf("the holder releases their row: %v", err)
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := AssigneeOf(art); got != "" {
		t.Fatalf("after the release the row reads %q, want nobody", got)
	}
	// Taking it again, then handing it on deliberately, still as the holder.
	if _, _, err := db.AssignTodo(ctx, holder, id, "holder-seat", nil); err != nil {
		t.Fatalf("the holder retakes the now-unheld row: %v", err)
	}
	if _, _, err := db.AssignTodo(ctx, holder, id, "someone-else", nil); err != nil {
		t.Fatalf("the holder hands their row on: %v", err)
	}
	// And the next move by anyone else is refused until they name the holder.
	if _, _, err := db.AssignTodo(ctx, other, id, "other-seat", nil); err == nil {
		t.Fatal("a third party moved a held row without naming its holder")
	}
}

// Restating your own claim is not losing to yourself, and writes no second
// entry: an agent re-reading its queue after a restart must not be refused.
func TestRestatingYourOwnClaimIsNotARace(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "keep this")
	me := &Principal{UserID: "01USER-ME", AgentID: "01AGENT-ME", Project: project}

	if _, _, err := db.ClaimTodo(ctx, me, id, "me", ""); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, entry, err := db.ClaimTodo(ctx, me, id, "me", "")
	if err != nil {
		t.Fatalf("restating my own claim was refused: %v", err)
	}
	if entry != nil {
		t.Error("restating my own claim wrote a second entry")
	}
}
