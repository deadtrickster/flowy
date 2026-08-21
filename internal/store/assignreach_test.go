package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A SEAT IS NOT HANDED WORK IT CANNOT SEE.
//
// Every other refusal on the assign door is about the CALLER - may you read
// this row, are you the holder. This is the only one about the party being
// named, and without it a row in one project can be given to a seat holding no
// credential for it: the agent polls, sees nothing, and reports that it has no
// work, which is indistinguishable from a quiet queue.
func TestAssigningToASeatThatCannotSeeTheRowIsRefused(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "reachhere")
	there := declaredProject(t, ctx, db, "reachthere")

	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	// The party being handed work: a user with a handle and a credential that
	// reaches only the OTHER project.
	// THE HANDLE CARRIES A ULID, and that is not decoration: `users_handle_key`
	// is unique, so a fixture handle spelled as a literal inserts once per
	// DATABASE rather than once per run. These three tests were the four that
	// passed on a fresh database and red on the second run against the same
	// one - see 01M0HJ1M25, where that cost three false diagnoses in a night.
	farside := "farside-" + ulid.Short()
	stranger := &User{ID: "u-" + ulid.NewString(), Handle: farside}
	if err := db.InsertUser(ctx, stranger); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.InsertToken(ctx, &Principal{
		Token: "t-" + ulid.NewString(), UserID: stranger.ID, Project: there,
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}

	todo := todoIn(t, ctx, db, author, "work in a project they cannot read", VisibilityProjectOnly, "")

	_, _, err := db.AssignTodo(ctx, author, todo.ID, farside, nil)
	if err == nil {
		t.Fatal("a row was handed to a seat whose every credential is in another project")
	}
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %v, want one the caller can act on", err)
	}
	// BOTH SIDES IN THE SENTENCE: the caller has to decide whether the row is in
	// the wrong project or the seat needs a credential.
	for _, want := range []string{farside, here, there} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// AND THE ROW DID NOT MOVE. A refusal that assigned anyway and then said no
	// would be the defect with a message attached.
	back, err := db.ReadArtifact(ctx, author, todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := strings.TrimSpace(artifactString(back, AssigneeField)); got != "" {
		t.Fatalf("the refused assignment left %q carrying the row", got)
	}
}

// A CREDENTIAL THAT REACHES THE ROW'S PROJECT IS ENOUGH, even when the seat
// ACTS somewhere else.
//
// This is the arm that stops the rule being a column comparison: reach is a
// property of a TOKEN, so a seat holding a two-project credential may be given
// work in either, and checking the agent's own project would refuse it.
func TestASeatReachingTheProjectMayBeHandedTheWork(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "reachok")
	elsewhere := declaredProject(t, ctx, db, "reachelse")

	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	twoproject := "twoproject-" + ulid.Short()
	worker := &User{ID: "u-" + ulid.NewString(), Handle: twoproject}
	if err := db.InsertUser(ctx, worker); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// Acts in elsewhere, reaches here as well.
	if err := db.InsertToken(ctx, &Principal{
		Token: "t-" + ulid.NewString(), UserID: worker.ID,
		Project: elsewhere, Projects: []string{here},
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}

	todo := todoIn(t, ctx, db, author, "work they can reach", VisibilityProjectOnly, "")
	if _, _, err := db.AssignTodo(ctx, author, todo.ID, twoproject, nil); err != nil {
		t.Fatalf("a seat whose credential reaches the project was refused: %v", err)
	}
}

// AND "I CANNOT SAY" IS NOT "NO".
//
// A party with no credential at all is the ordinary case for a person who has
// not been given one, and for every handle on a board that predates
// credentials. Refusing there would break assignment for a defect nobody has -
// it is GatingAt's rule and BlockedAt's rule, that an absent measurement reads
// as nothing to say.
func TestASeatWithNoCredentialIsNotRefused(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "reachnone")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	// A person with a handle and no token at all.
	uncredentialed := "uncredentialed-" + ulid.Short()
	nobody := &User{ID: "u-" + ulid.NewString(), Handle: uncredentialed}
	if err := db.InsertUser(ctx, nobody); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	todo := todoIn(t, ctx, db, author, "handed to somebody with no token", VisibilityProjectOnly, "")
	if _, _, err := db.AssignTodo(ctx, author, todo.ID, uncredentialed, nil); err != nil {
		t.Fatalf("a seat with no credential was refused: %v", err)
	}

	// A name nothing answers to is the same answer, for the same reason.
	other := todoIn(t, ctx, db, author, "handed to a name nobody has", VisibilityProjectOnly, "")
	if _, _, err := db.AssignTodo(ctx, author, other.ID, "no-such-handle", nil); err != nil {
		t.Fatalf("an unresolvable name was refused: %v", err)
	}

	// And putting a row down names nobody, which has no reach to check.
	//
	// ON ITS OWN ROW, because the arm above left `other` carried by
	// no-such-handle and a held row is refused to anybody but its holder - the
	// first version of this test released the row it had just assigned and was
	// refused by that guard, correctly, for a reason that has nothing to do
	// with reach.
	down := todoIn(t, ctx, db, author, "put down again", VisibilityProjectOnly, "")
	if _, _, err := db.AssignTodo(ctx, author, down.ID, "nobody", nil); err != nil {
		t.Fatalf("releasing a row was refused: %v", err)
	}
}
