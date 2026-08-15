package agentfs

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The two rules a path decides, tested without a database in front of them,
// because they are the two that must not be got wrong: who may write where, and
// what scope a file lands at.

func testFS(user, project string) *FS {
	return &FS{p: &store.Principal{UserID: user, Project: project}}
}

func ptr(s string) *string { return &s }

func TestYouWriteInYourOwnDirectoryAndYourOwnProject(t *testing.T) {
	f := testFS("alice", "pa")

	yes := []store.FSScope{
		{Project: nil, Owner: "alice"},       // the personal floor is always yours
		{Project: ptr("pa"), Owner: "alice"}, // and the project your token is for
	}
	for _, s := range yes {
		if !f.writable(s) {
			t.Errorf("writable(%+v) = false, want true", s)
		}
	}

	no := []store.FSScope{
		{Project: nil, Owner: "bob"},         // somebody else's personal floor
		{Project: ptr("pa"), Owner: "bob"},   // somebody else's memory in your project
		{Project: ptr("pb"), Owner: "alice"}, // your memory in a project you only read
		{Project: ptr(""), Owner: "alice"},   // a project that is not a project
	}
	for _, s := range no {
		if f.writable(s) {
			t.Errorf("writable(%+v) = true, want false", s)
		}
	}

	// A token with no user owns nothing and writes nowhere.
	if (&FS{p: &store.Principal{Project: "pa"}}).writable(store.FSScope{Owner: ""}) {
		t.Error("a principal with no user was allowed to write")
	}
}

func TestThePathDecidesTheScopeAndTheHeaderCannotArgue(t *testing.T) {
	f := testFS("alice", "pa")
	personal := store.FSScope{Project: nil, Owner: "alice", Type: "memory"}
	inProject := store.FSScope{Project: ptr("pa"), Owner: "alice", Type: "memory"}

	// The floor. A new file under _personal is personal whatever else is true.
	if got, err := f.visibilityFor(personal, "", nil); err != nil || got != store.VisibilityPersonal {
		t.Fatalf("a new personal file is %q, %v", got, err)
	}
	if got, err := f.visibilityFor(personal, "personal", nil); err != nil || got != store.VisibilityPersonal {
		t.Fatalf("a personal file saying personal is %q, %v", got, err)
	}

	// And it cannot be promoted by saying so in the file. This is the
	// mem_write rule: personal does not become project as a side effect of a
	// write, and a header that asked for it is refused rather than ignored.
	for _, scope := range []string{"project", "shared"} {
		got, err := f.visibilityFor(personal, scope, nil)
		if !errors.Is(err, errRefused) {
			t.Fatalf("a personal file asking for %s gave %q, %v; want it refused", scope, got, err)
		}
	}

	// Inside a project the header chooses between the two scopes that are that
	// project, and the narrower one is the default.
	if got, err := f.visibilityFor(inProject, "", nil); err != nil || got != store.VisibilityProjectOnly {
		t.Fatalf("a new project file is %q, %v; want project-only", got, err)
	}
	if got, err := f.visibilityFor(inProject, "project", nil); err != nil || got != store.VisibilityProjectOnly {
		t.Fatalf("a project file saying project is %q, %v", got, err)
	}
	if got, err := f.visibilityFor(inProject, "shared", nil); err != nil || got != store.VisibilityShared {
		t.Fatalf("a project file saying shared is %q, %v", got, err)
	}
	// The other direction is refused too: a save does not take a row out of the
	// project it lives in.
	if _, err := f.visibilityFor(inProject, "personal", nil); !errors.Is(err, errRefused) {
		t.Fatalf("a project file asking for personal was not refused: %v", err)
	}

	// An edit that says nothing about scope keeps the scope the row has - empty
	// means "leave the column alone", which is what mem_write does with an
	// update that names no scope.
	held := &store.Artifact{Visibility: store.VisibilityShared}
	if got, err := f.visibilityFor(inProject, "", held); err != nil || got != "" {
		t.Fatalf("an edit with no scope is %q, %v; want the column left alone", got, err)
	}

	// A scope that is not a scope is refused, not defaulted.
	if _, err := f.visibilityFor(inProject, "publik", nil); err == nil {
		t.Fatal("a misspelled scope was accepted")
	}
}

func TestAKindIsOneOfTheKinds(t *testing.T) {
	if got, err := kindFor("memory", ""); err != nil || got != "note" {
		t.Errorf("a memory item with no kind is %q, %v; want note", got, err)
	}
	for _, kind := range []string{"note", "todo", "feature", "handoff"} {
		if got, err := kindFor("memory", kind); err != nil || got != kind {
			t.Errorf("kind %s came back as %q, %v", kind, got, err)
		}
	}
	if _, err := kindFor("memory", "banana"); err == nil {
		t.Error("a kind that is not a kind was accepted")
	}
	// A note is a note: only memory is narrowed by kind, and a note file that
	// says otherwise is not given one.
	if got, err := kindFor("note", "todo"); err != nil || got != "" {
		t.Errorf("a note took the kind %q, %v", got, err)
	}
}
