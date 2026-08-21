package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

func TestTheWordsThatMeanTheCaller(t *testing.T) {
	for _, word := range []string{"me", "self", "mine", "ME", " me "} {
		if !SelfName(word) {
			t.Errorf("%q is a word somebody types for themselves", word)
		}
	}
	// And the ones that are handles, because every word claimed here is a name
	// a real seat can no longer have.
	for _, word := range []string{"", "meg", "myself", "flowy-claude", "claude-host"} {
		if SelfName(word) {
			t.Errorf("%q was taken for a self-reference and it is a handle", word)
		}
	}
}

// THE BOARD GREW A SEAT CALLED "me". Claimed with {"assignee":"me"}, stored
// verbatim, and a sweep an hour later counted it as a holder alongside the real
// seats. Every coordination question here is "who has this", and the answer was
// a word no roster can resolve - the row looked owned and was unreachable.
//
// Driven through the doors an agent actually uses rather than against
// SelfName, because the defect was never in the predicate: it was that nothing
// called one.
func TestClaimingAsMeStoresTheCallersHandle(t *testing.T) {
	ctx, db, project := lockCtx(t)
	me := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	// A HANDLE NOBODY ELSE USES, for the same reason the id is fresh: this
	// suite shares one database, and a fixed seat here is a seat every other
	// test has to know about.
	handle := "welder-" + ulid.Short()
	if _, err := db.sql.ExecContext(ctx,
		// Same shape claimtodo_test.go uses for a seat with a handle, and NO
		// node column: setting one puts this row in front of tests that count
		// who is on this node, which is how two unrelated checks started
		// failing the first time this test was written.
		`INSERT INTO users (id, handle) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		me.UserID, handle); err != nil {
		t.Fatalf("seat the caller: %v", err)
	}
	// AND TAKEN BACK OUT. This suite shares one database across runs, so a seat
	// left behind is a seat every test that counts them inherits - presence and
	// room-members both went red the first time this test was written, and
	// again on the second run against the same database. A test that adds a row
	// to a table other tests count has to remove it.
	t.Cleanup(func() {
		_, _ = db.sql.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, me.UserID)
	})

	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: me.UserID, Title: "somebody has to do it", Visibility: "project",
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the todo: %v", err)
	}

	got, _, err := db.ClaimTodo(ctx, me, row.ID, "me", "")
	if err != nil {
		t.Fatalf("claim as me: %v", err)
	}
	if AssigneeOf(got) != handle {
		t.Errorf("the row reads %q - a handle no roster resolves is not an owner",
			AssigneeOf(got))
	}

	// The other door, because a fix in one of two doors is how this fleet spent
	// a morning: the MCP side defaulted correctly and the HTTP side did not.
	again, _, err := db.AssignTodo(ctx, me, row.ID, "me", nil)
	if err != nil {
		t.Fatalf("assign as me: %v", err)
	}
	if AssigneeOf(again) != handle {
		t.Errorf("the assign door reads %q", AssigneeOf(again))
	}
}
