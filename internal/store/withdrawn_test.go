package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// artifactIn writes one artifact owned by p, at the visibility asked for, in the
// project p carries. It goes in through UpsertArtifact rather than through a
// verb because what these tests are about is what happens to an ordinary row
// when it is withdrawn, not how it got written.
func artifactIn(t *testing.T, ctx context.Context, db *DB, p *Principal, visibility, title string) *Artifact {
	t.Helper()

	fields, err := json.Marshal(map[string]any{RoomField: "general"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	project := p.Project
	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "note", Project: &project,
		OwnerUser: p.UserID, Title: title, Body: "the body nobody gets back",
		Visibility: visibility, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return art
}

// A withdrawal has to name somebody. "This is gone" is the answer a delete
// already gave, and the whole reason a tombstone is kept instead of a delete is
// that "somebody took this back at 23:14" is a different fact - which it only is
// if the row is carrying who and when.
//
// The seat is the one that acted, not the person behind it: an agent withdrawing
// its user's row is what a reader needs to see, because that is who to go and
// ask. It is voteActor's rule and it is the same rule a vote and a work item use.
func TestAWithdrawalNamesTheSeatThatTookTheRowBackAndWhen(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "withdraw")

	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	agent := &Principal{UserID: owner.UserID, AgentID: "a-" + ulid.NewString(), Project: project}
	art := artifactIn(t, ctx, db, owner, VisibilityProjectOnly, "the note")

	before := time.Now().UTC().Add(-time.Second)
	if _, err := db.TombstoneArtifact(ctx, agent, art.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	// The row stops BEING the artifact - that is artifacts.go's rule and this
	// does not soften it. There is no status left to move and no edit that
	// brings it back; what there is, is a sentence afterwards.
	if _, err := db.ReadArtifact(ctx, owner, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a withdrawn row gave %v, want ErrNotFound", err)
	}

	wd, err := db.ReadWithdrawn(ctx, owner, art.ID, false)
	if err != nil {
		t.Fatalf("the owner could not be told their own row was withdrawn: %v", err)
	}
	if wd.Actor != agent.AgentID {
		t.Errorf("withdrawn by %q, want the agent %q that did it", wd.Actor, agent.AgentID)
	}
	if wd.At.Before(before) || wd.At.After(time.Now().UTC().Add(time.Minute)) {
		t.Errorf("withdrawn at %s, which is not when it happened", wd.At)
	}
	if wd.ID != art.ID || wd.Type != MemoryType || wd.Kind != "note" {
		t.Errorf("the withdrawal is %+v, want the id, type and kind of %s", wd, art.ID)
	}
}

// AND THE LEAK. The permission filter runs in the same WHERE as the tombstone
// test, so a row somebody may not read answers exactly as a row that was never
// written: both come back ErrNotFound, and nothing in the reply distinguishes
// them. Move the filter after the tombstone test - which is the one clause this
// whole surface is about - and the stranger below starts being told that an id
// they cannot read exists, which is an existence oracle over guessable ids.
//
// The same id is asked for by both principals on purpose. A test where the id is
// simply absent for the stranger would pass with no permission check at all.
func TestAWithdrawnRowOutOfReachIsNotThereRatherThanWithdrawn(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "withdraw")
	elsewhere := declaredProject(t, ctx, db, "withdraw-else")

	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	roommate := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	// Personal is the visibility that cost twenty minutes: readable by its
	// owner and by nobody else, in a project full of people who can see the
	// rest of it.
	personal := artifactIn(t, ctx, db, owner, VisibilityPersonal, "the personal note")
	shared := artifactIn(t, ctx, db, owner, VisibilityProjectOnly, "the project note")
	for _, id := range []string{personal.ID, shared.ID} {
		if _, err := db.TombstoneArtifact(ctx, owner, id); err != nil {
			t.Fatalf("withdraw %s: %v", id, err)
		}
	}

	for _, tc := range []struct {
		name string
		p    *Principal
		id   string
		told bool
	}{
		{"the owner of the personal row", owner, personal.ID, true},
		{"a roommate, who never could read the personal row", roommate, personal.ID, false},
		{"the roommate, who could read the project row", roommate, shared.ID, true},
		{"a stranger in another project", stranger, shared.ID, false},
		{"nobody at all", nil, shared.ID, false},
	} {
		wd, err := db.ReadWithdrawn(ctx, tc.p, tc.id, false)
		switch {
		case tc.told && err != nil:
			t.Errorf("%s was not told the row was withdrawn: %v", tc.name, err)
		case tc.told && wd.Actor != owner.UserID:
			t.Errorf("%s was told it was withdrawn by %q, want %q", tc.name, wd.Actor, owner.UserID)
		case !tc.told && !errors.Is(err, ErrNotFound):
			t.Errorf("%s got %v for an id out of reach, want ErrNotFound - "+
				"anything else says the id exists", tc.name, err)
		}
	}
}

// The other two answers that must stay one answer. An id nobody ever wrote and a
// row that is sitting there perfectly readable are both "no withdrawal here":
// the first because there is nothing, the second because being asked "was this
// withdrawn" about a live row must not become a second way to read it.
func TestNeitherAnAbsentIdNorALiveRowIsAWithdrawal(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "withdraw")

	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	live := artifactIn(t, ctx, db, owner, VisibilityProjectOnly, "still here")

	if _, err := db.ReadWithdrawn(ctx, owner, ulid.NewString(), false); !errors.Is(err, ErrNotFound) {
		t.Errorf("an id nobody wrote gave %v, want ErrNotFound", err)
	}
	if _, err := db.ReadWithdrawn(ctx, owner, live.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a live row gave %v, want ErrNotFound - it was never withdrawn", err)
	}
}

// Withdrawing rewrites the fields column, so it has to carry forward everything
// that was already in it. The room is the one that bites: an artifact that loses
// its room loses the place a reader would have found it, and the withdrawal
// keys would be sitting on top of the wreckage looking correct.
func TestWithdrawingKeepsTheFieldsTheRowAlreadyCarried(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "withdraw")

	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := artifactIn(t, ctx, db, owner, VisibilityProjectOnly, "the note")
	if _, err := db.TombstoneArtifact(ctx, owner, art.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	var raw []byte
	if err := db.sql.QueryRowContext(ctx,
		`SELECT fields FROM artifacts WHERE id = $1`, art.ID).Scan(&raw); err != nil {
		t.Fatalf("read fields back: %v", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("the withdrawn row's fields do not parse: %v", err)
	}
	if fields[RoomField] != "general" {
		t.Errorf("the room is %v after withdrawing, want general", fields[RoomField])
	}
	if fields[WithdrawnByField] != owner.UserID {
		t.Errorf("%s is %v, want %s", WithdrawnByField, fields[WithdrawnByField], owner.UserID)
	}
	if _, ok := fields[WithdrawnAtField].(string); !ok {
		t.Errorf("%s is %v, want a moment", WithdrawnAtField, fields[WithdrawnAtField])
	}
}
