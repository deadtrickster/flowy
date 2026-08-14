package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// open dials the database the gate started. Without DATABASE_URL there is
// nothing to talk to, so these tests sit out a plain `go test ./...`.
func open(t *testing.T) (context.Context, *DB) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, db
}

func TestOpenRejectsEmptyDSN(t *testing.T) {
	if _, err := Open(context.Background(), "", "n"); err == nil {
		t.Fatal("Open with an empty DSN returned no error")
	}
}

func TestUserRoundTrip(t *testing.T) {
	ctx, db := open(t)

	u := &User{Handle: "h-" + ulid.NewString(), Display: "Display", AutoDelegate: true}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if u.ID == "" || u.HLC == 0 || u.Node != "test-node" {
		t.Fatalf("insert did not stamp the row: %+v", u)
	}

	got, err := db.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if *got != *u {
		t.Fatalf("read back %+v, want %+v", got, u)
	}
}

func TestHandleIsUnique(t *testing.T) {
	ctx, db := open(t)

	handle := "dup-" + ulid.NewString()
	if err := db.InsertUser(ctx, &User{Handle: handle}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := db.InsertUser(ctx, &User{Handle: handle}); err == nil {
		t.Fatal("second insert with the same handle was accepted")
	}
}

func TestAgentReferencesUser(t *testing.T) {
	ctx, db := open(t)

	u := &User{Handle: "a-" + ulid.NewString()}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	for _, kind := range []string{"claude", "glm", "opencode"} {
		a := &Agent{UserID: u.ID, Kind: kind, Project: "flowy"}
		if err := db.InsertAgent(ctx, a); err != nil {
			t.Fatalf("insert %s agent: %v", kind, err)
		}
		got, err := db.GetAgent(ctx, a.ID)
		if err != nil {
			t.Fatalf("get %s agent: %v", kind, err)
		}
		if *got != *a {
			t.Fatalf("read back %+v, want %+v", got, a)
		}
	}

	orphan := &Agent{UserID: ulid.NewString(), Kind: "claude"}
	if err := db.InsertAgent(ctx, orphan); err == nil {
		t.Fatal("agent with an unknown user_id was accepted")
	}
}

func TestArtifactArraysAndJSON(t *testing.T) {
	ctx, db := open(t)

	project := "flowy"
	a := &Artifact{
		Type:      "bug",
		Project:   &project,
		OwnerUser: "u-" + ulid.NewString(),
		Title:     "arrays survive",
		Tags:      []string{"a", "b", "c"},
		UserTags:  []string{"mine"},
		Related:   []string{ulid.NewString()},
		Fields:    json.RawMessage(`{"k":[1,2,3]}`),
	}
	if err := db.InsertArtifact(ctx, a); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if a.Visibility != "project" {
		t.Fatalf("visibility defaulted to %q, want project", a.Visibility)
	}

	got, err := db.GetArtifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project == nil || *got.Project != project {
		t.Fatalf("project read back as %v", got.Project)
	}
	for _, pair := range []struct {
		what      string
		got, want []string
	}{
		{"tags", got.Tags, a.Tags},
		{"user_tags", got.UserTags, a.UserTags},
		{"related", got.Related, a.Related},
	} {
		if len(pair.got) != len(pair.want) {
			t.Fatalf("%s read back as %v, want %v", pair.what, pair.got, pair.want)
		}
		for i := range pair.want {
			if pair.got[i] != pair.want[i] {
				t.Fatalf("%s read back as %v, want %v", pair.what, pair.got, pair.want)
			}
		}
	}

	var fields map[string][]int
	if err := json.Unmarshal(got.Fields, &fields); err != nil {
		t.Fatalf("fields did not survive as jsonb: %v", err)
	}
	if len(fields["k"]) != 3 {
		t.Fatalf("fields read back as %v", fields)
	}
	if got.Tombstone {
		t.Fatal("tombstone defaulted to true")
	}
}

func TestPersonalArtifactHasNoProject(t *testing.T) {
	ctx, db := open(t)

	a := &Artifact{
		Type:       "note",
		OwnerUser:  "u-" + ulid.NewString(),
		Title:      "personal",
		Visibility: "personal",
	}
	if err := db.InsertArtifact(ctx, a); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := db.GetArtifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != nil {
		t.Fatalf("project read back as %q, want NULL", *got.Project)
	}
	if got.Visibility != "personal" {
		t.Fatalf("visibility = %q, want personal", got.Visibility)
	}
}

func TestEventThreadDAG(t *testing.T) {
	ctx, db := open(t)

	root := &Event{Type: "thread.opened", Room: "r", Body: "root"}
	if err := db.AppendEvent(ctx, root); err != nil {
		t.Fatalf("append root: %v", err)
	}
	if root.Thread != root.ID {
		t.Fatalf("thread = %q, want the root id %q", root.Thread, root.ID)
	}

	left := &Event{Type: "note", Thread: root.Thread, Parents: []string{root.ID}, Body: "left"}
	right := &Event{Type: "note", Thread: root.Thread, Parents: []string{root.ID}, Body: "right"}
	for _, e := range []*Event{left, right} {
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append branch: %v", err)
		}
	}

	merge := &Event{
		Type:    "merge",
		Thread:  root.Thread,
		Parents: []string{left.ID, right.ID},
		Body:    "merge",
		Meta:    json.RawMessage(`{"strategy":"lww"}`),
	}
	if err := db.AppendEvent(ctx, merge); err != nil {
		t.Fatalf("append merge: %v", err)
	}

	got, err := db.GetEvent(ctx, merge.ID)
	if err != nil {
		t.Fatalf("get merge: %v", err)
	}
	if len(got.Parents) != 2 || got.Parents[0] != left.ID || got.Parents[1] != right.ID {
		t.Fatalf("parents read back as %v, want [%s %s]", got.Parents, left.ID, right.ID)
	}

	thread, err := db.ThreadEvents(ctx, root.Thread)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if len(thread) != 4 {
		t.Fatalf("thread has %d events, want 4", len(thread))
	}
	for i := 1; i < len(thread); i++ {
		if thread[i].SeqHLC <= thread[i-1].SeqHLC {
			t.Fatalf("seq_hlc did not advance between events %d and %d", i-1, i)
		}
	}

	// An event with no parents opens its own thread.
	if len(thread[0].Parents) != 0 {
		t.Fatalf("root has parents %v, want none", thread[0].Parents)
	}
}

func TestEmptyParentsRoundTrip(t *testing.T) {
	ctx, db := open(t)

	e := &Event{Type: "note", Parents: []string{}, Body: "no parents"}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := db.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Parents) != 0 {
		t.Fatalf("parents read back as %v, want empty", got.Parents)
	}
}

// TestGrantAndTaskDefaults pins the column defaults the later phases assume.
func TestGrantAndTaskDefaults(t *testing.T) {
	ctx, db := open(t)

	grantID := ulid.NewString()
	_, err := db.SQL().ExecContext(ctx,
		`INSERT INTO grants (id, from_project, to_project, subject, granted_by, hlc, node)
		 VALUES ($1, 'flowy', 'other', 'bugs', 'u1', $2, $3)`,
		grantID, db.Clock().Pack(), db.Node())
	if err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	var capability string
	var tombstone bool
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT cap, tombstone FROM grants WHERE id = $1`, grantID).Scan(&capability, &tombstone); err != nil {
		t.Fatalf("read grant: %v", err)
	}
	if capability != "read" || tombstone {
		t.Fatalf("grant defaults are cap=%q tombstone=%v, want read/false", capability, tombstone)
	}

	taskID := ulid.NewString()
	_, err = db.SQL().ExecContext(ctx,
		`INSERT INTO tasks (id, artifact, from_user, to_user, project, hlc, node)
		 VALUES ($1, $2, 'u1', 'u2', 'flowy', $3, $4)`,
		taskID, ulid.NewString(), db.Clock().Pack(), db.Node())
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	var state string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT state FROM tasks WHERE id = $1`, taskID).Scan(&state); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if state != "open" {
		t.Fatalf("task state defaulted to %q, want open", state)
	}
}

func TestPeerCursors(t *testing.T) {
	ctx, db := open(t)

	peer := "peer-" + ulid.NewString()
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO peers (peer) VALUES ($1)`, peer); err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	var pull, pushed int64
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT pull_cursor, pushed_cursor FROM peers WHERE peer = $1`, peer).Scan(&pull, &pushed); err != nil {
		t.Fatalf("read peer: %v", err)
	}
	if pull != 0 || pushed != 0 {
		t.Fatalf("cursors defaulted to %d/%d, want 0/0", pull, pushed)
	}

	now := db.Clock().Pack()
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE peers SET pull_cursor = $1, last_seen = now() WHERE peer = $2`, now, peer); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	var lastSeen time.Time
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT pull_cursor, last_seen FROM peers WHERE peer = $1`, peer).Scan(&pull, &lastSeen); err != nil {
		t.Fatalf("read peer back: %v", err)
	}
	if pull != now || lastSeen.IsZero() {
		t.Fatalf("cursor read back as %d at %s, want %d", pull, lastSeen, now)
	}
}

// TestSeqHLCPaging is the shape sync will use: read everything a peer has not
// seen, ordered by the packed clock.
func TestSeqHLCPaging(t *testing.T) {
	ctx, db := open(t)

	room := "room-" + ulid.NewString()
	cursor := db.Clock().Pack()

	var ids []string
	for i := 0; i < 5; i++ {
		e := &Event{Type: "note", Room: room, Body: "n"}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
		ids = append(ids, e.ID)
	}

	rows, err := db.SQL().QueryContext(ctx,
		`SELECT id, seq_hlc FROM events WHERE room = $1 AND seq_hlc > $2 ORDER BY seq_hlc`, room, cursor)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	defer rows.Close()

	var got []string
	var last int64
	for rows.Next() {
		var id string
		var seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if seq <= last && last != 0 {
			t.Fatalf("seq_hlc went backwards: %d after %d", seq, last)
		}
		last = seq
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("paged %d events, want %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("paged out of order at %d: %s, want %s", i, got[i], ids[i])
		}
	}
}

// TestArrayContainment covers the query the DAG walk will need: find the
// children of an event.
func TestArrayContainment(t *testing.T) {
	ctx, db := open(t)

	parent := &Event{Type: "note", Body: "parent"}
	if err := db.AppendEvent(ctx, parent); err != nil {
		t.Fatalf("append parent: %v", err)
	}
	child := &Event{Type: "note", Thread: parent.Thread, Parents: []string{parent.ID}, Body: "child"}
	if err := db.AppendEvent(ctx, child); err != nil {
		t.Fatalf("append child: %v", err)
	}

	rows, err := db.SQL().QueryContext(ctx,
		`SELECT id FROM events WHERE parents @> $1 ORDER BY seq_hlc`, pq.Array([]string{parent.ID}))
	if err != nil {
		t.Fatalf("query children: %v", err)
	}
	defer rows.Close()

	var found []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(found) != 1 || found[0] != child.ID {
		t.Fatalf("children of %s came back as %v, want [%s]", parent.ID, found, child.ID)
	}
}
