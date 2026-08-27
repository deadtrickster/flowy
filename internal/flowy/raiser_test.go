package flowy

// WHO RAISED A QUEUE ITEM, against a real store.
//
// The fact being tested is not that a string round-trips through fields. It is
// that the row says where the work came from WITHOUT anybody typing it, and
// that it says nothing at all when nobody said - the two ways this feature is
// worth having and the one way it would quietly become a lie.
//
// owner_user is the seat whose token wrote the row and it is not this. Every
// test here writes the todo from a DIFFERENT seat than the one the work came
// from, because that is the case the board is full of and the case where the
// two facts are told apart or are not: an agent files a line the operator asked
// for, and a row that only carried its author reads as work the agent invented.
//
// They need a database, so they sit out a plain `go test ./...` and run under
// ./run-tests.sh, the same way the chat tools' own live tests do.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// raisedTodo files a todo through mem_write and hands back the item.
func raisedTodo(
	t *testing.T, ctx context.Context, db *store.DB, p *store.Principal, args map[string]any,
) *store.Artifact {
	t.Helper()

	out, err := callChat(t, ctx, db, p, "mem_write", args)
	if err != nil {
		t.Fatalf("filing a todo failed: %v", err)
	}
	item, ok := out["item"].(*store.Artifact)
	if !ok {
		t.Fatalf("mem_write answered with %T where the item should be", out["item"])
	}
	return item
}

// fieldsOf is the row's own fields, as a map, so a test can ask whether a key is
// THERE rather than what it holds. Absent and empty are two different answers
// here and only one of them is a claim somebody made.
func fieldsOf(t *testing.T, a *store.Artifact) map[string]any {
	t.Helper()

	fields := map[string]any{}
	if len(a.Fields) == 0 {
		return fields
	}
	if err := json.Unmarshal(a.Fields, &fields); err != nil {
		t.Fatalf("the item's fields do not parse: %v", err)
	}
	return fields
}

// A todo raised out of a message carries the speaker of that message, and the
// seat that filed it stays the row's author.
//
// This is the whole feature. The operator asks for something in a room, an agent
// files the line, and until now the only party on the row was the agent - so the
// board could not say where the work came from, and the ask lived in a
// conversation nobody rereads. Nobody types the name here: the todo names the
// message, and the message already knows who spoke.
func TestATodoRaisedOutOfAMessageSaysWhoseRequestItWas(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "raiser-default")
	asker := newSeat(t, ctx, db, project, "operator")
	filer := newSeat(t, ctx, db, project, "agent")

	out, err := callChat(t, ctx, db, asker.p, "chat_say", map[string]any{
		"room": room, "text": "somebody split the todo title from the body",
	})
	if err != nil {
		t.Fatalf("the ask could not be said in the room: %v", err)
	}
	ask, ok := out["message"].(*store.Event)
	if !ok {
		t.Fatalf("chat_say answered with %T where the message should be", out["message"])
	}

	item := raisedTodo(t, ctx, db, filer.p, map[string]any{
		"title": "split the todo title from the body", "kind": "todo",
		"scope": "project", "room": room, "message": ask.ID,
	})
	if got := store.RaiserOf(item); got != asker.handle {
		t.Fatalf("the todo says the work came from %q, want %q - the speaker of the "+
			"message it was raised out of", got, asker.handle)
	}
	// And the other half of it: the two facts are different facts. A row where
	// the raiser had displaced the author would have lost the provenance this
	// fabric signs, and one where the author had displaced the raiser is where
	// this started.
	if item.OwnerUser != filer.p.UserID {
		t.Fatalf("the row is owned by %q, want the seat that wrote it (%q): a raiser "+
			"does not move owner_user", item.OwnerUser, filer.p.UserID)
	}

	// Read back through the permission filter rather than off the write, because
	// that is where every surface gets it - and the node puts it on the row
	// beside the assignee so one read answers both.
	read, err := db.ReadArtifact(ctx, filer.p, item.ID, false)
	if err != nil {
		t.Fatalf("reading the todo back failed: %v", err)
	}
	if read.Raiser != asker.handle {
		t.Fatalf("read back, the row says it was raised by %q, want %q", read.Raiser, asker.handle)
	}
}

// An explicit raiser wins over the message it was raised out of.
//
// The default is a courtesy and not a ruling: an agent filing on somebody's
// behalf knows whose work it is, and the message it happens to be replying to
// is not always the ask. Stating it has to be the last word or the field would
// be a guess nobody can correct.
func TestAnExplicitRaiserWinsOverTheMessagesSpeaker(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "raiser-explicit")
	speaker := newSeat(t, ctx, db, project, "speaker")
	filer := newSeat(t, ctx, db, project, "agent")

	out, err := callChat(t, ctx, db, speaker.p, "chat_say", map[string]any{
		"room": room, "text": "the gate is green again",
	})
	if err != nil {
		t.Fatalf("the message could not be said: %v", err)
	}
	said := out["message"].(*store.Event)

	item := raisedTodo(t, ctx, db, filer.p, map[string]any{
		"title": "re-gate the branch", "kind": "todo", "scope": "project",
		"room": room, "message": said.ID, "raiser": "the-operator",
	})
	if got := store.RaiserOf(item); got != "the-operator" {
		t.Fatalf("the stated raiser is %q, want %q - a default that outranks what "+
			"somebody said is not a default", got, "the-operator")
	}

	// And where the raiser is settled: at the raise. An update that states one is
	// refused rather than rewriting it, because work changes hands and where it
	// came from does not - and a weak fact that can be edited quietly on an
	// unrelated write is a fact nobody can rely on.
	if _, err := callChat(t, ctx, db, filer.p, "mem_write", map[string]any{
		"id": item.ID, "raiser": "somebody-else",
	}); err == nil {
		t.Fatal("an update restated the raiser and was taken; it has to be refused")
	} else if !strings.Contains(err.Error(), "settled when it is raised") {
		t.Fatalf("the refusal does not say why a raiser cannot be restated: %v", err)
	}
	after, err := db.ReadArtifact(ctx, filer.p, item.ID, false)
	if err != nil {
		t.Fatalf("reading the todo back failed: %v", err)
	}
	if after.Raiser != "the-operator" {
		t.Fatalf("the refused update moved the raiser to %q anyway", after.Raiser)
	}
}

// A row that nobody said anything about says nothing, and nothing is inferred.
//
// Every queue item on this board was written before this field, and the wrong
// way to be compatible with them is to answer owner_user - which is the seat
// whose token wrote the row, is the same id for every row one operator filed,
// and would put a name that nobody claimed on the whole board at once. So the
// key is absent, the reader answers empty, and "nobody said where this came
// from" is a state a surface can draw.
func TestARowWithNoRaiserSaysNobodyAndNothingIsInferred(t *testing.T) {
	ctx, db := chatStore(t)
	project, _ := declaredRoom(t, ctx, db, "raiser-absent")
	filer := newSeat(t, ctx, db, project, "agent")

	item := raisedTodo(t, ctx, db, filer.p, map[string]any{
		"title": "a todo raised out of no conversation at all",
		"kind":  "todo", "scope": "project",
	})
	if _, found := fieldsOf(t, item)[store.RaiserField]; found {
		t.Fatalf("a write that said nothing about a raiser wrote one anyway: %s", item.Fields)
	}
	if got := store.RaiserOf(item); got != "" {
		t.Fatalf("a row with no raiser reads as %q, want empty", got)
	}

	read, err := db.ReadArtifact(ctx, filer.p, item.ID, false)
	if err != nil {
		t.Fatalf("reading the todo back failed: %v", err)
	}
	if read.Raiser != "" {
		t.Fatalf("read back, a row nobody stated a raiser on says %q", read.Raiser)
	}

	// The rows written before the field are exactly this shape - fields with no
	// such key, or no fields at all - and the reader has to answer the same for
	// both without reaching for the author.
	legacy := &store.Artifact{
		OwnerUser: filer.p.UserID,
		Fields:    json.RawMessage(`{"room":"build","assignee":"a-writer"}`),
	}
	if got := store.RaiserOf(legacy); got != "" {
		t.Fatalf("a row from before this field reads as raised by %q", got)
	}
	if got := store.RaiserOf(&store.Artifact{OwnerUser: filer.p.UserID}); got != "" {
		t.Fatalf("a row with no fields at all reads as raised by %q", got)
	}
}

// The names it will take, and the ones it will not.
//
// It is the assignee's bar because it is the same kind of value in the same kind
// of column, and the words this queue has always used for nobody collapse the
// same way - so "unassigned" and an absent key are ONE state on every surface
// rather than two that read as a distinction. A pasted paragraph is refused
// rather than stored, at every door, because a name nobody can draw is a row
// nobody can read.
func TestARaiserIsAHandleAndTheWordsForNobodyCollapse(t *testing.T) {
	for _, ok := range []string{"", "  ", "operator", "  a-writer  "} {
		if _, err := store.NormalizeRaiser(ok); err != nil {
			t.Fatalf("NormalizeRaiser(%q) was refused: %v", ok, err)
		}
	}
	if got, _ := store.NormalizeRaiser("  a-writer  "); got != "a-writer" {
		t.Fatalf("a raiser came back as %q rather than trimmed", got)
	}
	for _, nobody := range []string{"?", "-", "none", "nobody", "TBD", "unassigned", "n/a"} {
		got, err := store.NormalizeRaiser(nobody)
		if err != nil {
			t.Fatalf("NormalizeRaiser(%q) was refused: %v", nobody, err)
		}
		if got != "" {
			t.Fatalf("%q came back as %q rather than as nobody", nobody, got)
		}
	}
	for _, bad := range []string{"two\nlines", strings.Repeat("x", store.MaxRaiserName+1)} {
		if _, err := store.NormalizeRaiser(bad); err == nil {
			t.Fatalf("NormalizeRaiser(%q) was taken; a raiser is a handle", bad)
		}
	}
}
