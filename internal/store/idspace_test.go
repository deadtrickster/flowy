package store

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// raiseInRoom is what POST /api/chat/{room}/todo writes when a conversation
// turns into a plan: the remark that started it, then the row and the one chat
// message announcing it, all in that room's thread.
func raiseInRoom(
	t *testing.T, ctx context.Context, db *DB, p *Principal, title string,
) (*Artifact, *Event) {
	t.Helper()

	project := p.Project
	// The remark the todo came out of goes in FIRST, in the same thread, naming
	// no row. That is what a room is, and it is also the trap: a message about
	// nothing is stored with an EMPTY artifact rather than a null one, so a
	// lookup that asked which message in the thread names a row and sorted on
	// nullness alone would hand back this one and name nothing at all.
	said := &Event{
		Type: ChatEventType, Project: &project, Room: "build",
		Thread: ulid.NewString(), Actor: p.UserID,
		Body: "the gearbox needs a bench test before we ship it",
	}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("say: %v", err)
	}

	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo",
		Project: &project, OwnerUser: p.UserID, Title: title, Status: "todo",
		Visibility: VisibilityProjectOnly,
	}
	e := &Event{
		Type: ChatEventType, Room: "build", Thread: said.Thread,
		Parents: []string{said.ID}, Actor: p.UserID,
		Body: "raised a todo " + art.ID + ": " + title,
	}
	if err := db.WriteMemory(ctx, art, e); err != nil {
		t.Fatalf("raise %q: %v", title, err)
	}
	return art, e
}

// THE ONE THAT MATTERS. A reader holding the thread id out of a raise
// notification is told it is a thread id, and told which row was raised in it.
//
// Before this, every row door answered that id with a bare 404, which reads as
// "the row is gone" and sent two agents looking for a deleted artifact on
// 2026-08-18 - one of them after reading the other's report of the same
// mistake. The two ids differed in their last character.
func TestAThreadIdOutOfARaiseSaysWhichRowWasRaisedInIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "idspace")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	art, e := raiseInRoom(t, ctx, db, author, "a raise notification carries the thread id")

	// The premise: the id that reads as the row's is not the row's.
	if _, err := db.ReadArtifact(ctx, author, e.Thread, false); err == nil {
		t.Fatal("a thread id read back as an artifact, so there is nothing here to diagnose")
	}

	misread, err := db.MisreadArtifactID(ctx, author, e.Thread)
	if err != nil {
		t.Fatalf("diagnose the thread id: %v", err)
	}
	if misread == nil {
		t.Fatal("a thread id this reader can read was diagnosed as nothing at all")
	}
	if misread.Space != IDSpaceThread {
		t.Fatalf("a thread id was called a %q", misread.Space)
	}
	// AND IT NAMES THE ROW. Saying "that is a thread" and stopping leaves the
	// reader exactly where the 404 left them: holding a wrong id and no right
	// one.
	if misread.Artifact != art.ID {
		t.Fatalf("the thread names row %q, and the raise wrote %q", misread.Artifact, art.ID)
	}

	// The message's own id is the other ULID a reader has in hand, and it is a
	// third thing again - so it is named as what it is rather than as a thread.
	byMessage, err := db.MisreadArtifactID(ctx, author, e.ID)
	if err != nil {
		t.Fatalf("diagnose the message id: %v", err)
	}
	if byMessage == nil || byMessage.Space != IDSpaceMessage || byMessage.Artifact != art.ID {
		t.Fatalf("the announcement's own id was diagnosed as %+v", byMessage)
	}

	// The remark in the middle of the same conversation is a message about
	// nothing, and it is told so without being handed the row: a row raised
	// further down a thread is not what an earlier message is about, and
	// offering it would be a guess dressed as an answer.
	said := e.Parents[0]
	byRemark, err := db.MisreadArtifactID(ctx, author, said)
	if err != nil {
		t.Fatalf("diagnose the remark: %v", err)
	}
	if byRemark == nil || byRemark.Space != IDSpaceMessage || byRemark.Artifact != "" {
		t.Fatalf("a message about nothing was diagnosed as %+v", byRemark)
	}

	// And a row id is not misread at all: the diagnosis only ever runs after a
	// lookup has already missed, and one that claimed a real row was something
	// else would be worse than the 404 it replaces.
	if m, err := db.MisreadArtifactID(ctx, author, art.ID); err != nil || m != nil {
		t.Fatalf("the row's own id was diagnosed as %+v (err %v)", m, err)
	}
}

// AND IT IS NOT AN EXISTENCE ORACLE. The diagnosis is read through the asking
// principal's own filter, so a reader who could not have read the thread is
// told nothing - which leaves them the bare 404 they had before, and leaves the
// id space of somebody else's project unguessable.
func TestADiagnosisTellsAStrangerNothingTheyCouldNotAlreadyRead(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "idspacehere")
	elsewhere := declaredProject(t, ctx, db, "idspaceaway")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	_, e := raiseInRoom(t, ctx, db, author, "not the stranger's business")

	misread, err := db.MisreadArtifactID(ctx, stranger, e.Thread)
	if err != nil {
		t.Fatalf("diagnose for a stranger: %v", err)
	}
	if misread != nil {
		t.Fatalf("a stranger was told the id names %+v", misread)
	}
	if m, err := db.MisreadArtifactID(ctx, stranger, e.ID); err != nil || m != nil {
		t.Fatalf("a stranger was told the message id names %+v (err %v)", m, err)
	}
	// An id nothing answers to is nothing to everybody, which is the answer that
	// keeps a 404 a 404 in the ordinary case.
	if m, err := db.MisreadArtifactID(ctx, author, ulid.NewString()); err != nil || m != nil {
		t.Fatalf("an id nothing was written under was diagnosed as %+v (err %v)", m, err)
	}
}
