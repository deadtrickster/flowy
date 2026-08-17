package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestWhatReplacedAReportIsFilteredLikeTheReportItself holds the claim the
// reverse of supersedes rests on, in the query that implements it.
//
// supersedes is written on the newer document and points backwards. The
// reader's question is the other one - has this been replaced - and answering
// it means finding the report whose supersedes names this one. That is a read,
// and its answer is another artifact's id, so the interesting case is not the
// one where it works: it is the one where the replacement is out of reach.
//
// Three claims, and the middle one is the whole reason this is in the store
// rather than in a handler.
//
//	the owner is told which report replaced theirs
//	a reader who cannot reach the replacement is told nothing at all - not the
//	  id, and not that there is one, because "there is a newer one you may not
//	  see" is itself a disclosure about a row they were refused
//	the replacement is not itself marked, so a mark means what it says
//
// Both principals are in the same project and the old report is project-scoped,
// so what separates them is exactly one thing: the replacement is personal to
// its author. A derivation that ran outside ArtifactFilterSQL would pass the
// first and third and hand the reader the id in the second.
func TestWhatReplacedAReportIsFilteredLikeTheReportItself(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rep")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	reader := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	write := func(title, visibility, supersedes string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: "report", OwnerUser: author.UserID,
			Title: title, Body: "measured off the sill at the tailrace mouth",
			Visibility: visibility, Project: &project,
		}
		if visibility == VisibilityPersonal {
			// A personal artifact has no project - that is the floor, and a row
			// that kept one would be read by the project branch instead.
			art.Project = nil
		}
		if supersedes != "" {
			fields, err := json.Marshal(map[string]any{SupersedesField: supersedes})
			if err != nil {
				t.Fatalf("fields: %v", err)
			}
			art.Fields = fields
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return art
	}

	old := write("the tailrace silt survey", VisibilityProjectOnly, "")
	replacement := write("the tailrace silt survey, remeasured", VisibilityPersonal, old.ID)

	// mark is what one principal is told replaced the report with this id, read
	// back through each of the three doors that fill it in. They are asserted
	// together because a filter that is right on one of them and missing on
	// another is the shape this whole thing is guarding against.
	mark := func(who *Principal, id string) (byRead, byList, bySearch string) {
		t.Helper()
		art, err := db.ReadArtifact(ctx, who, id, false)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		byRead = art.ReplacedBy

		list, err := db.ListArtifacts(ctx, who, ArtifactQuery{Type: "report"})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		byList = "absent"
		for _, found := range list {
			if found.ID == id {
				byList = found.ReplacedBy
			}
		}

		hits, err := db.SearchArtifacts(ctx, who, ArtifactQuery{Type: "report", Query: "tailrace"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		bySearch = "absent"
		for _, hit := range hits {
			if hit.ID == id {
				bySearch = hit.ReplacedBy
			}
		}
		return byRead, byList, bySearch
	}

	assert := func(what string, who *Principal, id, want string) {
		t.Helper()
		byRead, byList, bySearch := mark(who, id)
		if byRead != want || byList != want || bySearch != want {
			t.Fatalf("%s: read says %q, list says %q, search says %q, want %q everywhere",
				what, byRead, byList, bySearch, want)
		}
	}

	assert("the author reading their own report", author, old.ID, replacement.ID)
	assert("a reader who cannot see the replacement", reader, old.ID, "")

	// A replacement personal to its author is a row nobody else has a route to,
	// so the author is told which id replaced their report and given no address
	// for it. Half a reference would render as a link and land nowhere.
	if art, err := db.ReadArtifact(ctx, author, old.ID, false); err != nil {
		t.Fatalf("read: %v", err)
	} else if art.ReplacedByRef != "" {
		t.Fatalf("a personal replacement was addressed as %q, and it has no project to address",
			art.ReplacedByRef)
	}

	// And the replacement carries no mark of its own: nothing supersedes it.
	if art, err := db.ReadArtifact(ctx, author, replacement.ID, false); err != nil {
		t.Fatalf("read the replacement: %v", err)
	} else if art.ReplacedBy != "" {
		t.Fatalf("the replacement says it was itself replaced by %q", art.ReplacedBy)
	}

	// The reader cannot reach the replacement at all, which is what makes the
	// silence above a floor rather than a coincidence of this one query.
	if _, err := db.ReadArtifact(ctx, reader, replacement.ID, false); err == nil {
		t.Fatal("the reader read a personal artifact of somebody else's")
	}
}

// TestAReportReplacedTwiceNamesTheNewestReplacement pins the tie-break.
//
// Two reports superseding one is not a shape to design for, but it is a shape
// that arrives: two seats remeasure the same thing, or a replacement is written
// twice because the first was thought lost. Whichever way it happened, the
// reader wants the end of the chain, and an answer that depended on row order
// would send half of them to a document that has itself been overtaken.
func TestAReportReplacedTwiceNamesTheNewestReplacement(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rep2")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	write := func(title, supersedes string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: "report", OwnerUser: author.UserID,
			Title: title, Body: "the mill race bearing, again", Visibility: VisibilityProjectOnly,
			Project: &project,
		}
		if supersedes != "" {
			fields, err := json.Marshal(map[string]any{SupersedesField: supersedes})
			if err != nil {
				t.Fatalf("fields: %v", err)
			}
			art.Fields = fields
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return art
	}

	old := write("the mill race bearing survey", "")
	write("remeasured once", old.ID)
	// Bump the second replacement's updated stamp past the first's, which is
	// what "newest" is decided on. Two upserts in the same statement-timestamp
	// would otherwise be a coin toss.
	newest := write("remeasured again", old.ID)
	touch(t, ctx, db, newest.ID)

	art, err := db.ReadArtifact(ctx, author, old.ID, false)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if art.ReplacedBy != newest.ID {
		t.Fatalf("replaced by %q, want the newest replacement %q", art.ReplacedBy, newest.ID)
	}
}

// TestWhatReplacedAReportIsAddressedByItsOwnProjectAndType holds the claim
// that makes replaced_by usable: the address comes off the REPLACEMENT.
//
// A supersedes chain is not held to one project or to one artifact type -
// nothing here enforces either, and a design replaced by a decision, or a
// report remeasured by a team that keeps its documents in its own project, are
// both ordinary. A reader holding only the id has to invent the other two
// segments, and the only ones it has are the ones on the row it is already
// looking at, which is precisely the wrong row. That guess is right often
// enough to look correct and silently wrong the rest of the time, so the case
// worth pinning is the one where every segment differs.
func TestWhatReplacedAReportIsAddressedByItsOwnProjectAndType(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "rep-here")
	elsewhere := declaredProject(t, ctx, db, "rep-elsewhere")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	write := func(project *string, typ, title, supersedes string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: typ, OwnerUser: author.UserID,
			Title: title, Body: "the weir crest, before and after the rebuild",
			Visibility: VisibilityProject, Project: project,
		}
		if supersedes != "" {
			fields, err := json.Marshal(map[string]any{SupersedesField: supersedes})
			if err != nil {
				t.Fatalf("fields: %v", err)
			}
			art.Fields = fields
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return art
	}

	old := write(&here, "report", "the weir crest survey", "")
	replacement := write(&elsewhere, "memory", "what we settled on for the weir crest", old.ID)

	// One live edge, so the replacement is readable at all: without it this
	// would be testing the filter again rather than the address.
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: here, ToProject: elsewhere, Cap: CapRead, GrantedBy: author.UserID,
	}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	art, err := db.ReadArtifact(ctx, author, old.ID, false)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if art.ReplacedBy != replacement.ID {
		t.Fatalf("replaced by %q, want %q", art.ReplacedBy, replacement.ID)
	}
	want := elsewhere + "/memory/" + replacement.ID
	if art.ReplacedByRef != want {
		t.Fatalf("addressed as %q, want %q - the reference is built from the replacement, "+
			"not from the report being read (%s/report/...)", art.ReplacedByRef, want, here)
	}
}

// touch moves an artifact's updated stamp forward without going through a write
// path, so a test can order two rows that were written in the same instant.
func touch(t *testing.T, ctx context.Context, db *DB, id string) {
	t.Helper()
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE artifacts SET updated = now() + interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatalf("touch %s: %v", id, err)
	}
}
