package store

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The flows this door has to satisfy, named before it was built:
//   1. something with no token asks, and a row appears
//   2. the row grants nothing
//   3. the same handle twice is refused, naming what holds it
//   4. a request with no reason is refused - the operator needs something to decide on
//   5. a handle that cannot address a seat is refused

func TestAskingCreatesARowThatGrantsNothing(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	handle := "grok-" + ulid.NewString()

	art, entry, err := db.RequestJoin(ctx, JoinRequest{
		Handle: handle, Kind: "opencode", Project: project,
		Reason: "run builds under a different provider so the fleet is not one model",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if art.Kind != JoinKind {
		t.Fatalf("a join request is kind %q: got %q", JoinKind, art.Kind)
	}
	if JoinStateOf(art) != "pending" {
		t.Fatalf("a fresh request is pending, not %q - it grants nothing until a person acts", JoinStateOf(art))
	}
	if JoinHandleOf(art) != handle {
		t.Fatalf("the handle asked for must survive: %q", JoinHandleOf(art))
	}
	if art.Status != TodoStatus {
		t.Fatalf("it lands in the queue as work waiting on somebody: got %q", art.Status)
	}
	// No actor. Nothing has an identity here yet, which is the point of the
	// door - who the asker turns out to be is decided by the approval.
	if entry.Actor != "" {
		t.Fatalf("a request from nobody has no actor: got %q", entry.Actor)
	}
}

// The refusal names what holds the handle, because "taken" alone sends the
// asker round the loop with a new name they did not need.
func TestASecondRequestForOneHandleIsRefusedAndSaysWhy(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	req := JoinRequest{Handle: "twice-" + ulid.NewString(), Kind: "agent", Project: project, Reason: "first"}

	if _, _, err := db.RequestJoin(ctx, req); err != nil {
		t.Fatalf("first request: %v", err)
	}
	req.Reason = "second"
	_, _, err := db.RequestJoin(ctx, req)
	var taken *ErrHandleTaken
	if !errors.As(err, &taken) {
		t.Fatalf("a repeat must be refused: got %v", err)
	}
	if taken.By == "" {
		t.Fatal("the refusal says what holds the handle")
	}
}

// A request with no reason gives the operator nothing to decide on, and the
// decision is the entire point of the row.
func TestARequestWithNoReasonIsRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	if _, _, err := db.RequestJoin(ctx, JoinRequest{Handle: "silent-" + ulid.NewString(), Project: project}); err == nil {
		t.Fatal("a join request has to say what it is for")
	}
}

func TestAHandleThatCannotAddressASeatIsRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	for _, bad := range []string{"", "two words", "a/b"} {
		if _, _, err := db.RequestJoin(ctx, JoinRequest{Handle: bad, Project: project, Reason: "x"}); err == nil {
			t.Fatalf("handle %q should be refused - it cannot address a seat", bad)
		}
	}
}

// The project is where the request lands, so the operator of that project sees
// it. A request nobody can see is a request nobody can grant.
func TestARequestWithNoProjectIsRefused(t *testing.T) {
	ctx, db := open(t)
	if _, _, err := db.RequestJoin(ctx, JoinRequest{Handle: "homeless-" + ulid.NewString(), Reason: "x"}); err == nil {
		t.Fatal("a join request names the project it wants into")
	}
}

// Flows 2 and 3, the half that grants.

func TestApprovingMintsTheSeatAndClosesTheRequest(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	art, _, err := db.RequestJoin(ctx, JoinRequest{
		Handle: "grok-" + ulid.NewString(), Kind: "opencode", Project: project, Reason: "another provider",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	op := &Principal{UserID: "u-op", Project: project, Operator: true}

	got, minted, err := db.ApproveJoin(ctx, op, art.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if minted.Token == "" {
		t.Fatal("approval mints - the token is the whole point")
	}
	if JoinStateOf(got) != "approved" || got.Status != DoneStatus {
		t.Fatalf("an approved request is closed: state %q status %q", JoinStateOf(got), got.Status)
	}
	// The token must not be on the row. A credential written into an artifact
	// is a credential in every replica of it.
	blob, _ := ArtifactFields(got)
	for k, v := range blob {
		if s, ok := v.(string); ok && s == minted.Token {
			t.Fatalf("the token was written to the row under %q", k)
		}
	}
}

// Approving twice would mint a second seat for one request and hand out a
// second token - the shape a replay wants.
func TestApprovingTwiceIsRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	art, _, err := db.RequestJoin(ctx, JoinRequest{Handle: "once-" + ulid.NewString(), Project: project, Reason: "x"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	op := &Principal{UserID: "u-op", Project: project, Operator: true}
	if _, _, err := db.ApproveJoin(ctx, op, art.ID); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, _, err := db.ApproveJoin(ctx, op, art.ID); err == nil {
		t.Fatal("a request can only be granted once")
	}
}

// A refusal without a reason leaves the asker to guess whether to try again,
// which is the same as saying nothing.
func TestRefusingSaysWhyOrIsItselfRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	art, _, err := db.RequestJoin(ctx, JoinRequest{Handle: "nope-" + ulid.NewString(), Project: project, Reason: "x"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	op := &Principal{UserID: "u-op", Project: project, Operator: true}

	if _, err := db.RefuseJoin(ctx, op, art.ID, "   "); err == nil {
		t.Fatal("a refusal has to say why")
	}
	got, err := db.RefuseJoin(ctx, op, art.ID, "we do not need a second builder yet")
	if err != nil {
		t.Fatalf("refuse: %v", err)
	}
	if JoinStateOf(got) != "refused" || got.Status != DoneStatus {
		t.Fatalf("a refused request is closed and says so: %q %q", JoinStateOf(got), got.Status)
	}
}

// A refused handle is free again - the refusal closed the row, so asking is not
// blocked forever by a no.
func TestARefusedHandleCanBeAskedForAgain(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "join")
	handle := "retry-" + ulid.NewString()
	art, _, err := db.RequestJoin(ctx, JoinRequest{Handle: handle, Project: project, Reason: "first try"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	op := &Principal{UserID: "u-op", Project: project, Operator: true}
	if _, err := db.RefuseJoin(ctx, op, art.ID, "not yet"); err != nil {
		t.Fatalf("refuse: %v", err)
	}
	if _, _, err := db.RequestJoin(ctx, JoinRequest{Handle: handle, Project: project, Reason: "asking again"}); err != nil {
		t.Fatalf("a refused handle should be free to ask for again: %v", err)
	}
}
