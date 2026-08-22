package store

import (
	"context"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The write-behind queue behind the FUSE mount, and the reads the mount's
// directories are made of. These need the database the gate stands up; without
// DATABASE_URL they sit out - see open().

// fsUser mints a person to own the rows one of these tests writes, so two runs
// against the same database do not collide.
func fsUser(t *testing.T, ctx context.Context, db *DB, handle string) *User {
	t.Helper()
	u := &User{Handle: handle + "-" + ulid.NewString(), Display: handle}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u
}

func fsIntent(owner string, project *string, name, content string) *FSIntent {
	return &FSIntent{
		Artifact:  ulid.NewString(),
		Path:      "p/" + owner + "/memory/" + name,
		OwnerUser: owner,
		Actor:     owner,
		Project:   project,
		Type:      "memory",
		Name:      name,
		Hash:      "h-" + name + "-" + content,
		Content:   content,
	}
}

func fsFields(title, body string) FSFields {
	return FSFields{Title: title, Body: body, Kind: "note"}
}

// countEvents is how many log entries name this artifact. It is the assertion
// that matters for "exactly once": the row can be written twice and look the
// same afterwards, and the log cannot.
func countEvents(t *testing.T, ctx context.Context, db *DB, artifact string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM events WHERE artifact = $1`, artifact).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// A queued write becomes the artifact, the event that records it, and an intent
// marked applied - and it becomes all three or none, because they are one
// transaction.
func TestAQueuedWriteBecomesAnIndexedItem(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-writer")

	in := fsIntent(owner.ID, nil, "decisions.md", "we chose the queue")
	in.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if applied, err := db.FSIntentApplied(ctx, in.ID); err != nil || applied {
		t.Fatalf("a fresh intent reports applied=%v, %v; the whole point is that it is not yet", applied, err)
	}

	result, err := db.ApplyFSIntent(ctx, in, fsFields("we chose the queue", "a snorkbeetle body"))
	if err != nil || result != FSApplied {
		t.Fatalf("apply gave %q, %v", result, err)
	}

	art, err := db.GetArtifact(ctx, in.Artifact)
	if err != nil {
		t.Fatalf("the artifact the intent named is not there: %v", err)
	}
	if art.Title != "we chose the queue" || art.Body != "a snorkbeetle body" {
		t.Errorf("the row holds %q / %q", art.Title, art.Body)
	}
	if art.Visibility != VisibilityPersonal || art.Project != nil {
		t.Errorf("the row is %s in project %v, want the personal floor", art.Visibility, art.Project)
	}
	if art.FilePath != "decisions.md" {
		t.Errorf("the row's file_path is %q, want the name the file had", art.FilePath)
	}
	if len(art.Sig) == 0 {
		t.Error("the row went in unsigned; a queued write is a write like any other")
	}
	if n := countEvents(t, ctx, db, in.Artifact); n != 1 {
		t.Errorf("%d events name the artifact, want exactly the one that records the write", n)
	}
	if applied, err := db.FSIntentApplied(ctx, in.ID); err != nil || !applied {
		t.Errorf("the intent reports applied=%v, %v after being applied", applied, err)
	}

	// Indexed: the search column is written by the same statement, so a word
	// that is only in the body finds it.
	hits, err := db.SearchArtifacts(ctx, &Principal{UserID: owner.ID}, ArtifactQuery{Query: "snorkbeetle"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.ID == in.Artifact {
			found = true
		}
	}
	if !found {
		t.Error("a file written through the queue is not searchable")
	}
}

// The replay after a crash. At-least-once delivery means the same intent can be
// applied twice; the store has to make the second one nothing at all.
func TestApplyingTheSameIntentTwiceWritesOnce(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-replay")

	in := fsIntent(owner.ID, nil, "replay.md", "once")
	in.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, in, fsFields("once", "body")); err != nil || result != FSApplied {
		t.Fatalf("first apply gave %q, %v", result, err)
	}

	result, err := db.ApplyFSIntent(ctx, in, fsFields("once", "body"))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if result != FSDuplicate {
		t.Errorf("the second apply says %q, want duplicate", result)
	}
	if n := countEvents(t, ctx, db, in.Artifact); n != 1 {
		t.Errorf("%d events after applying one intent twice, want 1", n)
	}
}

// Two intents, the same bytes: the second is the file being saved again with
// nothing changed, and it must not be a second write of the same thing.
func TestTheSameBytesTwiceAreOneWrite(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-dedup")

	first := fsIntent(owner.ID, nil, "same.md", "unchanged")
	first.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, first); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := db.ApplyFSIntent(ctx, first, fsFields("same", "body")); err != nil {
		t.Fatalf("apply: %v", err)
	}

	second := fsIntent(owner.ID, nil, "same.md", "unchanged")
	second.Artifact, second.Hash, second.Visibility = first.Artifact, first.Hash, VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, second); err != nil {
		t.Fatalf("enqueue again: %v", err)
	}
	result, err := db.ApplyFSIntent(ctx, second, fsFields("same", "body"))
	if err != nil {
		t.Fatalf("apply again: %v", err)
	}
	if result != FSDuplicate {
		t.Errorf("the same bytes again say %q, want duplicate", result)
	}
	if n := countEvents(t, ctx, db, first.Artifact); n != 1 {
		t.Errorf("%d events after writing the same bytes twice, want 1", n)
	}

	// Different bytes are a different write, and do land.
	third := fsIntent(owner.ID, nil, "same.md", "changed")
	third.Artifact, third.Visibility = first.Artifact, VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, third); err != nil {
		t.Fatalf("enqueue the edit: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, third, fsFields("same", "edited")); err != nil || result != FSApplied {
		t.Fatalf("the edit gave %q, %v", result, err)
	}
	if n := countEvents(t, ctx, db, first.Artifact); n != 2 {
		t.Errorf("%d events after an edit, want 2", n)
	}
}

// The floor, in the transaction that would do the write. The mount refuses this
// at the door; this is the check that runs against the row as it is now, so a
// queued write cannot promote a personal item by being applied later.
func TestAQueuedWriteCannotMoveAnItemBetweenHomes(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-floor")
	project := "pa"
	declare(t, ctx, db, project)

	personal := fsIntent(owner.ID, nil, "floor.md", "mine")
	personal.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, personal); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := db.ApplyFSIntent(ctx, personal, fsFields("mine", "body")); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The same row, written again from a project directory.
	promote := fsIntent(owner.ID, &project, "floor.md", "promoted")
	promote.Artifact, promote.Visibility = personal.Artifact, VisibilityShared
	if err := db.EnqueueFSIntent(ctx, promote); err != nil {
		t.Fatalf("enqueue the promotion: %v", err)
	}
	result, err := db.ApplyFSIntent(ctx, promote, fsFields("promoted", "body"))
	if err != nil {
		t.Fatalf("apply the promotion: %v", err)
	}
	if result != FSRefused {
		t.Errorf("promoting a personal item said %q, want refused", result)
	}

	art, err := db.GetArtifact(ctx, personal.Artifact)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if art.Project != nil || art.Visibility != VisibilityPersonal {
		t.Fatalf("the item is now %s in project %v; the floor is not a scope a save can leave",
			art.Visibility, art.Project)
	}
	if n := countEvents(t, ctx, db, personal.Artifact); n != 1 {
		t.Errorf("%d events, want only the one write that was allowed", n)
	}

	// And the other way: a row that lives in a project is not taken out of it
	// by a file saved under the personal directory.
	inProject := fsIntent(owner.ID, &project, "team.md", "ours")
	inProject.Visibility = VisibilityProjectOnly
	if err := db.EnqueueFSIntent(ctx, inProject); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := db.ApplyFSIntent(ctx, inProject, fsFields("ours", "body")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	demote := fsIntent(owner.ID, nil, "team.md", "mine now")
	demote.Artifact, demote.Visibility = inProject.Artifact, VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, demote); err != nil {
		t.Fatalf("enqueue the demotion: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, demote, fsFields("mine now", "body")); err != nil || result != FSRefused {
		t.Errorf("taking a project row out of its project said %q, %v; want refused", result, err)
	}
}

// A row somebody else owns is not written by a queued intent, whatever the
// intent says about who owns it.
func TestAQueuedWriteDoesNotTakeSomebodyElsesRow(t *testing.T) {
	ctx, db := open(t)
	alice := fsUser(t, ctx, db, "fs-alice")
	bob := fsUser(t, ctx, db, "fs-bob")

	hers := fsIntent(alice.ID, nil, "hers.md", "alice")
	hers.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, hers); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := db.ApplyFSIntent(ctx, hers, fsFields("hers", "body")); err != nil {
		t.Fatalf("apply: %v", err)
	}

	takeover := fsIntent(bob.ID, nil, "hers.md", "bob")
	takeover.Artifact, takeover.Visibility = hers.Artifact, VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, takeover); err != nil {
		t.Fatalf("enqueue the takeover: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, takeover, fsFields("bob", "body")); err != nil || result != FSRefused {
		t.Errorf("the takeover said %q, %v; want refused", result, err)
	}
	art, err := db.GetArtifact(ctx, hers.Artifact)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if art.OwnerUser != alice.ID || art.Title != "hers" {
		t.Errorf("the row is now %s's, titled %q", art.OwnerUser, art.Title)
	}
}

// A file closed just before the item was deleted is a write that arrives after
// the delete. Coming back is something to do on purpose.
func TestAQueuedWriteDoesNotResurrectADeletedItem(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-deleted")
	p := &Principal{UserID: owner.ID}

	in := fsIntent(owner.ID, nil, "gone.md", "first")
	in.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := db.ApplyFSIntent(ctx, in, fsFields("gone", "body")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := db.TombstoneArtifact(ctx, p, in.Artifact); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	late := fsIntent(owner.ID, nil, "gone.md", "second")
	late.Artifact, late.Visibility = in.Artifact, VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, late); err != nil {
		t.Fatalf("enqueue the late write: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, late, fsFields("back", "body")); err != nil || result != FSSuperseded {
		t.Errorf("the late write said %q, %v; want superseded", result, err)
	}
	if _, err := db.ReadArtifact(ctx, p, in.Artifact, false); err == nil {
		t.Error("the deleted item is readable again")
	}
}

// The pending queue is what a restart replays, and it is in the order the files
// were closed: two writes of one file applied the other way round would leave
// the older bytes in the store.
func TestThePendingQueueIsOldestFirst(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-order")

	first := fsIntent(owner.ID, nil, "order.md", "one")
	second := fsIntent(owner.ID, nil, "order.md", "two")
	second.Artifact = first.Artifact
	for _, in := range []*FSIntent{first, second} {
		in.Visibility = VisibilityPersonal
		if err := db.EnqueueFSIntent(ctx, in); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	pending, err := db.PendingFSIntents(ctx, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	seen := map[string]int{}
	for i, in := range pending {
		seen[in.ID] = i
	}
	if seen[first.ID] > seen[second.ID] {
		t.Errorf("the queue came back newest first: %v", seen)
	}
	// And the content is the content, so a replay writes what was written
	// rather than a parse of it recorded alongside.
	for _, in := range pending {
		if in.ID == second.ID && in.Content != "two" {
			t.Errorf("the queued content is %q", in.Content)
		}
	}
}

// The mount's directories are permission-filtered reads and nothing else: the
// same filter the API and mem_search are narrowed by, asked about a scope.
func TestTheMountsDirectoriesAreThePermissionFilter(t *testing.T) {
	ctx, db := open(t)
	alice := fsUser(t, ctx, db, "fs-dir-alice")
	bob := fsUser(t, ctx, db, "fs-dir-bob")
	project := declaredProject(t, ctx, db, "fsp")

	aliceP := &Principal{UserID: alice.ID, Project: project}
	bobP := &Principal{UserID: bob.ID, Project: declaredProject(t, ctx, db, "fsother")}

	personal := &Artifact{
		Type: "memory", OwnerUser: alice.ID, Title: "mine alone",
		Visibility: VisibilityPersonal,
	}
	if err := db.UpsertArtifact(ctx, personal); err != nil {
		t.Fatalf("write the personal item: %v", err)
	}
	shared := &Artifact{
		Type: "memory", OwnerUser: alice.ID, Title: "ours", Project: &project,
		Visibility: VisibilityShared,
	}
	if err := db.UpsertArtifact(ctx, shared); err != nil {
		t.Fatalf("write the project item: %v", err)
	}

	floor := FSScope{Project: nil, Owner: alice.ID, Type: "memory"}
	inProject := FSScope{Project: &project, Owner: alice.ID, Type: "memory"}

	mine, err := db.FSList(ctx, aliceP, floor)
	if err != nil {
		t.Fatalf("list the floor: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != personal.ID {
		t.Fatalf("the owner's personal directory holds %d item(s)", len(mine))
	}

	// Bob has no grant and is not in the project. Both directories are empty
	// for him, and the personal one is empty for him however many grants exist:
	// FSList puts the filter in the WHERE clause rather than reading the rows
	// and then hiding them.
	for _, scope := range []FSScope{floor, inProject} {
		theirs, err := db.FSList(ctx, bobP, scope)
		if err != nil {
			t.Fatalf("list as bob: %v", err)
		}
		if len(theirs) != 0 {
			t.Errorf("bob sees %d item(s) in %v", len(theirs), scope.Project)
		}
	}
	if _, err := db.FSFind(ctx, bobP, floor, personal.ID); err == nil {
		t.Error("bob read alice's personal item by naming its id")
	}

	// And a row is only in the directory its own scope names: the personal item
	// is not in the project directory, even for its owner.
	if _, err := db.FSFind(ctx, aliceP, inProject, personal.ID); err == nil {
		t.Error("a personal item answered a lookup in a project directory")
	}
	if _, err := db.FSFind(ctx, aliceP, floor, shared.ID); err == nil {
		t.Error("a project item answered a lookup in the personal directory")
	}

	projects, err := db.FSProjects(ctx, aliceP)
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p == project {
			found = true
		}
	}
	if !found {
		t.Errorf("the owner's own project is not in %v", projects)
	}
	// The floor is not a project and never appears as one: a row with no
	// project has nothing to put in that column.
	for _, p := range projects {
		if p == "" {
			t.Error("the empty project is listed as a directory")
		}
	}

	owners, err := db.FSOwners(ctx, aliceP, &project)
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	if len(owners) != 1 || owners[0] != alice.ID {
		t.Errorf("the project's owners are %v", owners)
	}
	if theirs, err := db.FSOwners(ctx, bobP, &project); err != nil || len(theirs) != 0 {
		t.Errorf("bob sees the owners %v, %v", theirs, err)
	}
}

// Only the types the mount hosts are hosted. A bug is not a file here: an
// editor's save would be a way to move something through a lifecycle it has no
// business moving through.
func TestTheMountHostsMemoryAndNotesAndNothingElse(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-types")
	p := &Principal{UserID: owner.ID}

	bug := &Artifact{Type: "bug", OwnerUser: owner.ID, Title: "not a file", Visibility: VisibilityPersonal}
	if err := db.UpsertArtifact(ctx, bug); err != nil {
		t.Fatalf("write the bug: %v", err)
	}
	for _, hosted := range FSTypes {
		list, err := db.FSList(ctx, p, FSScope{Owner: owner.ID, Type: hosted})
		if err != nil {
			t.Fatalf("list %s: %v", hosted, err)
		}
		for _, art := range list {
			if art.ID == bug.ID {
				t.Fatalf("the bug turned up in the %s directory", hosted)
			}
		}
	}
	if FSTypeOK("bug") || !FSTypeOK("memory") || !FSTypeOK("note") {
		t.Error("FSTypeOK does not agree with FSTypes")
	}
}

// A file created and deleted before the drainer ever ran names no row, so there
// is no tombstone for the apply to refuse against. The queue is where that
// delete has to land.
func TestCancellingAQueuedWriteLeavesNothingToApply(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-cancel")
	other := fsUser(t, ctx, db, "fs-cancel-other")

	in := fsIntent(owner.ID, nil, "doomed.md", "never stored")
	in.Visibility = VisibilityPersonal
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// One principal does not cancel another's write.
	if n, err := db.CancelFSIntents(ctx, in.Artifact, other.ID); err != nil || n != 0 {
		t.Fatalf("somebody else's cancel took %d intent(s), %v", n, err)
	}
	n, err := db.CancelFSIntents(ctx, in.Artifact, owner.ID)
	if err != nil || n != 1 {
		t.Fatalf("the owner's cancel took %d intent(s), %v", n, err)
	}

	pending, err := db.PendingFSIntents(ctx, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	for _, left := range pending {
		if left.ID == in.ID {
			t.Fatal("the cancelled intent is still on the queue")
		}
	}
	if _, err := db.GetArtifact(ctx, in.Artifact); err == nil {
		t.Fatal("the cancelled write left an artifact behind")
	}
	// And cancelling again is nothing, rather than an error.
	if n, err := db.CancelFSIntents(ctx, in.Artifact, owner.ID); err != nil || n != 0 {
		t.Fatalf("a second cancel took %d intent(s), %v", n, err)
	}
}

// The husk arm: a queued rewrite of a spec keeps the row's kind. A save
// without the front-matter header parses as a note - kindFor defaults it -
// and the mount's read-only refusal stood in for this until the arm: the row
// says what it is, and the header cannot argue. (Operator's arm on thread
// 01M0K9WFBNBZ9V9XBK5NGD7D9K, message 01M0KENVHE554V04WN16B8M4RH.)
func TestAnOpenspecRowKeepsItsKindAcrossAQueuedRewrite(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-spec")

	spec := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: SpecKind,
		OwnerUser: owner.ID, Title: "the-capability",
		Body: "# the-capability\n\nthe words\n",
	}
	if err := db.UpsertArtifact(ctx, spec); err != nil {
		t.Fatalf("file the spec: %v", err)
	}

	in := fsIntent(owner.ID, nil, "the-capability.md", "# the-capability\n\nnew words\n")
	in.Artifact = spec.ID
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// The drainer parses the headerless content and defaults the kind to
	// note - exactly the write that would husk the row.
	result, err := db.ApplyFSIntent(ctx, in,
		fsFields("the-capability", "# the-capability\n\nnew words\n"))
	if err != nil || result != FSApplied {
		t.Fatalf("apply gave %q, %v", result, err)
	}

	art, err := db.GetArtifact(ctx, spec.ID)
	if err != nil {
		t.Fatalf("the spec is not there: %v", err)
	}
	if art.Kind != SpecKind {
		t.Fatalf("the row is kind %q after a headerless rewrite; the row decides, the header cannot husk it", art.Kind)
	}
	if art.Body != "# the-capability\n\nnew words\n" {
		t.Fatalf("the body is %q, want the saved words", art.Body)
	}
}

// The wedge arm: an apply the store refuses is dropped once, with the
// refusal's own sentence recorded on the queue row, and the intents behind
// it keep draining. Retrying it forever is the wedge the mount's read-only
// refusals stood in for until this arm.
func TestARefusedApplyIsDroppedOnceAndTheQueueKeepsDraining(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-refused")
	project := declaredProject(t, ctx, db, "fs-refused")
	p := &Principal{UserID: owner.ID, Project: project}

	change := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "the change\n",
	})
	// Strip the change's files so the shape check refuses the next write:
	// the write is the caller's mistake, and it stays the caller's mistake
	// on every retry.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE artifacts SET fields = '{}'::jsonb WHERE id = $1`, change.ID); err != nil {
		t.Fatalf("strip the change: %v", err)
	}

	refused := fsIntent(owner.ID, &project, "the-change.md", "rewritten without its files\n")
	refused.Artifact = change.ID
	if err := db.EnqueueFSIntent(ctx, refused); err != nil {
		t.Fatalf("enqueue the refused write: %v", err)
	}

	after := fsIntent(owner.ID, &project, "after.md", "a later write\n")
	if err := db.EnqueueFSIntent(ctx, after); err != nil {
		t.Fatalf("enqueue the later write: %v", err)
	}

	result, err := db.ApplyFSIntent(ctx, refused, fsFields("the change", "rewritten without its files\n"))
	if err != nil || result != FSRefused {
		t.Fatalf("the refused apply gave %q, %v", result, err)
	}
	if applied, err := db.FSIntentApplied(ctx, refused.ID); err != nil || !applied {
		t.Fatalf("the refused intent reports applied=%v, %v; it must be dropped, not retried", applied, err)
	}
	var reason string
	if err := db.sql.QueryRowContext(ctx,
		`SELECT coalesce(refusal, '') FROM fs_intents WHERE id = $1`, refused.ID).Scan(&reason); err != nil {
		t.Fatalf("read the refusal: %v", err)
	}
	if !strings.Contains(reason, "proposal.md") {
		t.Fatalf("the queue row records the store's own sentence, got %q", reason)
	}
	if _, err := db.GetArtifact(ctx, change.ID); err != nil {
		t.Fatalf("the row was not written: %v", err)
	}

	// The queue keeps draining: the intent behind the refusal applies.
	result, err = db.ApplyFSIntent(ctx, after, fsFields("after", "a later write\n"))
	if err != nil || result != FSApplied {
		t.Fatalf("the write behind the refusal gave %q, %v", result, err)
	}
	if _, err := db.GetArtifact(ctx, after.Artifact); err != nil {
		t.Fatalf("the later write did not land: %v", err)
	}
}
