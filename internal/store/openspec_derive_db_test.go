package store

// The wire half of the derivation engine: that writing a change row actually
// derives its todos, against the database rather than by calling the engine
// directly (which openspec_derive_test.go does). The three write statements
// ask it through prepareChangeWrite and deriveChange, so a test through the
// real write path proves the whole funnel.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

func TestChangeWriteDerivesTodos(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-derive")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "do the thing\n",
		"tasks.md":    "## Work\n\n- [ ] first\n- [x] second\n",
	})

	// The stored file carries explicit line ids, because prepareChangeWrite
	// annotated it before the shape check and the signature.
	files, err := OpenspecFilesOf(art)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	lines := parseTasks(files["tasks.md"])
	if len(lines) != 2 || lines[0].id == "" || lines[1].id == "" {
		t.Fatalf("the stored tasks.md carries ids: %+v", lines)
	}

	// And the lines derived their todos, with the origin naming the line.
	todos, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("2 lines, 2 todos, got %d", len(todos))
	}
	for i, todo := range todos {
		if originLineOf(todo) != lines[i].id || originNumOf(todo) != lines[i].num {
			t.Fatalf("todo %d origin is %s@%d, want %s@%d", i,
				originLineOf(todo), originNumOf(todo), lines[i].id, lines[i].num)
		}
		if todo.Title != lines[i].text {
			t.Fatalf("todo %d title %q, want %q", i, todo.Title, lines[i].text)
		}
		if todo.OwnerUser != art.OwnerUser || *todo.Project != *art.Project {
			t.Fatalf("a derived todo belongs where its change belongs: %+v", todo)
		}
	}
	if todos[0].Status != TodoStatus || todos[1].Status != DoneStatus {
		t.Fatalf("the checkbox is the status: %q and %q", todos[0].Status, todos[1].Status)
	}
}

func TestTasksEditSyncsDerivedTodos(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-sync")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "do the thing\n",
		"tasks.md":    "- [ ] first\n- [ ] second\n- [ ] third\n",
	})
	before, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("3 lines, 3 todos, got %d", len(before))
	}
	thirdID := before[2].ID

	// One edit, all three moves at once: first renamed, second checked,
	// third's line dropped. The markers survive the edit, so the rows are
	// found by id.
	files, err := OpenspecFilesOf(art)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	files["tasks.md"] = "- [ ] renamed <!-- flowy:" + originLineOf(before[0]) + " -->\n" +
		"- [x] second <!-- flowy:" + originLineOf(before[1]) + " -->\n"
	raw, err := json.Marshal(map[string]any{"openspec": map[string]any{"files": files}})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	art.Fields = raw
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("rewrite the change: %v", err)
	}

	after, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("2 lines survive, 2 todos, got %d", len(after))
	}
	if after[0].ID != before[0].ID || after[0].Title != "renamed" {
		t.Fatalf("the renamed line kept its row and moved its title: %+v", after[0])
	}
	if after[1].ID != before[1].ID || after[1].Status != DoneStatus {
		t.Fatalf("the checked line kept its row and closed: %+v", after[1])
	}
	var tombstoned bool
	if err := db.sql.QueryRowContext(ctx,
		`SELECT coalesce(tombstone, false) FROM artifacts WHERE id = $1`, thirdID).
		Scan(&tombstoned); err != nil {
		t.Fatalf("read the dropped todo: %v", err)
	}
	if !tombstoned {
		t.Fatalf("the dropped line's todo is a tombstone")
	}
}

func TestHandDoneTodoReopenedBySync(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-reopen")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "do the thing\n",
		"tasks.md":    "- [ ] first\n",
	})
	todos, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(todos) != 1 {
		t.Fatalf("1 line, 1 todo, got %d", len(todos))
	}

	// Somebody closes the row by hand; the checkbox stays open. They
	// diverge until the next write of the change re-syncs them.
	if _, _, err := db.SetTodoStatus(ctx, p, todos[0].ID, "done",
		"closed by hand in the test"); err != nil {
		t.Fatalf("close by hand: %v", err)
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("rewrite the change: %v", err)
	}

	after, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(after) != 1 || after[0].ID != todos[0].ID {
		t.Fatalf("the row survives the sync: %+v", after)
	}
	if after[0].Status != TodoStatus || !originReopenedOf(after[0]) {
		t.Fatalf("the sync reopens and says so on the row: status %q reopened %v",
			after[0].Status, originReopenedOf(after[0]))
	}
}

func TestNoTasksMDTombstonesAll(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-notasks")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "do the thing\n",
		"tasks.md":    "- [ ] first\n",
	})
	todos, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(todos) != 1 {
		t.Fatalf("1 line, 1 todo, got %d", len(todos))
	}

	// tasks.md leaves the change. The file is the truth: every derived
	// todo goes with it.
	files, err := OpenspecFilesOf(art)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	delete(files, "tasks.md")
	raw, err := json.Marshal(map[string]any{"openspec": map[string]any{"files": files}})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	art.Fields = raw
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("rewrite the change: %v", err)
	}

	after, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("no tasks.md, no derived todos, got %d", len(after))
	}
	var tombstoned bool
	if err := db.sql.QueryRowContext(ctx,
		`SELECT coalesce(tombstone, false) FROM artifacts WHERE id = $1`, todos[0].ID).
		Scan(&tombstoned); err != nil {
		t.Fatalf("read the todo: %v", err)
	}
	if !tombstoned {
		t.Fatalf("the todo left with its file")
	}
}

func TestDerivedTodoCarriesOriginInFields(t *testing.T) {
	// The association is a fact on the row, read back through the same
	// fields surface every other association uses - the derivation query
	// depends on it, so prove the shape survives a round trip.
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-origin")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "do the thing\n",
		"tasks.md":    "- [ ] first\n",
	})
	todos, err := db.derivedTodosOf(ctx, db.sql, art.ID)
	if err != nil {
		t.Fatalf("derived todos: %v", err)
	}
	if len(todos) != 1 {
		t.Fatalf("1 line, 1 todo, got %d", len(todos))
	}
	change, line, _, _ := originOf(todos[0])
	if change != art.ID || line == "" || !strings.Contains(string(todos[0].Fields), `"change"`) {
		t.Fatalf("origin names the change and the line: %+v", todos[0].Fields)
	}
}
