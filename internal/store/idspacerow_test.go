package store

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// writeRow puts one artifact in a project, of whatever type and kind the caller
// names, visible only inside that project - so a stranger's read of it fails the
// same way a stranger's read of anything else does.
func writeRow(
	t *testing.T, ctx context.Context, db *DB, p *Principal, typ, kind, title string,
) *Artifact {
	t.Helper()
	project := p.Project
	art := &Artifact{
		ID: ulid.NewString(), Type: typ, Kind: kind,
		Project: &project, OwnerUser: p.UserID, Title: title, Status: "todo",
		Visibility: VisibilityProjectOnly,
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("write a %s: %v", typ, err)
	}
	return art
}

// A ROW THAT IS THE WRONG KIND OF ROW IS NOT A ROW THAT IS NOT THERE.
//
// 01M1PSXM9P3Q9S3VXCT7SAA00S. `flowy todo claim --id <finding>` answered
//
//	no such todo: 01M0BQ2RF98DSC2D3B73KYNG9W - searched flowy, which is what
//	this credential reads. A row in another project answers exactly this too
//
// about a row that is in flowy, that the same credential reads in full through
// GET /api/artifact/{id}, and whose only problem is being a finding. The
// sentence named the one cause that did not apply and I went and checked two
// other projects for it - the disclosure that exists to prevent a wrong
// conclusion produced one.
//
// THE ASSERTION IS A DIFFERENCE AND NOT AN ABSOLUTE. One diagnosis cannot tell
// a rule being enforced from a rule that does not exist, so the same door is
// asked twice - about an id nothing was ever written under, and about a
// readable row of another type - and the two answers have to differ. A version
// of this that diagnosed everything, or nothing, passes an absolute and fails
// this.
func TestAReadableRowOfAnotherTypeIsNotDiagnosedAsAbsent(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "idspacerow")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	finding := writeRow(t, ctx, db, author, "finding", "", "a finding, not a queue item")

	// The premise, measured rather than assumed: the reader CAN read this row.
	// Everything this test permits rests on that, so it is checked and not
	// asserted in a comment.
	if _, err := db.ReadArtifact(ctx, author, finding.ID, false); err != nil {
		t.Fatalf("the premise is wrong - the author cannot read their own finding: %v", err)
	}

	absent, err := db.MisreadArtifactID(ctx, author, ulid.NewString())
	if err != nil {
		t.Fatalf("diagnose an id nothing was written under: %v", err)
	}
	present, err := db.MisreadArtifactID(ctx, author, finding.ID)
	if err != nil {
		t.Fatalf("diagnose a readable finding: %v", err)
	}

	if absent != nil {
		t.Fatalf("an id nothing answers to was diagnosed as %+v", absent)
	}
	if present == nil {
		t.Fatal(
			"a readable finding and an id that was never written answer the same way, " +
				"so 'no such todo' still cannot be told from 'that is not a todo'",
		)
	}
	if present.Space != IDSpaceRow {
		t.Fatalf("a readable finding was diagnosed as space %q, want %q", present.Space, IDSpaceRow)
	}
	if present.What != "finding" {
		t.Fatalf("the diagnosis says the row is a %q, want %q", present.What, "finding")
	}
}

// AND A QUEUE ITEM IS NOT DIAGNOSED AT ALL, or the sentence would tell a caller
// that a todo is "not a queue item" - the same defect, wearing the label of its
// own fix. The test exists because the type check is a copy of a rule that lives
// in readWorkItem, and a copy is exactly the thing that drifts.
func TestAQueueItemIsNotDiagnosedAsTheWrongKindOfRow(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "idspacework")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := writeRow(t, ctx, db, author, MemoryType, "todo", "an ordinary row")

	m, err := db.MisreadArtifactID(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("diagnose a todo: %v", err)
	}
	if m != nil {
		t.Fatalf("a todo was diagnosed as the wrong kind of row: %+v", m)
	}
}

// THE DIRECTION THAT PROTECTS THE STORE, and the one a careless version of this
// fix fails while looking finished.
//
// store.NotATodoError collapses absent, out-of-reach and wrong-type into one
// answer on purpose, so that naming an id in a queue verb cannot be used to
// find out what that id is. This fix is allowed to speak ONLY because it speaks
// after a read the caller's own filter permitted. A stranger must therefore
// still get nothing, and get it identically for a row that exists and for an id
// that never did - which is the pair asserted here.
func TestAStrangerIsNotToldThatARowExistsAsAnotherType(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "idspacerowhere")
	elsewhere := declaredProject(t, ctx, db, "idspacerowaway")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	finding := writeRow(t, ctx, db, author, "finding", "", "none of the stranger's business")

	// The premise: the stranger genuinely cannot read it. Without this the test
	// would pass for the wrong reason on a row nobody could reach.
	if _, err := db.ReadArtifact(ctx, stranger, finding.ID, false); err == nil {
		t.Fatal("the premise is wrong - the stranger can read the finding, so nothing is hidden")
	}

	hidden, err := db.MisreadArtifactID(ctx, stranger, finding.ID)
	if err != nil {
		t.Fatalf("diagnose a stranger's read of a finding: %v", err)
	}
	if hidden != nil {
		t.Fatalf(
			"a stranger was told an id they cannot read names %+v - this door is now an existence oracle",
			hidden,
		)
	}

	never, err := db.MisreadArtifactID(ctx, stranger, ulid.NewString())
	if err != nil {
		t.Fatalf("diagnose an id that was never written: %v", err)
	}
	// The two have to be INDISTINGUISHABLE, not merely both quiet: a stranger who
	// could tell "exists, not yours" from "never existed" has the oracle back.
	if (hidden == nil) != (never == nil) {
		t.Fatalf(
			"a stranger can tell a real row from an id that never existed: real=%+v never=%+v",
			hidden, never,
		)
	}
}
