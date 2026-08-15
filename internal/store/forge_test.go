package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestExternalRefRoundTrips is the store half of the forge bridge: a link
// written onto an artifact comes back as it went in, an ordinary update of the
// artifact does not disturb it, and it replicates - a peer that merges the row
// gets the issue and both cursors with it.
func TestExternalRefRoundTrips(t *testing.T) {
	ctx, db := open(t)

	project := "pf-" + ulid.NewString()
	owner := &User{Handle: "filer-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	art := &Artifact{Type: "bug", Project: &project, OwnerUser: owner.ID, Title: "the gearbox whines"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if art.Reported || art.External != nil {
		t.Fatal("a fresh artifact has not been filed anywhere")
	}

	filed := time.Now().UTC().Truncate(time.Millisecond)
	ref := &ExternalRef{
		Forge: "mock", Repo: "o/r", Number: 7,
		URL: "https://mock.forge/o/r/issues/7", State: "open",
		Thread: ulid.NewString(), Author: "flowy",
		Since: filed, Pushed: 42, Filed: filed,
	}
	if err := db.SetArtifactExternal(ctx, art, ref, true); err != nil {
		t.Fatalf("set external: %v", err)
	}

	read, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if !read.Reported {
		t.Error("the artifact should be reported")
	}
	if read.External == nil {
		t.Fatal("the artifact came back with no external ref")
	}
	if read.External.Repo != "o/r" || read.External.Number != 7 || read.External.Pushed != 42 {
		t.Errorf("external ref came back as %+v", read.External)
	}
	if !read.External.Since.Equal(filed) {
		t.Errorf("comment cursor is %s, want %s", read.External.Since, filed)
	}

	// An ordinary update of the artifact must not unfile it: the two columns
	// are written by SetArtifactExternal alone.
	art.Title = "the gearbox whines under load"
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("update artifact: %v", err)
	}
	read, err = db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if !read.Reported || read.External == nil || read.External.Number != 7 {
		t.Errorf("an edit lost the forge link: reported=%v external=%+v", read.Reported, read.External)
	}

	// And it travels: the same row, from a peer, with a later reading.
	incoming := *read
	incoming.Node = "peer"
	incoming.HLC = read.HLC + 1
	incoming.Title = "as the peer has it"
	applied, err := db.SyncApply(ctx, fromPeer(t, ctx, db, &SyncSet{Artifacts: []*Artifact{&incoming}}))
	if err != nil {
		t.Fatalf("sync apply: %v", err)
	}
	if applied["artifacts"] != 1 {
		t.Fatalf("applied %d artifacts, want 1", applied["artifacts"])
	}
	merged, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if merged.Title != "as the peer has it" {
		t.Errorf("the later write did not win: title is %q", merged.Title)
	}
	if !merged.Reported || merged.External == nil || merged.External.Repo != "o/r" ||
		merged.External.Thread != ref.Thread {
		t.Errorf("the forge link did not replicate: reported=%v external=%+v",
			merged.Reported, merged.External)
	}
}

// TestExternalRefCursors covers the two rules that make a sync idempotent: a
// comment that has been threaded in is never threaded in twice, and the cursor
// only moves forward.
func TestExternalRefCursors(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	ref := &ExternalRef{}

	if ref.AlreadySeen("c1", at) {
		t.Error("nothing has been seen yet")
	}
	ref.MarkSeen("c1", at)
	if !ref.AlreadySeen("c1", at) {
		t.Error("c1 has been seen")
	}
	// Same instant, different comment: the id is what saves it, because a forge
	// whose timestamps have one-second resolution hands back both.
	if ref.AlreadySeen("c2", at) {
		t.Error("c2 at the same instant is a new comment")
	}
	// Older than the cursor: accounted for by an earlier sync.
	if !ref.AlreadySeen("c0", at.Add(-time.Hour)) {
		t.Error("a comment older than the cursor has been dealt with")
	}
	ref.MarkSeen("c2", at.Add(-time.Hour))
	if !ref.Since.Equal(at) {
		t.Errorf("the cursor went backwards to %s", ref.Since)
	}

	// The seen list is trimmed towards the cap, but never at the cost of an
	// entry the cursor cannot rule out on its own: these are all at the cursor,
	// so they all stay however many there are.
	for i := 0; i < seenCap+50; i++ {
		ref.MarkSeen("x"+ulid.NewString(), at)
	}
	if len(ref.Seen) <= seenCap {
		t.Errorf("seen list holds %d entries; entries at the cursor must not be dropped", len(ref.Seen))
	}

	// Once the cursor moves past them, the same entries are forgettable: the
	// cursor covers them, and the list comes back to the cap.
	ref.MarkSeen("later", at.Add(time.Hour))
	if len(ref.Seen) != seenCap {
		t.Errorf("seen list holds %d entries, want it trimmed to %d", len(ref.Seen), seenCap)
	}
	if !ref.AlreadySeen("later", at.Add(time.Hour)) {
		t.Error("the newest comment must survive the trim")
	}
}

// TestExternalRefKeepsSameSecondCommentsSeen is LOW 10: a forge that stamps
// comments to the second hands back several at the cursor's exact time, and the
// cursor cannot tell them apart - only the seen list can. Trimming that list by
// count alone threw the oldest of them away, and the next sync threaded it in
// again as if the reviewer had said it twice.
func TestExternalRefKeepsSameSecondCommentsSeen(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	ref := &ExternalRef{}

	first := "c-first"
	ref.MarkSeen(first, at)
	// Enough same-second comments to push the first one out of a list that is
	// capped by count.
	for i := 0; i < seenCap+1; i++ {
		ref.MarkSeen("c"+ulid.NewString(), at)
	}
	if !ref.AlreadySeen(first, at) {
		t.Fatalf("%s was threaded in already; a second sync would post it again", first)
	}
}

// TestSeenCommentReadsTheOlderShape: refs replicate, so the bare-id list an
// older node wrote has to keep parsing here.
func TestSeenCommentReadsTheOlderShape(t *testing.T) {
	var ref ExternalRef
	if err := json.Unmarshal([]byte(`{"repo":"o/r","seen":["c1",{"id":"c2","at":"2026-02-03T04:05:06Z"}]}`),
		&ref); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ref.Seen) != 2 || ref.Seen[0].ID != "c1" || ref.Seen[1].ID != "c2" {
		t.Fatalf("seen came back as %+v", ref.Seen)
	}
	if !ref.Seen[0].At.IsZero() {
		t.Errorf("a bare id has no time, got %s", ref.Seen[0].At)
	}
	if ref.Seen[1].At.IsZero() {
		t.Error("the pair shape lost its time")
	}
}

// TestAffectedRowsReportsTheDriversError is LOW 9: SetAutoDelegate used to read
// RowsAffected and drop the error, which turns "the driver could not tell me
// whether that update found the row" into "it did".
func TestAffectedRowsReportsTheDriversError(t *testing.T) {
	n, err := affectedRows(countlessResult{})
	if err == nil {
		t.Fatalf("affectedRows returned %d and no error for a driver that cannot count", n)
	}
	if !errors.Is(err, errNoCount) {
		t.Errorf("the driver's own error was lost: %v", err)
	}
	if n, err := affectedRows(countedResult(3)); err != nil || n != 3 {
		t.Errorf("affectedRows(3) = %d, %v", n, err)
	}
}

var errNoCount = errors.New("this driver does not count rows")

// countlessResult is a driver that will not say how many rows it changed.
type countlessResult struct{}

func (countlessResult) LastInsertId() (int64, error) { return 0, errNoCount }
func (countlessResult) RowsAffected() (int64, error) { return 0, errNoCount }

// countedResult is one that will.
type countedResult int64

func (countedResult) LastInsertId() (int64, error)   { return 0, nil }
func (r countedResult) RowsAffected() (int64, error) { return int64(r), nil }

// TestLatestTaskForArtifact: the forge bridge asks for it so an issue's
// conversation lands in the thread the people working on it already have.
func TestLatestTaskForArtifact(t *testing.T) {
	ctx, db := open(t)

	project := "pt-" + ulid.NewString()
	from := &User{Handle: "from-" + ulid.NewString()}
	to := &User{Handle: "to-" + ulid.NewString()}
	for _, u := range []*User{from, to} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	art := &Artifact{Type: "bug", Project: &project, OwnerUser: from.ID, Title: "unassigned"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	if _, err := db.LatestTaskForArtifact(ctx, art.ID); err == nil {
		t.Fatal("an unassigned artifact has no task")
	}

	first := &Task{Artifact: art.ID, FromUser: from.ID, ToUser: to.ID, Project: project,
		State: TaskOpen, Thread: ulid.NewString()}
	if err := db.InsertTask(ctx, first); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	second := &Task{Artifact: art.ID, FromUser: to.ID, ToUser: from.ID, Project: project,
		State: TaskOpen, Thread: ulid.NewString()}
	if err := db.InsertTask(ctx, second); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	latest, err := db.LatestTaskForArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("latest task: %v", err)
	}
	if latest.Thread != second.Thread {
		t.Errorf("latest task is %s, want the newer one %s", latest.ID, second.ID)
	}
}
