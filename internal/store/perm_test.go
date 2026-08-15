package store

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestCanReadMatchesSQL is the check that keeps the two halves of the
// permission rule honest. CanRead decides in Go and ArtifactFilterSQL decides in
// the database, and every list and search in the node uses the second one - so
// if they ever disagree, the reviewed predicate is not the one that runs.
//
// It walks the whole matrix of principals against artifacts, in both directions
// across a project boundary, with and without a grant.
func TestCanReadMatchesSQL(t *testing.T) {
	ctx, db := open(t)

	// Fresh project names, so the rows other tests leave behind cannot make
	// this one pass or fail by accident.
	px := "px-" + ulid.NewString()
	py := "py-" + ulid.NewString()

	alice := &User{Handle: "alice-" + ulid.NewString()}
	bob := &User{Handle: "bob-" + ulid.NewString()}
	for _, u := range []*User{alice, bob} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	aliceAgent := &Agent{UserID: alice.ID, Kind: "claude", Project: px}
	if err := db.InsertAgent(ctx, aliceAgent); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	mk := func(project *string, visibility, owner string) *Artifact {
		a := &Artifact{
			Type: "note", Project: project, OwnerUser: owner,
			Title: "t", Body: "b", Visibility: visibility,
		}
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("upsert artifact: %v", err)
		}
		return a
	}
	inPX := mk(&px, "project", alice.ID)
	sharedPX := mk(&px, "project", alice.ID)
	inPY := mk(&py, "project", bob.ID)
	personalAlice := mk(nil, "personal", alice.ID)
	personalBob := mk(nil, "personal", bob.ID)
	// The second floor: px and nobody else, whatever grants say. It is what the
	// memory tools' `project` scope has always claimed to be.
	onlyPX := mk(&px, VisibilityProjectOnly, alice.ID)

	// bob's project may read alice's, and one artifact of alice's is shared to
	// bob by name - including, on purpose, the project-only one, which neither
	// the grant nor the share reaches.
	grants := []*Grant{
		{FromProject: py, ToProject: px, GrantedBy: alice.ID},
		{Artifact: sharedPX.ID, Subject: bob.ID, ToProject: px, GrantedBy: alice.ID},
		{Artifact: onlyPX.ID, Subject: bob.ID, ToProject: px, GrantedBy: alice.ID},
		// A tombstoned grant must count for nothing.
		{FromProject: px, ToProject: py, GrantedBy: bob.ID, Tombstone: true},
	}
	for _, g := range grants {
		if err := db.InsertGrant(ctx, g); err != nil {
			t.Fatalf("insert grant: %v", err)
		}
	}

	principals := map[string]*Principal{
		"alice in px":       {UserID: alice.ID, Project: px},
		"alice's agent":     {UserID: alice.ID, AgentID: aliceAgent.ID, Project: px},
		"bob in py":         {UserID: bob.ID, Project: py},
		"stranger in pz":    {UserID: ulid.NewString(), Project: "pz-" + ulid.NewString()},
		"projectless alice": {UserID: alice.ID},
	}
	artifacts := map[string]*Artifact{
		"alice's px note":      inPX,
		"alice's shared px":    sharedPX,
		"alice's px-only note": onlyPX,
		"bob's py note":        inPY,
		"alice's personal":     personalAlice,
		"bob's personal":       personalBob,
	}

	// What the rules say, spelled out rather than derived, so a change to
	// CanRead cannot quietly rewrite the expectation too.
	want := map[string]map[string]bool{
		"alice in px": {
			"alice's px note": true, "alice's shared px": true, "alice's px-only note": true,
			"bob's py note": false, "alice's personal": true, "bob's personal": false,
		},
		"alice's agent": {
			"alice's px note": true, "alice's shared px": true, "alice's px-only note": true,
			"bob's py note": false, "alice's personal": true, "bob's personal": false,
		},
		"bob in py": {
			// The py -> px grant reaches both px notes, and the personal floor
			// stops it dead at alice's note even though the grant exists. The
			// px-only note is the second floor: bob holds a share of that one
			// by name and still does not reach it.
			"alice's px note": true, "alice's shared px": true, "alice's px-only note": false,
			"bob's py note": true, "alice's personal": false, "bob's personal": true,
		},
		"stranger in pz": {
			"alice's px note": false, "alice's shared px": false, "alice's px-only note": false,
			"bob's py note": false, "alice's personal": false, "bob's personal": false,
		},
		"projectless alice": {
			// No home project, so only what alice owns personally.
			"alice's px note": false, "alice's shared px": false, "alice's px-only note": false,
			"bob's py note": false, "alice's personal": true, "bob's personal": false,
		},
	}

	for pName, p := range principals {
		for aName, art := range artifacts {
			expected := want[pName][aName]

			relevant, err := db.GrantsFor(ctx, art)
			if err != nil {
				t.Fatalf("grants for %s: %v", aName, err)
			}
			if got := CanRead(p, art, relevant); got != expected {
				t.Errorf("CanRead(%s, %s) = %v, want %v", pName, aName, got, expected)
			}

			_, err = db.ReadArtifact(ctx, p, art.ID, false)
			switch {
			case err == nil && !expected:
				t.Errorf("SQL filter let %s read %s", pName, aName)
			case errors.Is(err, ErrNotFound) && expected:
				t.Errorf("SQL filter hid %s from %s", aName, pName)
			case err != nil && !errors.Is(err, ErrNotFound):
				t.Fatalf("read %s as %s: %v", aName, pName, err)
			}
		}
	}
}

// TestEventFilterInheritsTheArtifactFloor is TestCanReadMatchesSQL's other
// half: the two read surfaces over the same artifact have to agree.
//
// A project-wide grant into px let a principal of py read every event in px,
// with no join to artifacts and no visibility test at all - so the artifact
// behind the personal or project-only floor was refused row by row and handed
// over event by event: the chat about it, its status trail, the forge entries
// naming it, bodies and meta included, over /api/events, the inbox, a room read
// and a replication pull. The share branch has carried the floor since it was
// written; this is the same floor on the branch beside it.
//
// The widening the grant is actually for still works: an event that names no
// artifact is project chatter, and it stays readable across the edge.
func TestEventFilterInheritsTheArtifactFloor(t *testing.T) {
	ctx, db := open(t)

	px := "floorpx-" + ulid.NewString()
	py := "floorpy-" + ulid.NewString()

	alice := &User{Handle: "ev-alice-" + ulid.NewString()}
	bob := &User{Handle: "ev-bob-" + ulid.NewString()}
	for _, u := range []*User{alice, bob} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	mk := func(project *string, visibility string) *Artifact {
		a := &Artifact{
			Type: "note", Project: project, OwnerUser: alice.ID,
			Title: "t", Body: "b", Visibility: visibility,
		}
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("upsert artifact: %v", err)
		}
		return a
	}
	openNote := mk(&px, VisibilityProject)
	onlyPX := mk(&px, VisibilityProjectOnly)
	personal := mk(nil, VisibilityPersonal)

	// One live project-wide edge: py may read px. Nothing else.
	if err := db.InsertGrant(ctx, &Grant{FromProject: py, ToProject: px, GrantedBy: alice.ID}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	mkEvent := func(artifact, body string) *Event {
		home := px
		e := &Event{
			Type: "chat", Project: &home, Room: px + "/general",
			Actor: alice.ID, Artifact: artifact, Body: body,
		}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
		return e
	}
	events := map[string]*Event{
		"about the open note":      mkEvent(openNote.ID, "readable, and about a readable artifact"),
		"about the px-only note":   mkEvent(onlyPX.ID, "the design nobody outside px may see"),
		"about the personal note":  mkEvent(personal.ID, "what alice keeps to herself"),
		"about nothing in the row": mkEvent("", "project chatter, which the grant is for"),
	}
	artifactOf := map[string]*Artifact{
		"about the open note":     openNote,
		"about the px-only note":  onlyPX,
		"about the personal note": personal,
	}

	reader := &Principal{UserID: bob.ID, Project: py}
	want := map[string]bool{
		"about the open note":      true,
		"about the px-only note":   false,
		"about the personal note":  false,
		"about nothing in the row": true,
	}

	for name, e := range events {
		_, err := db.ReadEvent(ctx, reader, e.ID)
		switch {
		case err == nil && !want[name]:
			t.Errorf("the event filter let a py principal read the event %s", name)
		case errors.Is(err, ErrNotFound) && want[name]:
			t.Errorf("the event filter hid the event %s from a py principal", name)
		case err != nil && !errors.Is(err, ErrNotFound):
			t.Fatalf("read event %s: %v", name, err)
		}

		// And where the event names an artifact, the two surfaces agree: the
		// event is readable exactly when the row is.
		if art, ok := artifactOf[name]; ok {
			_, rowErr := db.ReadArtifact(ctx, reader, art.ID, false)
			rowOK := rowErr == nil
			if rowErr != nil && !errors.Is(rowErr, ErrNotFound) {
				t.Fatalf("read artifact for %s: %v", name, rowErr)
			}
			if rowOK != want[name] {
				t.Errorf("the row for %s reads %v and the event reads %v: the two disagree",
					name, rowOK, want[name])
			}
		}
	}

	// The list path is the same filter, and it is where a leak actually goes
	// out: /api/events, the inbox, a room read.
	listed, err := db.ListEvents(ctx, reader, EventQuery{Room: px + "/general"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	got := map[string]bool{}
	for _, e := range listed {
		got[e.ID] = true
	}
	for name, e := range events {
		if got[e.ID] != want[name] {
			t.Errorf("the room read %s the event %s, want %v", listedAs(got[e.ID]), name, want[name])
		}
	}

	// Nothing narrowed for the project the events are in: alice reads all four.
	owner := &Principal{UserID: alice.ID, Project: px}
	for name, e := range events {
		if _, err := db.ReadEvent(ctx, owner, e.ID); err != nil {
			t.Errorf("px could not read its own event %s: %v", name, err)
		}
	}
}

// listedAs is TestEventFilterInheritsTheArtifactFloor's failure wording.
func listedAs(in bool) string {
	if in {
		return "returned"
	}
	return "omitted"
}

// TestScopeAllIsOperatorOnly pins the escape hatch shut for everybody else.
func TestScopeAllIsOperatorOnly(t *testing.T) {
	ctx, db := open(t)

	project := "scoped-" + ulid.NewString()
	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	art := &Artifact{Type: "note", Project: &project, OwnerUser: owner.ID, Title: "hidden"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	outsider := &Principal{UserID: ulid.NewString(), Project: "elsewhere-" + ulid.NewString()}
	if _, err := db.ReadArtifact(ctx, outsider, art.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scope=all worked for a non-operator: %v", err)
	}

	operator := &Principal{UserID: outsider.UserID, Project: outsider.Project, Operator: true}
	if _, err := db.ReadArtifact(ctx, operator, art.ID, true); err != nil {
		t.Fatalf("scope=all failed for the operator: %v", err)
	}
	// Without asking for it, the operator is bound like anyone else.
	if _, err := db.ReadArtifact(ctx, operator, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the operator read across a boundary without scope=all: %v", err)
	}
}

// TestPersonalFloorSurvivesEveryGrant is the one rule that has no exception: a
// personal artifact is its owner's, and no capability reaches it.
func TestPersonalFloorSurvivesEveryGrant(t *testing.T) {
	ctx, db := open(t)

	px := "floor-a-" + ulid.NewString()
	py := "floor-b-" + ulid.NewString()
	owner := &User{Handle: "floor-owner-" + ulid.NewString()}
	other := &User{Handle: "floor-other-" + ulid.NewString()}
	for _, u := range []*User{owner, other} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	note := &Artifact{Type: "note", OwnerUser: owner.ID, Visibility: "personal", Title: "mine"}
	if err := db.UpsertArtifact(ctx, note); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if note.Project != nil {
		t.Fatalf("a personal artifact kept a project: %q", *note.Project)
	}

	// Every grant anyone could think to write, pointed at it.
	for _, g := range []*Grant{
		{FromProject: py, ToProject: px, GrantedBy: owner.ID},
		{Artifact: note.ID, Subject: other.ID, ToProject: px, GrantedBy: owner.ID},
		{FromProject: py, ToProject: "", GrantedBy: owner.ID},
	} {
		if err := db.InsertGrant(ctx, g); err != nil {
			t.Fatalf("insert grant: %v", err)
		}
	}

	intruder := &Principal{UserID: other.ID, Project: py}
	relevant, err := db.GrantsFor(ctx, note)
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if CanRead(intruder, note, relevant) {
		t.Fatal("CanRead let a grant reach a personal artifact")
	}
	if _, err := db.ReadArtifact(ctx, intruder, note.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the SQL filter let a grant reach a personal artifact: %v", err)
	}
	if _, err := db.ReadArtifact(ctx, &Principal{UserID: owner.ID}, note.ID, false); err != nil {
		t.Fatalf("the owner could not read their own personal artifact: %v", err)
	}
}

// TestSearchFindsDiscoveryOnlyWords is why discovery is in the search vector: an
// agent's finding is often the only place a word appears.
func TestSearchFindsDiscoveryOnlyWords(t *testing.T) {
	ctx, db := open(t)

	project := "search-" + ulid.NewString()
	owner := &User{Handle: "searcher-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// A word that cannot be anywhere else in the database.
	word := "kryptobanana"
	art := &Artifact{
		Type: "bug", Project: &project, OwnerUser: owner.ID,
		Title: "a title with none of it", Body: "a body with none of it",
		Discovery: "the fault is a " + word + " in the parser",
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	p := &Principal{UserID: owner.ID, Project: project}
	hits, err := db.SearchArtifacts(ctx, p, ArtifactQuery{Query: word})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != art.ID {
		t.Fatalf("search for %q returned %d hits, want just %s", word, len(hits), art.ID)
	}
	if hits[0].Rank <= 0 {
		t.Fatalf("hit came back with rank %v, want a positive score", hits[0].Rank)
	}

	// A stranger searching the same word finds nothing at all: the filter is in
	// the same WHERE clause as the match.
	stranger := &Principal{UserID: ulid.NewString(), Project: "nowhere-" + ulid.NewString()}
	hits, err = db.SearchArtifacts(ctx, stranger, ArtifactQuery{Query: word})
	if err != nil {
		t.Fatalf("search as a stranger: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a stranger's search returned %d hits, want none", len(hits))
	}

	// And a tombstone takes it out of both the list and the search.
	if _, err := db.TombstoneArtifact(ctx, p, art.ID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	hits, err = db.SearchArtifacts(ctx, p, ArtifactQuery{Query: word})
	if err != nil {
		t.Fatalf("search after tombstone: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a tombstoned artifact still turns up in search: %d hits", len(hits))
	}
	list, err := db.ListArtifacts(ctx, p, ArtifactQuery{Project: project})
	if err != nil {
		t.Fatalf("list after tombstone: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a tombstoned artifact still turns up in the list: %d rows", len(list))
	}
}
