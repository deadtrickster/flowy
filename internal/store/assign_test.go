package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// fieldsWithout is a fields blob with one key taken back out, which is how a row
// from before that key existed is written here. Removing it is not the same as
// setting it empty - see AssigneeOf, where a key that is present wins whatever
// it holds.
func fieldsWithout(t *testing.T, fields json.RawMessage, key string) json.RawMessage {
	t.Helper()

	var m map[string]any
	if len(fields) > 0 {
		if err := json.Unmarshal(fields, &m); err != nil {
			t.Fatalf("fields: %v", err)
		}
	}
	delete(m, key)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	return raw
}

// assigneeIn is what a reader's queue says is carrying one todo, and who put them
// there - both through that reader's own filter, which is the point of asking it
// this way rather than reading the row.
func assigneeIn(
	t *testing.T, ctx context.Context, db *DB, p *Principal, id string,
) (string, *Assignment) {
	t.Helper()

	r, found := readyOf(t, ctx, db, p, id)
	if !found {
		t.Fatalf("%s cannot see todo %s at all", p.UserID, id)
	}
	return r.Assignee, r.Assignment
}

// THE ONE THAT MATTERS.
//
// A todo one principal wrote, assigned by another one who did not write it and
// cannot write anything else about it. That is the whole ruling: the queue was
// filed by one agent and nobody else could ever own a line of it, so every row
// said nobody was carrying it and the operator asked three times why.
//
// The second half is the record. The entry names the seat that made the claim and
// the person behind it, so "the operator handed this over" and "an agent claimed
// it" are different answers rather than the same silent field write - which is the
// thing a column cannot say and the reason this is an event.
func TestAnybodyWhoCanReadATodoCanAssignIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pas")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	// Another person in the same project, with an agent of their own: the agent is
	// its own seat here, exactly as it is its own voter.
	other := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "bench-test the gearbox", VisibilityProjectOnly, "")
	if got, claim := assigneeIn(t, ctx, db, other, todo.ID); got != "" || claim != nil {
		t.Fatalf("a fresh todo already reads as carried by %q (%v)", got, claim)
	}

	art, entry, err := db.AssignTodo(ctx, other, todo.ID, "b-drainer", nil)
	if err != nil {
		t.Fatalf("a principal who did not write the todo was refused: %v", err)
	}
	if got := AssigneeOf(art); got != "b-drainer" {
		t.Fatalf("the row came back carrying %q", got)
	}
	if entry.Type != EventTodoAssign || entry.Artifact != todo.ID {
		t.Fatalf("the entry is a %q about %q", entry.Type, entry.Artifact)
	}
	// The row is still the author's. An assignment moves one key in fields, and
	// nothing about who owns the item or what it says.
	if art.OwnerUser != author.UserID || art.Title != "bench-test the gearbox" {
		t.Fatalf("the assignment rewrote the item: owner %q, title %q", art.OwnerUser, art.Title)
	}

	// What the AUTHOR reads, which is the half that fails when the entry hangs off
	// the wrong row: the value, and who put it there.
	got, claim := assigneeIn(t, ctx, db, author, todo.ID)
	if got != "b-drainer" {
		t.Fatalf("the author's queue says %q is carrying it", got)
	}
	if claim == nil {
		t.Fatal("the author cannot read the entry behind an assignment of their own todo")
	}
	if claim.Assignee != "b-drainer" || claim.By != other.AgentID || claim.ByUser != other.UserID {
		t.Fatalf("the claim reads %+v, want b-drainer by %s for %s", claim, other.AgentID, other.UserID)
	}
	if claim.ByKind != "agent" {
		t.Fatalf("an agent's claim came back as kind %q", claim.ByKind)
	}
	if claim.At == "" || claim.Entry != entry.ID {
		t.Fatalf("the claim does not name when or which entry: %+v", claim)
	}
}

// A principal who cannot READ the todo cannot assign it, and finds out exactly
// what a read of it would have told them - which is nothing about the row.
//
// Read permission is the whole bar, so this is the only refusal that matters, and
// a write that got through here would be a principal editing an item in a project
// it has no reach into.
func TestAPrincipalWhoCannotReadATodoCannotAssignIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pat")
	elsewhere := declaredProject(t, ctx, db, "pau")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	todo := todoIn(t, ctx, db, author, "rebuild the gearbox", VisibilityProjectOnly, "a-bench")

	_, _, err := db.AssignTodo(ctx, stranger, todo.ID, "b-drainer", nil)
	if err == nil {
		t.Fatal("a principal with no reach into the project assigned the todo anyway")
	}
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refusal is %v, want the answer a read of the id would give", err)
	}
	// And nothing moved. A refusal that wrote the value and then said no would be
	// the failure this round is about, from the other end.
	if got, _ := assigneeIn(t, ctx, db, author, todo.ID); got != "a-bench" {
		t.Fatalf("the refused write moved the assignee to %q", got)
	}
	log, err := db.AssignLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("assign log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("the refused write left %d entries in the log", len(log))
	}
}

// Two seats claiming the same todo, across a grant. Latest wins, both claims stay
// in the log, and the second one can be read by the first - which is what makes a
// handover between agents legible after the fact.
//
// The grant is what makes this one queue seen by two principals rather than two
// principals who cannot see each other at all.
func TestTheLatestClaimWinsAndTheLogKeepsTheRest(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pav")
	elsewhere := declaredProject(t, ctx, db, "paw")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	across := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: elsewhere, ToProject: here, GrantedBy: author.UserID,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	todo := todoIn(t, ctx, db, author, "drain the queue", VisibilityShared, "")

	if _, _, err := db.AssignTodo(ctx, author, todo.ID, "a-bench", nil); err != nil {
		t.Fatalf("the author claiming their own todo was refused: %v", err)
	}
	// The handover: somebody else takes it, from another project, over the grant.
	if _, _, err := db.AssignTodo(ctx, across, todo.ID, "b-drainer", nil); err != nil {
		t.Fatalf("a reader across the grant was refused: %v", err)
	}

	got, claim := assigneeIn(t, ctx, db, author, todo.ID)
	if got != "b-drainer" {
		t.Fatalf("the last claim did not win: %q", got)
	}
	if claim == nil || claim.ByUser != across.UserID {
		t.Fatalf("the standing claim is %+v, want it made by %s", claim, across.UserID)
	}

	log, err := db.AssignLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("assign log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want both claims", len(log))
	}
	if log[0].Assignee != "a-bench" || log[1].Assignee != "b-drainer" {
		t.Fatalf("the log is %q then %q, want oldest first", log[0].Assignee, log[1].Assignee)
	}
	// Putting it down is a claim too, and it is the case a truthiness test gets
	// wrong: the empty name is a value somebody chose.
	if _, _, err := db.AssignTodo(ctx, across, todo.ID, "unassigned", nil); err != nil {
		t.Fatalf("putting the work down was refused: %v", err)
	}
	got, claim = assigneeIn(t, ctx, db, author, todo.ID)
	if got != "" {
		t.Fatalf("after being put down the todo reads as carried by %q", got)
	}
	if claim == nil || claim.Assignee != "" {
		t.Fatalf("nobody-is-carrying-it came back as %+v, want a claim saying nobody", claim)
	}
}

// What is not a queue item is not assignable, and says so the way every other
// queue verb does: naming an id here is not a way to find out what else it might
// be. A report with an assignee on it would be an assignee nothing ever reads.
func TestOnlyAQueueItemTakesAnAssignee(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pax")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	project := here
	report := &Artifact{
		ID: ulid.NewString(), Type: "report", Kind: "handoff", Project: &project,
		OwnerUser: p.UserID, Title: "what the last shift did", Visibility: VisibilityProjectOnly,
	}
	if err := db.UpsertArtifact(ctx, report); err != nil {
		t.Fatalf("write report: %v", err)
	}

	_, _, err := db.AssignTodo(ctx, p, report.ID, "a-bench", nil)
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) {
		t.Fatalf("assigning a report was answered with %v", err)
	}
}

// A name is a handle on one line, wherever it arrives - the verb normalises it
// itself rather than trusting the door to have done it, because both doors and the
// memory tools call this and a name that is a paragraph must be refused at all
// three.
func TestTheVerbRefusesANameThatIsNotOne(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pay")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	todo := todoIn(t, ctx, db, p, "keep the name a name", VisibilityProjectOnly, "a-bench")

	_, _, err := db.AssignTodo(ctx, p, todo.ID, "two\nlines", nil)
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a two-line name was answered with %v, want a refusal the caller can fix", err)
	}
	if got, _ := assigneeIn(t, ctx, db, p, todo.ID); got != "a-bench" {
		t.Fatalf("the refused name still moved the assignee to %q", got)
	}
}

// TestAReadSaysWhoIsCarryingItWithoutDiggingIntoFields is the queue's two
// most-read facts arriving in one shape.
//
// Status was top level and assignee was one level down in fields, and neither
// was discoverable from the other. Three agents misread the board in one
// afternoon because of it, every read succeeding and every read about the wrong
// population: one filtered status inside .fields and called 23 finished rows
// open, another parsed the owner out of the body with the wrong prefix, got
// twelve honest blanks, and reassigned three rows somebody else had claimed.
//
// So a read now answers both from the row itself, resolved the one way -
// AssigneeOf - rather than each client rolling its own and getting it wrong.
// The legacy OWNER line still resolves, because most of this queue predates the
// field.
func TestAReadSaysWhoIsCarryingItWithoutDiggingIntoFields(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pax")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	claimed := todoIn(t, ctx, db, author, "reseat the intake valve", VisibilityShared, "")
	if _, _, err := db.AssignTodo(ctx, author, claimed.ID, "a-welder", nil); err != nil {
		t.Fatalf("assign: %v", err)
	}

	// Written the way the whole queue was before the field existed: the OWNER
	// line in the body and NO assignee key, because there was no field to put
	// one in. The key has to go rather than be left empty - todoIn writes it
	// either way, and a key that is there wins even when it is empty, so a row
	// that kept it would be somebody saying nobody is carrying this instead of
	// the pre-field row it stands in for. See AssigneeOf.
	legacy := todoIn(t, ctx, db, author, "rewrite the pruning notes", VisibilityShared, "")
	legacy.Body = "OWNER: a-gardener\n\nthe notes are stale"
	legacy.Fields = fieldsWithout(t, legacy.Fields, AssigneeField)
	if err := db.UpsertArtifact(ctx, legacy); err != nil {
		t.Fatalf("legacy todo: %v", err)
	}

	unowned := todoIn(t, ctx, db, author, "nobody has this one", VisibilityShared, "")

	want := map[string]string{
		claimed.ID: "a-welder",
		legacy.ID:  "a-gardener",
		unowned.ID: "",
	}

	// Through the LIST path, which is what every board read uses.
	arts, err := db.ListArtifacts(ctx, author, ArtifactQuery{Type: "memory", Kind: "todo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := 0
	for _, a := range arts {
		expect, ours := want[a.ID]
		if !ours {
			continue
		}
		seen++
		if a.Assignee != expect {
			t.Errorf("%q: read says assignee %q, want %q - a reader would have to dig into fields",
				a.Title, a.Assignee, expect)
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of the %d todos written here", seen, len(want))
	}

	// And through the single-artifact path, so the two doors cannot disagree.
	one, err := db.ReadArtifact(ctx, author, claimed.ID, false)
	if err != nil {
		t.Fatalf("read one: %v", err)
	}
	if one.Assignee != "a-welder" {
		t.Errorf("reading one artifact says assignee %q, want %q", one.Assignee, "a-welder")
	}
}
