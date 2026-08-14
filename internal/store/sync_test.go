package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// remote builds a row as another node would have written it: an id and a clock
// reading that this node did not mint.
func remote(id string, hlc int64, project *string, owner, title string) *Artifact {
	return &Artifact{
		ID: id, Type: "note", Project: project, OwnerUser: owner, Title: title,
		Body: "from a peer", Visibility: "project", HLC: hlc, Node: "peer-node",
	}
}

// TestSyncApplyIsLastWriterWinsByHLC walks the whole merge rule for an
// artifact: a newer reading replaces, an older one is ignored, the same one
// changes nothing, and a tombstone is just another write that has to be newer
// to win.
func TestSyncApplyIsLastWriterWinsByHLC(t *testing.T) {
	ctx, db := open(t)

	project := "pz-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	id := ulid.NewString()
	base := db.Clock().Pack()

	apply := func(a *Artifact) int {
		t.Helper()
		applied, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{a}})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		return applied["artifacts"]
	}
	read := func() *Artifact {
		t.Helper()
		got, err := db.GetArtifact(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		return got
	}

	if n := apply(remote(id, base+10, &project, owner, "first")); n != 1 {
		t.Fatalf("a new id applied %d rows, want 1", n)
	}
	if got := read(); got.Title != "first" || got.HLC != base+10 || got.Node != "peer-node" {
		t.Fatalf("after the first write: %+v", got)
	}

	// Older loses, and loses silently: it is received and not applied.
	if n := apply(remote(id, base+5, &project, owner, "older")); n != 0 {
		t.Fatalf("an older reading applied %d rows, want 0", n)
	}
	if got := read(); got.Title != "first" || got.HLC != base+10 {
		t.Fatalf("an older reading changed the row: %+v", got)
	}

	// The same reading twice is the idempotent case: replaying a delta is not
	// a write.
	if n := apply(remote(id, base+10, &project, owner, "same clock, other text")); n != 0 {
		t.Fatalf("an equal reading applied %d rows, want 0", n)
	}
	if got := read(); got.Title != "first" {
		t.Fatalf("an equal reading changed the row: %+v", got)
	}

	// Newer wins.
	if n := apply(remote(id, base+20, &project, owner, "second")); n != 1 {
		t.Fatalf("a newer reading applied %d rows, want 1", n)
	}
	if got := read(); got.Title != "second" || got.HLC != base+20 {
		t.Fatalf("after the second write: %+v", got)
	}

	// A delete is a row, so it beats an older write and loses to a newer one.
	tomb := remote(id, base+30, &project, owner, "second")
	tomb.Tombstone = true
	if n := apply(tomb); n != 1 {
		t.Fatalf("a tombstone applied %d rows, want 1", n)
	}
	if got := read(); !got.Tombstone {
		t.Fatalf("the tombstone did not land: %+v", got)
	}

	stale := remote(id, base+25, &project, owner, "resurrected")
	if n := apply(stale); n != 0 {
		t.Fatalf("a write older than the delete applied %d rows, want 0", n)
	}
	if got := read(); !got.Tombstone || got.Title == "resurrected" {
		t.Fatalf("an older write undid the delete: %+v", got)
	}

	// And an edit made after the delete brings it back, which is the same rule
	// read the other way round.
	if n := apply(remote(id, base+40, &project, owner, "back")); n != 1 {
		t.Fatalf("a write newer than the delete applied %d rows, want 1", n)
	}
	if got := read(); got.Tombstone || got.Title != "back" {
		t.Fatalf("a newer write did not undo the delete: %+v", got)
	}
}

// TestSyncApplyAdvancesTheClock is the causality half: after merging a peer's
// row, the next reading this node mints has to be newer than the one it just
// applied, or the local edit that follows would lose the next merge.
func TestSyncApplyAdvancesTheClock(t *testing.T) {
	ctx, db := open(t)

	project := "pz-" + ulid.NewString()
	ahead := db.Clock().Pack() + 60_000<<16 // a full minute of wall clock ahead
	art := remote(ulid.NewString(), ahead, &project, "u-"+ulid.NewString(), "from the future")
	if _, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{art}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if next := db.Clock().Pack(); next <= ahead {
		t.Fatalf("clock handed out %d after applying %d; it did not move past the peer", next, ahead)
	}
}

// TestSyncApplyEventsAreAppendOnly asserts the other merge rule: an event is
// inserted when its id is new and ignored when it is not. Nothing about an
// event is ever updated, including by replication.
func TestSyncApplyEventsAreAppendOnly(t *testing.T) {
	ctx, db := open(t)

	project := "pz-" + ulid.NewString()
	first := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: "u-" + ulid.NewString(), Body: "hello", SeqHLC: db.Clock().Pack(),
		Node: "peer-node", Parents: []string{},
	}
	first.Thread = first.ID
	child := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Thread: first.Thread, Parents: []string{first.ID}, Actor: first.Actor,
		Body: "reply", SeqHLC: db.Clock().Pack(), Node: "peer-node",
	}

	set := &SyncSet{Events: []*Event{first, child}}
	applied, err := db.SyncApply(ctx, set)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied["events"] != 2 {
		t.Fatalf("applied %d events, want 2", applied["events"])
	}

	// The DAG survives the trip.
	got, err := db.GetEvent(ctx, child.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Parents) != 1 || got.Parents[0] != first.ID {
		t.Fatalf("parents came back as %v, want [%s]", got.Parents, first.ID)
	}
	if got.SeqHLC != child.SeqHLC || got.Node != "peer-node" {
		t.Fatalf("the event was restamped: %+v", got)
	}

	// The same delta again, with a body that differs: nothing moves.
	child.Body = "edited on the way through"
	applied, err = db.SyncApply(ctx, set)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if applied["events"] != 0 {
		t.Fatalf("re-applying the same events applied %d rows, want 0", applied["events"])
	}
	if got, err = db.GetEvent(ctx, child.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Body != "reply" {
		t.Fatalf("an append-only row was updated: %q", got.Body)
	}
}

// TestSyncPullIsPermissionFiltered is the claim that replication is a client of
// the permission model rather than a way round it: a peer pulling as a
// principal gets exactly what that principal could have read one row at a time.
func TestSyncPullIsPermissionFiltered(t *testing.T) {
	ctx, db := open(t)

	pa := "pa-" + ulid.NewString()
	pb := "pb-" + ulid.NewString()
	alice := &User{Handle: "alice-" + ulid.NewString()}
	bob := &User{Handle: "bob-" + ulid.NewString()}
	for _, u := range []*User{alice, bob} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	since := db.Clock().Pack()

	shared := &Artifact{Type: "note", Project: &pa, OwnerUser: alice.ID, Title: "shared",
		Visibility: "project"}
	personal := &Artifact{Type: "note", OwnerUser: alice.ID, Title: "personal",
		Visibility: "personal"}
	for _, a := range []*Artifact{shared, personal} {
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("upsert artifact: %v", err)
		}
	}

	peer := &Principal{UserID: bob.ID, Project: pb}
	has := func(set *SyncSet, id string) bool {
		for _, a := range set.Artifacts {
			if a.ID == id {
				return true
			}
		}
		return false
	}

	set, err := db.SyncPull(ctx, peer, SyncQuery{Since: since})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if has(set, shared.ID) {
		t.Fatalf("a peer with no grant pulled an artifact in %s", pa)
	}
	if has(set, personal.ID) {
		t.Fatalf("a peer pulled somebody else's personal artifact")
	}

	// The grant that opens pa up to pb is the grant that replicates it.
	if err := db.InsertGrant(ctx, &Grant{FromProject: pb, ToProject: pa, Cap: "read",
		GrantedBy: alice.ID}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	set, err = db.SyncPull(ctx, peer, SyncQuery{Since: since})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !has(set, shared.ID) {
		t.Fatalf("a peer holding a read grant on %s did not pull the artifact in it", pa)
	}
	if has(set, personal.ID) {
		t.Fatalf("the personal floor did not hold across a grant")
	}
	if set.HWM < shared.HLC {
		t.Fatalf("high water mark %d is below the row it returned (%d)", set.HWM, shared.HLC)
	}

	// A cursor at the high water mark is caught up.
	set, err = db.SyncPull(ctx, peer, SyncQuery{Since: set.HWM})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if has(set, shared.ID) {
		t.Fatalf("a pull from the high water mark returned a row it had already handed over")
	}
}

// TestSyncPullPagesAndHoldsTheCursorBack asserts the paging rule: when a table
// fills its page, the high water mark stays at the end of that page, so nothing
// above it is skipped by a caller that stores the cursor and comes back.
func TestSyncPullPagesAndHoldsTheCursorBack(t *testing.T) {
	ctx, db := open(t)

	project := "pp-" + ulid.NewString()
	owner := &User{Handle: "pager-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	peer := &Principal{UserID: owner.ID, Project: project}

	since := db.Clock().Pack()
	ids := make([]string, 5)
	for i := range ids {
		a := &Artifact{Type: "note", Project: &project, OwnerUser: owner.ID,
			Title: "page me", Visibility: "project"}
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		ids[i] = a.ID
	}

	cursor := since
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		set, err := db.SyncPull(ctx, peer, SyncQuery{Since: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if len(set.Artifacts) == 0 {
			break
		}
		if len(set.Artifacts) > 2 {
			t.Fatalf("a page of 2 returned %d rows", len(set.Artifacts))
		}
		for _, a := range set.Artifacts {
			seen[a.ID] = true
		}
		if set.HWM <= cursor {
			t.Fatalf("high water mark %d did not advance past the cursor %d", set.HWM, cursor)
		}
		cursor = set.HWM
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("paging skipped %s", id)
		}
	}
}

// TestPeerCursorsOnlyMoveForward keeps the bookmarks honest: a cursor that went
// backwards would replay rows for ever.
func TestPeerCursorsOnlyMoveForward(t *testing.T) {
	ctx, db := open(t)

	peer := "http://peer-" + ulid.NewString() + ":8787"
	if err := db.RegisterPeer(ctx, peer); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Registering twice is how every sync starts, and it must not reset a cursor.
	if err := db.AdvancePullCursor(ctx, peer, 100); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := db.AdvancePushedCursor(ctx, peer, 200); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := db.RegisterPeer(ctx, peer); err != nil {
		t.Fatalf("re-register: %v", err)
	}

	got, err := db.GetPeer(ctx, peer)
	if err != nil {
		t.Fatalf("get peer: %v", err)
	}
	if got.PullCursor != 100 || got.PushedCursor != 200 {
		t.Fatalf("re-registering moved the cursors: %+v", got)
	}

	if err := db.AdvancePullCursor(ctx, peer, 50); err != nil {
		t.Fatalf("advance backwards: %v", err)
	}
	if err := db.AdvancePushedCursor(ctx, peer, 50); err != nil {
		t.Fatalf("advance backwards: %v", err)
	}
	if got, err = db.GetPeer(ctx, peer); err != nil {
		t.Fatalf("get peer: %v", err)
	}
	if got.PullCursor != 100 || got.PushedCursor != 200 {
		t.Fatalf("a cursor went backwards: %+v", got)
	}

	if _, err := db.GetPeer(ctx, "http://never-registered:1"); err == nil {
		t.Fatal("an unknown peer read back without an error")
	}
}

// TestCheckEventIsWhatTheAPIWouldHaveAllowed is the rule a pushed event has to
// clear, and it needs no database: an event is signed by the principal handing
// it over, lands in that principal's project, and is not one of the types this
// node's own handlers mint.
//
// Without it, POST /api/sync/push was a way to write the log under anybody's
// name - a status move nobody made, a message from somebody who never said it -
// and to have every peer hold the forgery afterwards.
func TestCheckEventIsWhatTheAPIWouldHaveAllowed(t *testing.T) {
	pa, pb := "pa", "pb"
	peer := &Principal{UserID: "u-peer", Project: pb}
	agent := &Principal{UserID: "u-peer", AgentID: "a-peer", Project: pb}
	event := func(kind, actor string, project *string) *Event {
		return &Event{ID: "e1", Type: kind, Actor: actor, Project: project}
	}

	for _, tc := range []struct {
		what  string
		p     *Principal
		e     *Event
		allow bool
	}{
		{"its own chat in its own project", peer, event("chat", "u-peer", &pb), true},
		{"its own note with no project", peer, event("note", "u-peer", nil), true},
		{"an agent's own message", agent, event("chat", "a-peer", &pb), true},
		{"somebody else's chat", peer, event("chat", "u-other", &pb), false},
		{"its own chat in another project", peer, event("chat", "u-peer", &pa), false},
		{"a status move nobody made", peer, event("status", "u-peer", &pb), false},
		{"a handoff nobody handed over", peer, event("task", "u-peer", &pb), false},
		{"something the forge bridge did not do", peer, event("forge", "u-peer", &pb), false},
		{"an unsigned event", peer, event("chat", "", &pb), false},
		{"an agent posting as its user", agent, event("chat", "u-peer", &pb), false},
	} {
		why := checkEvent(tc.p, tc.e)
		if tc.allow && why != "" {
			t.Errorf("%s was refused: %s", tc.what, why)
		}
		if !tc.allow && why == "" {
			t.Errorf("%s was taken", tc.what)
		}
	}

	// The operator is this node's own administration - the pull side, run by
	// whoever owns the machine - and is not filtered at all.
	if why := checkEvent(nil, event("status", "anybody", &pa)); why != "" {
		t.Errorf("the operator's own merge was refused: %s", why)
	}
}

// TestSyncApplyAsRefusesAForgedEvent is the same rule through the merge, which
// is where it matters: a refused row is counted and not written, and the rest
// of the delta still lands.
func TestSyncApplyAsRefusesAForgedEvent(t *testing.T) {
	ctx, db := open(t)

	project := "pe-" + ulid.NewString()
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	at := db.Clock().Pack()

	mine := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: peer.UserID, Body: "mine", SeqHLC: at + 1, Node: "peer-node"}
	forged := &Event{ID: ulid.NewString(), Type: "status", Project: &project,
		Actor: "u-somebody-else", Body: "open->done", SeqHLC: at + 2, Node: "peer-node"}

	res, err := db.SyncApplyAs(ctx, peer, &SyncSet{Events: []*Event{mine, forged}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied["events"] != 1 || res.Refused["events"] != 1 {
		t.Fatalf("applied %d and refused %d events, want one of each: %+v",
			res.Applied["events"], res.Refused["events"], res.Reasons)
	}
	if _, err := db.GetEvent(ctx, forged.ID); err == nil {
		t.Fatal("the forged event was written")
	}
	if _, err := db.GetEvent(ctx, mine.ID); err != nil {
		t.Fatalf("the pusher's own event was not written: %v", err)
	}
}
