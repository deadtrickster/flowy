package store

import (
	"context"
	"errors"
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

// A HANDOVER IS NOT A RACE. AssignTodo keeps its last-write-wins behaviour, so
// the operator handing work out and an agent picking up an abandoned row are
// untouched by any of this.
func TestPlainAssignmentIsStillLastWriteWins(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "claimtodo")
	id := todoRow(t, ctx, db, project, "hand this around")
	p := &Principal{UserID: "01USER-OP", AgentID: "01AGENT-OP", Project: project}

	for _, name := range []string{"a", "b", "c"} {
		if _, _, err := db.AssignTodo(ctx, p, id, name, nil); err != nil {
			t.Fatalf("assign to %s: %v", name, err)
		}
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := AssigneeOf(art); got != "c" {
		t.Errorf("after three handovers the row reads %q, want c", got)
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
