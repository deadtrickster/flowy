package store

import (
	"encoding/json"
	"errors"
	"sync/atomic"
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
	base := packed(t, db)

	apply := func(a *Artifact) int {
		t.Helper()
		applied, err := db.SyncApply(ctx, fromPeer(t, ctx, db, &SyncSet{Artifacts: []*Artifact{a}}))
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
	ahead := packed(t, db) + 60_000<<16 // a full minute of wall clock ahead
	art := remote(ulid.NewString(), ahead, &project, "u-"+ulid.NewString(), "from the future")
	if _, err := db.SyncApply(ctx, fromPeer(t, ctx, db, &SyncSet{Artifacts: []*Artifact{art}})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if next := packed(t, db); next <= ahead {
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
		Actor: "u-" + ulid.NewString(), Body: "hello", SeqHLC: packed(t, db),
		Node: "peer-node", Parents: []string{},
	}
	first.Thread = first.ID
	child := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Thread: first.Thread, Parents: []string{first.ID}, Actor: first.Actor,
		Body: "reply", SeqHLC: packed(t, db), Node: "peer-node",
	}

	set := &SyncSet{Events: []*Event{first, child}}
	applied, err := db.SyncApply(ctx, fromPeer(t, ctx, db, set))
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
	applied, err = db.SyncApply(ctx, fromPeer(t, ctx, db, set))
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

	since := packed(t, db)

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

	since := packed(t, db)
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
	at := packed(t, db)

	mine := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: peer.UserID, Body: "mine", SeqHLC: at + 1, Node: "peer-node"}
	forged := &Event{ID: ulid.NewString(), Type: "status", Project: &project,
		Actor: "u-somebody-else", Body: "open->done", SeqHLC: at + 2, Node: "peer-node"}

	res, err := db.SyncApplyAs(ctx, peer, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{mine, forged}}))
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

// TestPulledProjectGrantNeedsALocalOpener is the one row a peer could write
// itself.
//
// A project-wide grant is a capability that lands in to_project. A peer serving
// a page could name this principal's own project there, name a from_project of
// its own, say anybody at all granted it, and the pull side applied it without
// asking anything: from the next pull onwards the peer's project reads this one,
// and because merging is last-writer-wins the forgery outlives being noticed.
//
// So a grant that opens this principal's project up is taken only when its
// grantor is somebody who holds a principal here in that project. Grants between
// other projects, riding a page this principal may read, are federation and are
// untouched.
func TestPulledProjectGrantNeedsALocalOpener(t *testing.T) {
	ctx, db := open(t)

	home := "ph-" + ulid.NewString()   // the puller's project: the one being opened
	theirs := "pi-" + ulid.NewString() // the peer's
	third := "pj-" + ulid.NewString()  // somebody else's entirely

	me := &User{Handle: "opener-" + ulid.NewString()}
	if err := db.InsertUser(ctx, me); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// The token is what makes me a principal of home on this node, which is
	// what the check looks for.
	if err := db.InsertToken(ctx, &Principal{
		Token: "t-" + ulid.NewString(), UserID: me.ID, Project: home,
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	puller := &Principal{UserID: me.ID, Project: home}

	at := packed(t, db)
	forged := Grant{
		ID: ulid.NewString(), FromProject: theirs, ToProject: home, Cap: "read",
		GrantedBy: "u-stranger-" + ulid.NewString(), HLC: at + 1, Node: "peer-node",
	}
	// The same shape, opened by somebody who is here and is in home: that is a
	// grant this node could have issued, arriving back from a peer.
	ours := Grant{
		ID: ulid.NewString(), FromProject: theirs, ToProject: home, Cap: "read",
		GrantedBy: me.ID, HLC: at + 2, Node: "peer-node",
	}
	// And one between two projects that are neither of ours, reaching this
	// principal because it names them as the subject. Refusing this would be
	// refusing federation.
	elsewhere := Grant{
		ID: ulid.NewString(), FromProject: third, ToProject: theirs, Cap: "read",
		Subject: me.ID, GrantedBy: "u-" + ulid.NewString(), HLC: at + 3, Node: "peer-node",
	}

	res, err := db.SyncApplyFrom(ctx, puller,
		fromPeer(t, ctx, db, &SyncSet{Grants: []Grant{forged, ours, elsewhere}}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied["grants"] != 2 || res.Refused["grants"] != 1 {
		t.Fatalf("applied %d and refused %d grants, want 2 and 1: %+v",
			res.Applied["grants"], res.Refused["grants"], res.Reasons)
	}
	if grantRows(t, db, forged.ID) != 0 {
		t.Errorf("the forged grant opened %s up: %s says anyone can", home, forged.GrantedBy)
	}
	if grantRows(t, db, ours.ID) != 1 {
		t.Errorf("a grant opened by a principal of %s did not come back", home)
	}
	if grantRows(t, db, elsewhere.ID) != 1 {
		t.Errorf("a grant between %s and %s was refused: that is federation", third, theirs)
	}
}

// grantRows counts the rows of the grants table with one id.
func grantRows(t *testing.T, db *DB, id string) int {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM grants WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	return n
}

// TestPulledMintedEventIsRefused holds the minted rule on the pull side too.
//
// checkEvent refuses a pushed status, task or forge event because it is a claim
// the pusher is not entitled to make. A pulled one was taken without question,
// which is the same forgery through the other door: a peer serving a page wrote
// this node's own history for it - a lifecycle move nobody made, a handoff
// nobody handed over - and every peer downstream then held it as well.
//
// The chat event beside it still lands: refusing conversations would be
// refusing federation, and chat carries no authority the peer did not already
// have.
func TestPulledMintedEventIsRefused(t *testing.T) {
	ctx, db := open(t)

	project := "pn-" + ulid.NewString()
	puller := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	at := packed(t, db)

	said := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: "u-over-there", Body: "said on the other node", SeqHLC: at + 1, Node: "peer-node"}

	res, err := db.SyncApplyFrom(ctx, puller, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{said}}))
	if err != nil {
		t.Fatalf("apply the chat: %v", err)
	}
	if res.Applied["events"] != 1 {
		t.Fatalf("a pulled chat event applied %d rows, want 1: %+v", res.Applied["events"], res.Reasons)
	}

	for _, kind := range []string{"status", "task", "forge"} {
		minted := &Event{ID: ulid.NewString(), Type: kind, Project: &project,
			Actor: "u-over-there", Body: "open->done", SeqHLC: at + 10, Node: "peer-node"}
		res, err := db.SyncApplyFrom(ctx, puller, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{minted}}))
		if err != nil {
			t.Fatalf("apply the %s event: %v", kind, err)
		}
		if res.Applied["events"] != 0 || res.Refused["events"] != 1 {
			t.Errorf("a pulled %s event applied %d and was refused %d times, want 0 and 1: %+v",
				kind, res.Applied["events"], res.Refused["events"], res.Reasons)
		}
		if _, err := db.GetEvent(ctx, minted.ID); err == nil {
			t.Errorf("a %s event this node never did is in its own trail", kind)
		}
	}
}

// TestSyncApplyObservesTheClockAfterTheCommit is where the clock is allowed to
// learn what a page carried.
//
// The merge used to observe each row's reading as it applied it, inside the
// transaction and before the commit that decides whether any of those rows
// exist. A page that failed halfway rolled its writes back and left the clock
// standing past readings this node does not hold - and nothing puts that back.
// Every write afterwards is stamped above rows that are not here, so the peer
// that does have them loses every merge against a node that never applied them.
func TestSyncApplyObservesTheClockAfterTheCommit(t *testing.T) {
	ctx, db := open(t)

	project := "pq-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)
	applied := at + 1000

	// One good artifact and, behind it, an event whose meta is not JSON: the
	// insert fails, so the whole page rolls back with the artifact in it.
	page := &SyncSet{
		Artifacts: []*Artifact{remote(ulid.NewString(), applied, &project, owner, "the good one")},
		Events: []*Event{{
			ID: ulid.NewString(), Type: "chat", Project: &project, Actor: owner,
			Parents: []string{}, Body: "the one that cannot be written",
			Meta: []byte(`{not json`), SeqHLC: at + 2000, Node: "peer-node",
		}},
	}
	if _, err := db.SyncApply(ctx, fromPeer(t, ctx, db, page)); err == nil {
		t.Fatal("a page whose event cannot be written reported success")
	}

	// Nothing was applied, so nothing should have reached the clock.
	if now := db.Clock().Reading().Pack(); now >= applied {
		t.Errorf("the clock reads %d after a page that rolled back, which is past the %d it "+
			"carried: the node is now stamping above rows it does not have", now, applied)
	}

	// And the happy path still moves it: the guarantee is that a reading is
	// observed once it is committed, not that it is never observed.
	good := remote(ulid.NewString(), applied, &project, owner, "the one that lands")
	if _, err := db.SyncApply(ctx, fromPeer(t, ctx, db, &SyncSet{Artifacts: []*Artifact{good}})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if now := db.Clock().Reading().Pack(); now < applied {
		t.Errorf("the clock reads %d after committing a row at %d, want at least that",
			now, applied)
	}
}

// TestPushedNewArtifactIsThePushersOwn holds the push rule for a row that is
// not here yet.
//
// checkArtifact's third rule is the one that stops a merge changing hands, and
// on a push it is stricter: the row has to be the pusher's own. But it was
// written against the row already in the table, and a new id matches nothing -
// so the check returned early and the rule never fired. A peer could push a
// brand new artifact into any project it can reach with anybody at all in
// owner_user: authorship forged at the door, replicated onward from here, and
// the name it forged then holding the update and tombstone rights that column
// carries.
//
// The pusher's own new row still lands, which is what a push is for.
func TestPushedNewArtifactIsThePushersOwn(t *testing.T) {
	ctx, db := open(t)

	project := "pk-" + ulid.NewString()
	pusher := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	at := packed(t, db)

	forged := remote(ulid.NewString(), at+1, &project, "u-somebody-else", "signed by somebody else")
	own := remote(ulid.NewString(), at+2, &project, pusher.UserID, "the pusher's own")
	// And the unsigned one, which is the same forgery with the name left out:
	// a row nobody here can be said to own.
	unsigned := remote(ulid.NewString(), at+3, &project, "", "signed by nobody")

	res, err := db.SyncApplyAs(ctx, pusher, fromPeer(t, ctx, db, &SyncSet{
		Artifacts: []*Artifact{forged, own, unsigned},
	}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied["artifacts"] != 1 || res.Refused["artifacts"] != 2 {
		t.Fatalf("applied %d and refused %d artifacts, want 1 and 2: %+v",
			res.Applied["artifacts"], res.Refused["artifacts"], res.Reasons)
	}
	if n := rows(t, db, "artifacts", forged.ID); n != 0 {
		t.Errorf("a new row owned by %s was pushed in by %s (%d rows): forged authorship",
			forged.OwnerUser, pusher.UserID, n)
	}
	if n := rows(t, db, "artifacts", unsigned.ID); n != 0 {
		t.Errorf("a new row owned by nobody was pushed in (%d rows)", n)
	}
	if n := rows(t, db, "artifacts", own.ID); n != 1 {
		t.Errorf("the pusher's own new row did not land (%d rows): that is what a push is", n)
	}
}

// TestPushedTaskAboutAProjectOnlyArtifactIsRefused is the read filter's second
// floor, held at the sync door.
//
// A 'project-only' artifact is the project it is in and nothing else: the CASE
// in ArtifactFilterSQL takes that branch and the grant and share tests below it
// are never reached. So the share an assignment writes for one can never take
// effect, and the task that comes with it points at an artifact the assignee
// gets a 404 on - the riddle POST /api/assign exists to refuse. checkTask
// excluded only the personal floor, so a handoff about one replicated in.
func TestPushedTaskAboutAProjectOnlyArtifactIsRefused(t *testing.T) {
	ctx, db := open(t)

	project := "pl-" + ulid.NewString()
	from := &User{Handle: "from-" + ulid.NewString()}
	to := &User{Handle: "to-" + ulid.NewString()}
	for _, u := range []*User{from, to} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	pusher := &Principal{UserID: from.ID, Project: project}

	narrow := &Artifact{Type: "bug", Project: &project, OwnerUser: from.ID,
		Title: "the one no grant reaches", Visibility: VisibilityProjectOnly}
	wide := &Artifact{Type: "bug", Project: &project, OwnerUser: from.ID,
		Title: "the one a share can reach", Visibility: VisibilityProject}
	for _, a := range []*Artifact{narrow, wide} {
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("upsert artifact: %v", err)
		}
	}

	at := packed(t, db)
	handoff := func(art *Artifact, hlc int64) *Task {
		return &Task{
			ID: ulid.NewString(), Artifact: art.ID, FromUser: from.ID, ToUser: to.ID,
			Project: project, State: TaskOpen, Thread: ulid.NewString(),
			HLC: hlc, Node: "peer-node",
		}
	}
	unopenable, real := handoff(narrow, at+1), handoff(wide, at+2)

	res, err := db.SyncApplyAs(ctx, pusher, fromPeer(t, ctx, db, &SyncSet{Tasks: []*Task{unopenable, real}}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied["tasks"] != 1 || res.Refused["tasks"] != 1 {
		t.Fatalf("applied %d and refused %d tasks, want one of each: %+v",
			res.Applied["tasks"], res.Refused["tasks"], res.Reasons)
	}
	if n := rows(t, db, "tasks", unopenable.ID); n != 0 {
		t.Errorf("a handoff about a project-only artifact landed (%d rows): %s gets a 404 on it",
			n, to.ID)
	}
	if n := rows(t, db, "tasks", real.ID); n != 1 {
		t.Errorf("an ordinary handoff was refused (%d rows)", n)
	}
}

// TestPulledArtifactShareIsStillTheOwnersToGive is the share the pull side
// waved through.
//
// checkGrant's pull half asked two things: that the grant reaches this
// principal at all, and - only when the grant said this principal signed it -
// that they could have issued it. The reach test is satisfied by naming the
// puller as the subject, so a share of somebody else's artifact, granted by
// anybody at all, matched neither branch and was taken. From then on the
// puller reads that artifact for good: the share clause in ArtifactFilterSQL
// asks for the artifact and the subject and never for the grantor, and the
// forged row pushes onward from here like any other.
//
// Signing does not close it. The peer signs its own row with its own pinned
// key, so the row is authentic - it is the authority behind it that is
// invented, and that is asked of the artifact this node holds rather than of
// the claim on the row.
func TestPulledArtifactShareIsStillTheOwnersToGive(t *testing.T) {
	ctx, db := open(t)

	home := "pa-" + ulid.NewString()   // where the artifact and its owner live
	theirs := "pb-" + ulid.NewString() // where the puller does

	owner := &User{Handle: "owner-" + ulid.NewString()}
	reader := &User{Handle: "reader-" + ulid.NewString()}
	for _, u := range []*User{owner, reader} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	art := &Artifact{
		Type: "bug", Project: &home, OwnerUser: owner.ID,
		Title: "not the peer's to hand out", Visibility: VisibilityProject,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	puller := &Principal{UserID: reader.ID, Project: theirs}

	// The baseline: nothing about the puller reaches this artifact.
	if _, err := db.ReadArtifact(ctx, puller, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the fixture already lets %s read %s: %v", reader.ID, art.ID, err)
	}

	at := packed(t, db)
	share := func(grantedBy string, hlc int64) Grant {
		return Grant{
			ID: ulid.NewString(), FromProject: home, ToProject: theirs,
			Artifact: art.ID, Subject: reader.ID, Cap: "read",
			GrantedBy: grantedBy, HLC: hlc, Node: "peer-node",
		}
	}
	forged := share("u-stranger-"+ulid.NewString(), at+1)

	res, err := db.SyncApplyFrom(ctx, puller, fromPeer(t, ctx, db, &SyncSet{Grants: []Grant{forged}}))
	if err != nil {
		t.Fatalf("apply the forged share: %v", err)
	}
	if res.Applied["grants"] != 0 || res.Refused["grants"] != 1 {
		t.Fatalf("a share granted by %s applied %d and was refused %d times, want 0 and 1: %+v",
			forged.GrantedBy, res.Applied["grants"], res.Refused["grants"], res.Reasons)
	}
	if n := grantRows(t, db, forged.ID); n != 0 {
		t.Errorf("the forged share is here (%d rows): %s reads %s from now on", n, reader.ID, art.ID)
	}
	if _, err := db.ReadArtifact(ctx, puller, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("%s can read %s after a share nobody who owns it wrote: %v", reader.ID, art.ID, err)
	}

	// The same forgery with a row for the artifact beside it, which is the
	// shape a cross-project handoff really has: the share and the artifact it
	// opens arrive together, and each is checked against the other. Here the
	// peer names itself the owner of a row that is already here, so both halves
	// go - the artifact because a merge does not change hands, and the share
	// because the owner it claims is not the owner this node holds.
	rewrite := &Artifact{
		ID: art.ID, Type: "bug", Project: &theirs, OwnerUser: "u-evil-" + ulid.NewString(),
		Title: "mine now", Visibility: VisibilityProject, HLC: at + 10, Node: "peer-node",
	}
	pair := share(rewrite.OwnerUser, at+11)
	res, err = db.SyncApplyFrom(ctx, puller, fromPeer(t, ctx, db,
		&SyncSet{Artifacts: []*Artifact{rewrite}, Grants: []Grant{pair}}))
	if err != nil {
		t.Fatalf("apply the pair: %v", err)
	}
	if res.Applied["grants"] != 0 || res.Applied["artifacts"] != 0 {
		t.Fatalf("a share carrying its own version of the artifact applied %d grants and "+
			"%d artifacts, want none of either: %+v",
			res.Applied["grants"], res.Applied["artifacts"], res.Reasons)
	}
	if n := grantRows(t, db, pair.ID); n != 0 {
		t.Errorf("the paired share is here (%d rows)", n)
	}
	if here, err := db.GetArtifact(ctx, art.ID); err != nil {
		t.Fatalf("read the artifact back: %v", err)
	} else if here.OwnerUser != owner.ID || here.Title != art.Title {
		t.Errorf("the artifact changed hands: %+v", here)
	}
	if _, err := db.ReadArtifact(ctx, puller, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("%s can read %s after a share that came with its own owner: %v",
			reader.ID, art.ID, err)
	}

	// And the owner's own share of the same artifact, arriving the same way,
	// still lands: that is what federation is, and the grantor is not the
	// principal carrying it.
	real := share(owner.ID, at+2)
	res, err = db.SyncApplyFrom(ctx, puller, fromPeer(t, ctx, db, &SyncSet{Grants: []Grant{real}}))
	if err != nil {
		t.Fatalf("apply the owner's share: %v", err)
	}
	if res.Applied["grants"] != 1 {
		t.Fatalf("the owner's own share applied %d grants, want 1: %+v",
			res.Applied["grants"], res.Reasons)
	}
	if n := grantRows(t, db, real.ID); n != 1 {
		t.Fatalf("the owner's share is not here (%d rows)", n)
	}
	if _, err := db.ReadArtifact(ctx, puller, art.ID, false); err != nil {
		t.Errorf("%s cannot read %s after the owner shared it: %v", reader.ID, art.ID, err)
	}
}

// TestPulledNewTaskIsTheOwnersHandoffIntoAFreshThread is the other half of the
// same thing: a task is a read capability, and minting one was open to anybody
// who could read the artifact.
//
// The tasks clause in EventFilterSQL shows a whole thread to from_user, to_user
// and the agent a task was delegated to. The new-task branch asked only that
// the carrier was a party to the row, could read the artifact and could read
// the thread - never that they could have opened the handoff. POST /api/assign
// requires the assigner to own the artifact and opens a thread of its own, so a
// principal who merely reads an artifact could carry in a task naming any
// thread they can see and any local user in to_user, and hand that user the
// conversation. assignee_agent was guarded; to_user was not.
func TestPulledNewTaskIsTheOwnersHandoffIntoAFreshThread(t *testing.T) {
	ctx, db := open(t)

	home := "pc-" + ulid.NewString()    // the artifact, the thread and the mate
	outside := "pd-" + ulid.NewString() // the person being handed the read

	owner := &User{Handle: "owner-" + ulid.NewString()}
	mate := &User{Handle: "mate-" + ulid.NewString()}
	outsider := &User{Handle: "outsider-" + ulid.NewString()}
	for _, u := range []*User{owner, mate, outsider} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	art := &Artifact{
		Type: "bug", Project: &home, OwnerUser: owner.ID,
		Title: "the work", Visibility: VisibilityProject,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	// A conversation that is already here, in the project, which the mate reads
	// because they are in it.
	said := &Event{
		Type: "chat", Project: &home, Room: "general", Parents: []string{},
		Actor: owner.ID, Body: "what we are actually doing about this",
	}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("append: %v", err)
	}

	carrier := &Principal{UserID: mate.ID, Project: home}
	stranger := &Principal{UserID: outsider.ID, Project: outside}

	if _, err := db.ReadEvent(ctx, stranger, said.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the fixture already lets %s read the thread: %v", outsider.ID, err)
	}

	at := packed(t, db)
	forged := &Task{
		ID: ulid.NewString(), Artifact: art.ID, FromUser: mate.ID, ToUser: outsider.ID,
		Project: home, State: TaskOpen, Thread: said.Thread, HLC: at + 1, Node: "peer-node",
	}
	res, err := db.SyncApplyFrom(ctx, carrier, fromPeer(t, ctx, db, &SyncSet{Tasks: []*Task{forged}}))
	if err != nil {
		t.Fatalf("apply the forged handoff: %v", err)
	}
	if res.Applied["tasks"] != 0 || res.Refused["tasks"] != 1 {
		t.Fatalf("a handoff minted by %s applied %d and was refused %d times, want 0 and 1: %+v",
			mate.ID, res.Applied["tasks"], res.Refused["tasks"], res.Reasons)
	}
	if n := rows(t, db, "tasks", forged.ID); n != 0 {
		t.Errorf("the forged handoff is here (%d rows)", n)
	}
	if _, err := db.ReadEvent(ctx, stranger, said.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("%s reads a conversation they were named into by somebody who does not own %s: %v",
			outsider.ID, art.ID, err)
	}

	// The real thing still replicates: the owner hands the work over, into a
	// thread nothing has been said in - which is what an assignment made on
	// another node looks like when it arrives, because the `task` event that
	// opens it is minted and refused at every wire path.
	real := &Task{
		ID: ulid.NewString(), Artifact: art.ID, FromUser: owner.ID, ToUser: outsider.ID,
		Project: home, State: TaskOpen, Thread: ulid.NewString(), HLC: at + 2, Node: "peer-node",
	}
	res, err = db.SyncApplyFrom(ctx, &Principal{UserID: owner.ID, Project: home},
		fromPeer(t, ctx, db, &SyncSet{Tasks: []*Task{real}}))
	if err != nil {
		t.Fatalf("apply the owner's handoff: %v", err)
	}
	if res.Applied["tasks"] != 1 {
		t.Fatalf("the owner's own handoff applied %d tasks, want 1: %+v",
			res.Applied["tasks"], res.Reasons)
	}
	if n := rows(t, db, "tasks", real.ID); n != 1 {
		t.Errorf("the owner's handoff is not here (%d rows)", n)
	}
}

// TestPulledEventCannotClaimSomebodyElsesName is the pull door's answer to
// attribution.
//
// A pulled event was checked for three things - not a minted type, lands
// somewhere this principal reads, thread it may write into - and for nothing at
// all about who it says it is from. Every row on a peer's page carries that
// peer's own valid signature, and a signature says the node wrote the bytes,
// not that the actor column is honest: so a hostile peer put a chat event
// naming somebody else into a page, it verified, and it landed rendered
// everywhere as that person - permanently, because the log is append-only, and
// onward, because the next peer pulls it too. Operator pinning does not help by
// itself: the forgery is genuinely signed by the pinned peer's key.
//
// So attribution is answered with the one thing this node decides for itself:
// whether its operator pinned the writing node. From a pinned node an event
// says who wrote it and is believed, which is what makes ordinary federation
// work. From a node whose key merely turned up on a page it may say only what
// this principal could have said itself, in the actor column and in the meta
// beside it.
func TestPulledEventCannotClaimSomebodyElsesName(t *testing.T) {
	ctx, db := open(t)

	project := "pk-" + ulid.NewString()
	me := "u-" + ulid.NewString()
	alice := "u-alice-" + ulid.NewString()
	puller := &Principal{UserID: me, Project: project}
	at := packed(t, db)

	// A node this one has never heard of, whose key rides in on the page:
	// trust on first use, which is nobody's decision.
	relay := "relay-" + ulid.NewString()
	relayKey := testKey(relay)

	forged := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: alice, Body: "alice never said this", SeqHLC: at + 1, Node: relay,
		Meta: json.RawMessage(`{"actor_kind":"user","actor_user":"` + alice + `"}`),
	}
	SignEvent(relayKey, forged)

	res, err := db.SyncApplyFrom(ctx, puller, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(relay)}, Events: []*Event{forged},
	})
	if err != nil {
		t.Fatalf("apply the forged event: %v", err)
	}
	if res.Applied["events"] != 0 || res.Refused["events"] != 1 {
		t.Fatalf("an event under %s's name from unpinned %s applied %d and was refused %d, "+
			"want 0 and 1: %+v", alice, relay, res.Applied["events"], res.Refused["events"], res.Reasons)
	}
	if _, err := db.GetEvent(ctx, forged.ID); err == nil {
		t.Errorf("%s is in the log saying something they never said", alice)
	}
	// The key itself was taken, which is what makes the refusal about
	// attribution rather than about not being able to verify anything.
	held, err := db.GetIdentity(ctx, relay)
	if err != nil {
		t.Fatalf("the relay's key did not arrive: %v", err)
	}
	if held.Pinned {
		t.Fatalf("%s came out pinned; the test proves nothing", relay)
	}

	// The same peer, this principal's own name in the actor column, and the
	// meta claiming somebody else. Meta is where every reader that cares who is
	// speaking looks, so it is the same forgery through the column beside it.
	viaMeta := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: me, Body: "mine, but it renders as alice", SeqHLC: at + 2, Node: relay,
		Meta: json.RawMessage(`{"actor_kind":"user","actor_user":"` + alice + `","topic":"kept"}`),
	}
	SignEvent(relayKey, viaMeta)

	res, err = db.SyncApplyFrom(ctx, puller, &SyncSet{Events: []*Event{viaMeta}})
	if err != nil {
		t.Fatalf("apply the meta claim: %v", err)
	}
	if res.Applied["events"] != 0 || res.Refused["events"] != 1 {
		t.Fatalf("an event whose meta names %s applied %d and was refused %d, want 0 and 1: %+v",
			alice, res.Applied["events"], res.Refused["events"], res.Reasons)
	}
	if _, err := db.GetEvent(ctx, viaMeta.ID); err == nil {
		t.Errorf("the meta forgery is in the log")
	}

	// This principal's own message, relayed by the same unpinned node, still
	// lands: the refusal is about attribution and not about relaying.
	own := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: me, Body: "and this one really is mine", SeqHLC: at + 3, Node: relay,
		Meta: json.RawMessage(`{"actor_kind":"user","actor_user":"` + me + `"}`),
	}
	SignEvent(relayKey, own)
	res, err = db.SyncApplyFrom(ctx, puller, &SyncSet{Events: []*Event{own}})
	if err != nil {
		t.Fatalf("apply my own event: %v", err)
	}
	if res.Applied["events"] != 1 {
		t.Fatalf("my own message relayed by %s applied %d events, want 1: %+v",
			relay, res.Applied["events"], res.Reasons)
	}

	// And the legitimate relay federation is made of: alice's message, written
	// on a node the operator pinned, arrives under alice's name with its meta
	// as she wrote it.
	origin := "origin-" + ulid.NewString()
	originKey := pinTestNode(t, ctx, db, origin)
	real := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: alice, Body: "this one alice did say", SeqHLC: at + 4, Node: origin,
		Meta: json.RawMessage(`{"actor_kind":"user","actor_user":"` + alice + `"}`),
	}
	SignEvent(originKey, real)

	res, err = db.SyncApplyFrom(ctx, puller, &SyncSet{Events: []*Event{real}})
	if err != nil {
		t.Fatalf("apply the real event: %v", err)
	}
	if res.Applied["events"] != 1 {
		t.Fatalf("alice's message from pinned %s applied %d events, want 1: %+v",
			origin, res.Applied["events"], res.Reasons)
	}
	got, err := db.GetEvent(ctx, real.ID)
	if err != nil {
		t.Fatalf("the relayed message is not here: %v", err)
	}
	if got.Actor != alice {
		t.Errorf("the relayed message came out under %q, want %q", got.Actor, alice)
	}
	var meta map[string]string
	if err := json.Unmarshal(got.Meta, &meta); err != nil {
		t.Fatalf("the relayed meta reads back as %q: %v", got.Meta, err)
	}
	if meta["actor_user"] != alice || meta["actor_kind"] != "user" {
		t.Errorf("the relayed meta reads back as %v, want alice speaking as herself", meta)
	}
}

// TestEventCannotNameAnArtifactItCannotRead closes the fourth of an event's
// reference columns.
//
// An event says four things about somebody else's work: who wrote it, what
// thread it is in, what it descends from, and what artifact it is about. The
// first three are checked on the doors they arrive at; the artifact column went
// in on trust, and it is not decoration - the per-artifact share clause in the
// event filter carries the events about an artifact to everybody it is shared
// with, and /api/artifact/{id}/history is gated on reading the artifact rather
// than on reading each event. So a writer holding nothing but a guessed id
// could put entries into what that artifact's readers see, and they replicated
// from there.
func TestEventCannotNameAnArtifactItCannotRead(t *testing.T) {
	ctx, db := open(t)

	// Somebody else's project, and a note in it that no grant reaches.
	theirs := "pt-" + ulid.NewString()
	stranger := "u-" + ulid.NewString()
	closed := &Artifact{
		Type: "note", Project: &theirs, OwnerUser: stranger, Visibility: "project-only",
		Title: "not yours to be about", Body: "quillfetch",
	}
	if err := db.UpsertArtifact(ctx, closed); err != nil {
		t.Fatalf("upsert the closed artifact: %v", err)
	}

	home := "pu-" + ulid.NewString()
	me := "u-" + ulid.NewString()
	p := &Principal{UserID: me, Project: home}

	mine := &Artifact{
		Type: "note", Project: &home, OwnerUser: me, Visibility: "shared",
		Title: "mine to be about", Body: "quillfetch too",
	}
	if err := db.UpsertArtifact(ctx, mine); err != nil {
		t.Fatalf("upsert my artifact: %v", err)
	}

	at := packed(t, db)
	// The same claim at both merge doors: pushed as my own work, and pulled as
	// a page a peer served.
	doors := []struct {
		name string
		mode syncMode
	}{{"push", modePush}, {"pull", modePull}}
	for i, door := range doors {
		injected := &Event{
			ID: ulid.NewString(), Type: "chat", Project: &home, Room: "general",
			Actor: me, Artifact: closed.ID, Body: "into a trail that is not mine",
			SeqHLC: at + int64(i) + 1, Node: "peer-node",
		}
		set := fromPeer(t, ctx, db, &SyncSet{Events: []*Event{injected}})

		var (
			res *SyncResult
			err error
		)
		if door.mode == modePush {
			res, err = db.SyncApplyAs(ctx, p, set)
		} else {
			res, err = db.SyncApplyFrom(ctx, p, set)
		}
		if err != nil {
			t.Fatalf("%s the injected event: %v", door.name, err)
		}
		if res.Applied["events"] != 0 || res.Refused["events"] != 1 {
			t.Fatalf("an event about %s applied %d and was refused %d on the %s door, "+
				"want 0 and 1: %+v", closed.ID, res.Applied["events"], res.Refused["events"],
				door.name, res.Reasons)
		}
		if n := rows(t, db, "events", injected.ID); n != 0 {
			t.Errorf("the %s door left %d rows in %s's trail", door.name, n, closed.ID)
		}
	}

	// An event about an artifact this principal really can read is untouched:
	// what is refused is naming somebody else's, not naming one at all.
	fine := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &home, Room: "general",
		Actor: me, Artifact: mine.ID, Body: "about my own", SeqHLC: at + 3, Node: "peer-node",
	}
	res, err := db.SyncApplyAs(ctx, p, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{fine}}))
	if err != nil {
		t.Fatalf("push the event about my own artifact: %v", err)
	}
	if res.Applied["events"] != 1 {
		t.Fatalf("an event about my own artifact applied %d, want 1: %+v",
			res.Applied["events"], res.Reasons)
	}
}

// TestNewlyVisibleRescanIsBoundedAndBatched holds the rescan to statements
// somebody chose the size of.
//
// A fresh project-wide grant makes every older artifact in a project readable
// at once, below the reader's cursor, and the rescan that finds them ran on the
// serving node inside the request that carried the grant. It read everything
// past the first page in one statement with no LIMIT on it, and then wrote the
// ids down one INSERT at a time and handed them over one UPDATE at a time - so
// one grant row bought O(N) round trips and an N-row answer, with N whatever
// the project holds and nothing on the serving side bounding it. Any principal
// allowed to mint a project-wide grant into its own project and then pull could
// ask for that.
//
// The rows still all land: what is asserted here is that they land a batch at a
// time.
func TestNewlyVisibleRescanIsBoundedAndBatched(t *testing.T) {
	ctx, db, queries := openCounting(t)

	// A small batch, so the batching is visible on a handful of rows instead of
	// on a few thousand.
	was := syncBatch
	syncBatch = 4
	t.Cleanup(func() { syncBatch = was })

	home := "pv-" + ulid.NewString()
	theirs := "pw-" + ulid.NewString()
	me := &User{Handle: "puller-" + ulid.NewString()}
	them := &User{Handle: "holder-" + ulid.NewString()}
	for _, u := range []*User{me, them} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	// More than a page of older work in the other project.
	const total = 21
	want := map[string]bool{}
	for i := 0; i < total; i++ {
		art := &Artifact{
			Type: "note", Project: &theirs, OwnerUser: them.ID, Visibility: "shared",
			Title: "older than the cursor", Body: "brindlewisp",
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert artifact %d: %v", i, err)
		}
		want[art.ID] = true
	}

	// The cursor sits above all of them, and the grant that opens them arrives
	// above the cursor: the case the rescan exists for.
	since := packed(t, db)
	grant := &Grant{FromProject: home, ToProject: theirs, Cap: "read", GrantedBy: me.ID}
	if err := db.InsertGrant(ctx, grant); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	if grant.HLC <= since {
		t.Fatalf("the grant read %d, which is not above the cursor %d", grant.HLC, since)
	}

	p := &Principal{UserID: me.ID, Project: home}
	key := pendingKey(p)
	const page = 4

	resetCounts()
	got, over, err := db.syncNewlyVisible(ctx, p, since, page, []Grant{*grant})
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if err := db.holdPending(ctx, key, over); err != nil {
		t.Fatalf("hold: %v", err)
	}
	reads := atomic.LoadInt64(queries)
	writes := atomic.LoadInt64(&countedExecs)
	widest := atomic.LoadInt64(&countedWidest)

	// Every row the grant opened is accounted for: the page, plus the debt.
	if len(got) != page {
		t.Fatalf("the page came back with %d rows, want %d", len(got), page)
	}
	seen := map[string]bool{}
	for _, art := range got {
		seen[art.ID] = true
	}
	for _, id := range over {
		if seen[id] {
			t.Errorf("%s is both on the page and in the debt", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Fatalf("the rescan accounted for %d of %d artifacts", len(seen), total)
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s was opened by the grant and the rescan lost it", id)
		}
	}

	// And no statement it ran was one the project's size chose: not the reads,
	// which is what the missing LIMIT was, and not the writes, which were one
	// per id.
	if widest > int64(page) {
		t.Errorf("one statement came back with %d rows; nothing here reads more than %d",
			widest, page)
	}
	if bound := int64(total/syncBatch + 2); writes > bound {
		t.Errorf("writing %d ids down took %d statements, want at most %d",
			len(over), writes, bound)
	}
	if bound := int64(total/syncBatch + 3); reads > bound {
		t.Errorf("reading %d ids took %d statements, want at most %d", total, reads, bound)
	}

	// The debt is on the list, and it is the rows themselves that come back off
	// it - bounded statements are not worth much if they lose rows.
	var held int
	if err := db.SQL().QueryRow(
		`SELECT count(*) FROM sync_pending WHERE principal = $1`, key).Scan(&held); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if held != len(over) {
		t.Fatalf("sync_pending holds %d rows for this reader, want %d", held, len(over))
	}
	drained, err := db.drainPending(ctx, p, key, total)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(drained) != len(over) {
		t.Fatalf("draining the debt gave back %d rows, want %d", len(drained), len(over))
	}
	for _, art := range drained {
		if !want[art.ID] {
			t.Errorf("the debt handed over %s, which the grant never opened", art.ID)
		}
	}
}
