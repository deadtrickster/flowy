package store

// The pure half of the derivation engine, DB-free: parsing, annotation and
// the plan. The wire half - that the three write statements actually derive -
// is openspec_derive_db_test.go, against the database.

import (
	"encoding/json"
	"strings"
	"testing"
)

// derivedTodoRow is the row a derivation would have written: an ordinary todo
// carrying its origin in fields, the way deriveCreate writes it.
func derivedTodoRow(t *testing.T, line string, num int, title, status string, reopened bool) *Artifact {
	t.Helper()
	fields, err := json.Marshal(map[string]any{
		originField: map[string]any{
			originOpenspec: map[string]any{
				originChange:   "01CHANGE",
				originLine:     line,
				originNum:      num,
				originReopened: reopened,
			},
		},
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	return &Artifact{ID: "01TODO" + line, Kind: derivedTodoKind, Title: title, Status: status, Fields: fields}
}

func TestParseTasks(t *testing.T) {
	md := "## The work\n\n- [ ] first <!-- flowy:01AA -->\n- [x] second\n- [X] third\n\n" +
		"some prose\n- not a checkbox\n  - [ ] indented\n"
	lines := parseTasks(md)
	if len(lines) != 3 {
		t.Fatalf("3 checkbox lines, parsed %d", len(lines))
	}
	if lines[0].num != 1 || lines[1].num != 2 || lines[2].num != 3 {
		t.Fatalf("positions are %d %d %d, want 1 2 3", lines[0].num, lines[1].num, lines[2].num)
	}
	if lines[0].text != "first" || lines[0].id != "01AA" || lines[0].done {
		t.Fatalf("line 1 parsed as %+v", lines[0])
	}
	if !lines[1].done || lines[1].id != "" {
		t.Fatalf("line 2 parsed as %+v", lines[1])
	}
	if !lines[2].done {
		t.Fatalf("an uppercase X is done: %+v", lines[2])
	}
	if lines[0].hash == "" || lines[0].hash == lines[1].hash {
		t.Fatalf("hash is identity of the text: %+v", lines)
	}
}

func TestParseTasksHashIgnoresCheckboxAndMarker(t *testing.T) {
	a := parseTasks("- [ ] same <!-- flowy:01AA -->")
	b := parseTasks("- [x] same")
	if len(a) != 1 || len(b) != 1 || a[0].hash != b[0].hash {
		t.Fatalf("the text is the identity, not the box or the marker: %q vs %q", a[0].hash, b[0].hash)
	}
}

func TestAnnotateTasksMintsIds(t *testing.T) {
	md := "## Work\n\n- [ ] first\n- [x] second\n\nend\n"
	annotated, lines := annotateTasks(md, nil)
	if len(lines) != 2 || lines[0].id == "" || lines[1].id == "" || lines[0].id == lines[1].id {
		t.Fatalf("two lines, two distinct ids: %+v", lines)
	}
	want := "## Work\n\n- [ ] first <!-- flowy:" + lines[0].id + " -->\n" +
		"- [x] second <!-- flowy:" + lines[1].id + " -->\n\nend\n"
	if annotated != want {
		t.Fatalf("only the markers were added:\n got %q\nwant %q", annotated, want)
	}
}

func TestAnnotateTasksKeepsMarked(t *testing.T) {
	md := "- [ ] first <!-- flowy:01AA -->\n- [x] second <!-- flowy:01BB -->\n"
	annotated, lines := annotateTasks(md, nil)
	if annotated != md {
		t.Fatalf("a fully marked file comes back byte for byte: %q", annotated)
	}
	if lines[0].id != "01AA" || lines[1].id != "01BB" {
		t.Fatalf("ids kept: %+v", lines)
	}
}

func TestAnnotateTasksBootstrapsByPosition(t *testing.T) {
	// Two identical unmarked lines; the old row held position 2. The line
	// standing where the old line stood keeps the old id.
	md := "- [ ] first\n- [ ] first\n"
	known := []lineIdentity{{id: "01OLD", num: 2, hash: taskTextHash("first")}}
	annotated, lines := annotateTasks(md, known)
	if lines[1].id != "01OLD" {
		t.Fatalf("position 2 keeps its id, got %q", lines[1].id)
	}
	if lines[0].id == "" || lines[0].id == lines[1].id {
		t.Fatalf("position 1 got a fresh id: %+v", lines)
	}
	if !strings.Contains(annotated, "- [ ] first <!-- flowy:01OLD -->") {
		t.Fatalf("the marker landed on the second line: %q", annotated)
	}
}

func TestPlanDerivationCreates(t *testing.T) {
	lines := parseTasks("- [ ] first\n- [x] second\n")
	plan := planDerivation(lines, nil)
	if len(plan.create) != 2 || len(plan.update) != 0 || len(plan.tombstone) != 0 {
		t.Fatalf("nothing exists, everything creates: %+v", plan)
	}
}

func TestPlanDerivationUpdatesTitleAndStatus(t *testing.T) {
	// Marker kept, text edited and box checked: one update, nothing else.
	lines := parseTasks("- [x] renamed <!-- flowy:01AA -->")
	existing := []*Artifact{derivedTodoRow(t, "01AA", 1, "first", TodoStatus, false)}
	plan := planDerivation(lines, existing)
	if len(plan.create) != 0 || len(plan.tombstone) != 0 || len(plan.update) != 1 {
		t.Fatalf("one update, nothing else: %+v", plan)
	}
	u := plan.update[0]
	if u.status != DoneStatus || u.reopen || u.line.text != "renamed" {
		t.Fatalf("update as decided: %+v", u)
	}
}

func TestPlanDerivationReopensHandDone(t *testing.T) {
	// The row was closed by hand; the checkbox says open. The sync reopens
	// and says so on the row.
	lines := parseTasks("- [ ] first <!-- flowy:01AA -->")
	existing := []*Artifact{derivedTodoRow(t, "01AA", 1, "first", DoneStatus, false)}
	plan := planDerivation(lines, existing)
	if len(plan.update) != 1 {
		t.Fatalf("the divergence is one update: %+v", plan)
	}
	if u := plan.update[0]; u.status != TodoStatus || !u.reopen {
		t.Fatalf("reopened and flagged: %+v", u)
	}
}

func TestPlanDerivationActiveSurvivesUnchecked(t *testing.T) {
	// In progress is a state tasks.md cannot spell, so an unchecked box
	// never demotes it - and a row that already matches its line is not
	// rewritten at all.
	lines := parseTasks("- [ ] first <!-- flowy:01AA -->")
	existing := []*Artifact{derivedTodoRow(t, "01AA", 1, "first", ActiveStatus, false)}
	plan := planDerivation(lines, existing)
	if len(plan.update) != 0 || len(plan.create) != 0 || len(plan.tombstone) != 0 {
		t.Fatalf("active work survives untouched: %+v", plan)
	}
}

func TestPlanDerivationTombstonesMissing(t *testing.T) {
	// A line left the file: its todo is a tombstone. The surviving line
	// moved position - its row follows it, which is the one update.
	lines := parseTasks("- [ ] second <!-- flowy:02BB -->")
	existing := []*Artifact{
		derivedTodoRow(t, "01AA", 1, "first", TodoStatus, false),
		derivedTodoRow(t, "02BB", 2, "second", TodoStatus, false),
	}
	plan := planDerivation(lines, existing)
	if len(plan.tombstone) != 1 || plan.tombstone[0].ID != "01TODO01AA" {
		t.Fatalf("the missing line's todo is the tombstone: %+v", plan)
	}
	if len(plan.create) != 0 || len(plan.update) != 1 {
		t.Fatalf("the survivor follows its line, nothing else: %+v", plan)
	}
	if u := plan.update[0]; u.art.ID != "01TODO02BB" {
		t.Fatalf("the survivor is the row that moved: %+v", u)
	}
}

func TestPlanDerivationBootstrapsUnmarked(t *testing.T) {
	// A marker stripped by an editor: the line finds its row by text and
	// position, claims it with a fresh id, and keeps it - no create, no
	// tombstone.
	lines := parseTasks("- [ ] first\n")
	existing := []*Artifact{derivedTodoRow(t, "01AA", 1, "first", TodoStatus, false)}
	plan := planDerivation(lines, existing)
	if len(plan.update) != 1 || len(plan.create) != 0 || len(plan.tombstone) != 0 {
		t.Fatalf("the row is found, not replaced: %+v", plan)
	}
	if u := plan.update[0]; u.line.id == "" || u.line.id == "01AA" {
		t.Fatalf("a fresh id claims the row: %+v", u.line)
	}
}

func TestPlanDerivationMarkedLineRecreatesDeletedRow(t *testing.T) {
	// The row was deleted by hand; the file still names the line. The
	// file's id stands and a fresh row is derived under it - the file is
	// the truth.
	lines := parseTasks("- [ ] first <!-- flowy:01AA -->")
	plan := planDerivation(lines, nil)
	if len(plan.create) != 1 || plan.create[0].id != "01AA" {
		t.Fatalf("the file's id recreates the row: %+v", plan)
	}
}
