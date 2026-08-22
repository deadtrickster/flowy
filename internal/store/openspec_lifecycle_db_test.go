package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The wire side of the openspec lifecycle: a move writes the row and its
// trail entry as one fact, the line refuses what is off it, complete's two
// arms each refuse in their own words, and the funnel carries the state
// through every generic write - doors, views, the drainer. Proven against
// the database rather than by calling the checks directly, because the point
// is that the statements the surfaces actually run ask them.

// openspecMove moves a change with the raw mover, the way the transition
// door does after its check has passed. It is what these tests use to SEED a
// state, and what the happy-path test exercises.
func openspecMove(t *testing.T, ctx context.Context, db *DB, art *Artifact,
	to OpenspecState,
) *Event {
	t.Helper()
	e := &Event{
		Type:     EventOpenspecTransition,
		Project:  art.Project,
		Artifact: art.ID,
		Body:     "x->" + to,
	}
	if err := db.MoveOpenspecState(ctx, art, to, e); err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	return e
}

// storedChange reads the row back the way a reader would, so the assertions
// are about what is stored and not what the test holds.
func storedChange(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) *Artifact {
	t.Helper()
	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	return art
}

// stateOf is OpenspecStateOf on the stored row.
func stateOf(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) OpenspecState {
	t.Helper()
	state, err := OpenspecStateOf(storedChange(t, ctx, db, p, id))
	if err != nil {
		t.Fatalf("state of %s: %v", id, err)
	}
	return state
}

// derivedRowsByLine maps a change's derived todos by their line identity, so
// a test can close the ROW a tasks.md marker names.
func derivedRowsByLine(t *testing.T, ctx context.Context, db *DB, change string) map[string]*Artifact {
	t.Helper()
	known, err := db.derivedTodosOf(ctx, db.sql, change)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	byLine := map[string]*Artifact{}
	for _, todo := range known {
		if line := originLineOf(todo); line != "" {
			byLine[line] = todo
		}
	}
	return byLine
}

func TestOpenspecMoveWritesRowAndEventInOneReading(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-move")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})
	e := openspecMove(t, ctx, db, art, OpenspecInProgress)

	stored := storedChange(t, ctx, db, p, art.ID)
	if state, err := OpenspecStateOf(stored); err != nil || state != OpenspecInProgress {
		t.Fatalf("the stored change reads %q, %v - want in-progress", state, err)
	}
	// One clock reading for the pair: the row and the entry that records its
	// move are one fact, and a gap between them would be a state the trail
	// cannot vouch for.
	if stored.HLC != e.SeqHLC {
		t.Fatalf("the row reads %d and its entry %d - a state and its trail must be one reading",
			stored.HLC, e.SeqHLC)
	}
	events, err := db.ArtifactEvents(ctx, art.ID, EventOpenspecTransition)
	if err != nil {
		t.Fatalf("trail: %v", err)
	}
	if len(events) != 1 || events[0].ID != e.ID {
		t.Fatalf("the trail holds %d entries, want the one that records the move", len(events))
	}
}

func TestOpenspecCheckRefusesOffLineMoves(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-line")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})

	// A skip and a word off the line: both refused with the line's own
	// sentence, which says where from proposed the one move leads.
	for _, to := range []OpenspecState{OpenspecComplete, "banana"} {
		err := db.CheckOpenspecTransition(ctx, art, to)
		if err == nil {
			t.Fatalf("proposed -> %s was allowed - the line has no skips", to)
		}
		if !strings.Contains(err.Error(), "from proposed the lifecycle allows in-progress") {
			t.Fatalf("proposed -> %s refused with %q, not the line's sentence", to, err)
		}
	}

	// A seeded archived row is terminal: nothing moves out of it, and the
	// refusal says so in its own words rather than by a map lookup failing
	// quietly.
	seeded := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})
	openspecMove(t, ctx, db, seeded, OpenspecInProgress)
	openspecMove(t, ctx, db, seeded, OpenspecComplete)
	openspecMove(t, ctx, db, seeded, OpenspecArchived)
	err := db.CheckOpenspecTransition(ctx, seeded, OpenspecComplete)
	if err == nil {
		t.Fatal("archived -> complete was allowed - an archived change left the line")
	}
	if !strings.Contains(err.Error(), "archived is terminal") {
		t.Fatalf("the refusal is %q, not the terminal sentence", err)
	}
}

// The todo arm: complete is refused while any task derived off the change's
// tasks.md is not done, naming the one that is not.
func TestOpenspecCompleteRefusesWhileTasksAreOpen(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-tasks")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] one\n- [ ] two\n",
	})
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	// The stored tasks.md carries the line markers the derivation mints -
	// those ids identify the derived todo rows by their origin, and closing
	// a task means closing the ROW.
	stored := storedChange(t, ctx, db, p, art.ID)
	files, err := OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	var ids []string
	for _, line := range parseTasks(files["tasks.md"]) {
		if line.id != "" {
			ids = append(ids, line.id)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("the stored tasks.md derives %d todos, want two: %v", len(ids), ids)
	}
	byLine := derivedRowsByLine(t, ctx, db, art.ID)

	// One done, one open: the arm names the open one.
	if _, _, err := db.SetTodoStatus(ctx, p, byLine[ids[0]].ID, DoneStatus, "done"); err != nil {
		t.Fatalf("close %s: %v", ids[0], err)
	}
	err = db.CheckOpenspecTransition(ctx, art, OpenspecComplete)
	if err == nil {
		t.Fatal("complete was allowed with a task open")
	}
	if !strings.Contains(err.Error(), "its tasks are not all done") ||
		!strings.Contains(err.Error(), ids[1]) {
		t.Fatalf("the refusal is %q - it must say which task is open", err)
	}
}

// The validate arm: with every task done, complete is still refused while no
// verdict is cached on the row, and the refusal names the door that fixes it
// - the p4 cache is what the arm reads, absent included.
func TestOpenspecCompleteRefusesWhileUnvalidated(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-validate")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] one\n",
	})
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	stored := storedChange(t, ctx, db, p, art.ID)
	files, err := OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	byLine := derivedRowsByLine(t, ctx, db, art.ID)
	for _, line := range parseTasks(files["tasks.md"]) {
		if line.id == "" {
			t.Fatal("the stored tasks.md carries an unmarked line")
		}
		if _, _, err := db.SetTodoStatus(ctx, p, byLine[line.id].ID, DoneStatus, "done"); err != nil {
			t.Fatalf("close %s: %v", line.id, err)
		}
	}

	err = db.CheckOpenspecTransition(ctx, art, OpenspecComplete)
	if err == nil {
		t.Fatal("complete was allowed while no verdict is cached")
	}
	if !strings.Contains(err.Error(), "has not been validated - run POST /api/openspec/{id}/validate") {
		t.Fatalf("the refusal is %q, not the validate arm's own sentence", err)
	}
}

// The far end of the line, seeded: complete -> archived is the one move the
// line allows out of complete, and the entries chain - each move's entry is
// the child of the one before, so a change's trail reads as a thread.
func TestOpenspecArchivedReachableFromSeededCompleteAndEventsChain(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-chain")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})
	first := openspecMove(t, ctx, db, art, OpenspecInProgress)

	// Each later entry is built the way the door builds one: the previous
	// entry is its parent, in the thread that entry opened.
	last, err := db.LatestArtifactEvent(ctx, art.ID, EventOpenspecTransition)
	if err != nil {
		t.Fatalf("latest entry: %v", err)
	}
	second := &Event{
		Type:     EventOpenspecTransition,
		Project:  art.Project,
		Parents:  []string{last.ID},
		Thread:   last.Thread,
		Artifact: art.ID,
		Body:     "in-progress->complete",
	}
	if err := db.MoveOpenspecState(ctx, art, OpenspecComplete, second); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if err := db.CheckOpenspecTransition(ctx, art, OpenspecArchived); err != nil {
		t.Fatalf("complete -> archived was refused: %v", err)
	}
	last, err = db.LatestArtifactEvent(ctx, art.ID, EventOpenspecTransition)
	if err != nil {
		t.Fatalf("latest entry: %v", err)
	}
	third := &Event{
		Type:     EventOpenspecTransition,
		Project:  art.Project,
		Parents:  []string{last.ID},
		Thread:   last.Thread,
		Artifact: art.ID,
		Body:     "complete->archived",
	}
	if err := db.MoveOpenspecState(ctx, art, OpenspecArchived, third); err != nil {
		t.Fatalf("archive: %v", err)
	}

	if state := stateOf(t, ctx, db, p, art.ID); state != OpenspecArchived {
		t.Fatalf("the archived change reads %q", state)
	}
	// The chain: each entry is the child of the one before, all in one thread.
	if len(second.Parents) != 1 || second.Parents[0] != first.ID {
		t.Fatalf("the second entry's parent is %v, want [%s]", second.Parents, first.ID)
	}
	if len(third.Parents) != 1 || third.Parents[0] != second.ID {
		t.Fatalf("the third entry's parent is %v, want [%s]", third.Parents, second.ID)
	}
	if third.Thread != second.Thread {
		t.Fatalf("the trail split into threads %q and %q", third.Thread, second.Thread)
	}
}

// THE FUNNEL ARM: whatever state a caller writes into the fields blob, the
// stored row keeps the state the lifecycle holds. A rewrite of an existing
// change keeps the held state; a fresh create is born proposed, because the
// only thing that moves the state is the transition door.
func TestOpenspecStateCarriedThroughTheFunnel(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-life-carry")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	// A rewrite of the row carrying a forged state: the content applies, the
	// forged state does not.
	forged, err := json.Marshal(map[string]any{
		"openspec": map[string]any{
			"state": OpenspecArchived,
			"files": map[string]string{"proposal.md": "rewritten\n"},
		},
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	rewrite := *art
	rewrite.Fields = forged
	if err := db.UpsertArtifact(ctx, &rewrite); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	stored := storedChange(t, ctx, db, p, art.ID)
	if state, err := OpenspecStateOf(stored); err != nil || state != OpenspecInProgress {
		t.Fatalf("the rewrite moved the state to %q, %v - the caller's blob is not the lifecycle",
			state, err)
	}
	if files, err := OpenspecFilesOf(stored); err != nil || files["proposal.md"] != "rewritten\n" {
		t.Fatalf("the rewrite's content did not apply: %v, %v", files, err)
	}

	// A fresh change born with a forged state: there is no held row, so there
	// is no held state - the create reads as proposed.
	fresh := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: ChangeKind,
		Project: &project, OwnerUser: p.UserID, Title: "born archived",
	}
	if err := setOpenspecFiles(fresh, map[string]string{"proposal.md": "# why\n"}); err != nil {
		t.Fatalf("seed files: %v", err)
	}
	if err := SetOpenspecState(fresh, OpenspecArchived); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := db.CreateArtifact(ctx, fresh); err != nil {
		t.Fatalf("create: %v", err)
	}
	if state := stateOf(t, ctx, db, p, fresh.ID); state != OpenspecProposed {
		t.Fatalf("a fresh change reads %q - the line starts at proposed, whatever it was born with",
			state)
	}
}

// THE MOUNT ARM: a view write of one tasks.md key keeps the row's fields -
// the lifecycle state among them - and does not mint a transition. The
// seeding in the view branch is what makes the save write the row's fields
// rather than rebuild them; the funnel carry is the invariant's own arm.
func TestOpenspecViewWriteKeepsTheRowFields(t *testing.T) {
	ctx, db := open(t)
	owner := fsUser(t, ctx, db, "fs-life-view")
	project := declaredProject(t, ctx, db, "fs-life-view")
	p := &Principal{UserID: owner.ID, Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] one\n",
	})
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	// A custom key beyond the openspec map, so the test can tell "the fields
	// were kept" apart from "the state was carried".
	custom, err := json.Marshal(map[string]any{
		"openspec": map[string]any{
			"state": OpenspecInProgress,
			"files": map[string]string{"proposal.md": "# why\n", "tasks.md": "- [ ] one\n"},
		},
		"custom": "kept",
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if err := db.SetArtifactFields(ctx, art, custom); err != nil {
		t.Fatalf("set custom key: %v", err)
	}

	in := fsIntent(owner.ID, &project, "the-change.md", "- [x] one\n")
	in.Artifact = art.ID
	in.FileKey = "tasks.md"
	if err := db.EnqueueFSIntent(ctx, in); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if result, err := db.ApplyFSIntent(ctx, in, FSFields{}); err != nil || result != FSApplied {
		t.Fatalf("apply gave %q, %v", result, err)
	}

	stored := storedChange(t, ctx, db, p, art.ID)
	fields, err := ArtifactFields(stored)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if state, err := OpenspecStateOf(stored); err != nil || state != OpenspecInProgress {
		t.Fatalf("a view save moved the state to %q, %v - the mount edits files, not the line",
			state, err)
	}
	if fields["custom"] != "kept" {
		t.Fatalf("a view save dropped the row's own fields: %v", fields)
	}
	if files, err := OpenspecFilesOf(stored); err != nil ||
		!strings.Contains(files["tasks.md"], "- [x] one") {
		t.Fatalf("the view save did not apply its file: %v, %v", files, err)
	}
	events, err := db.ArtifactEvents(ctx, art.ID, EventOpenspecTransition)
	if err != nil {
		t.Fatalf("trail: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d transition entries after a view save, want only the real move", len(events))
	}
}
