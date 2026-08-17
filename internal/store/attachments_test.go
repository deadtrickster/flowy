package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// attachmentPayload is deliberately not ASCII.
//
// A NUL byte is what a text column cannot hold at all - Postgres refuses one in
// a text value - and a newline is what a line-oriented path is most likely to
// have eaten by the time anyone notices. 0xff is not valid UTF-8, so a route
// that decodes to a string and back gets it wrong here and nowhere else. A
// fixture written in ASCII passes every one of those.
var attachmentPayload = []byte{
	'B', 'U', 'I', 'L', 'D', ' ', 'l', 'o', 'g', '\n',
	0x00,
	'p', 'a', 'n', 'i', 'c', ':', ' ', 'n', 'i', 'l', '\n',
	0xff, 0xfe, 0x00, 0x0d, 0x0a,
	'e', 'n', 'd',
}

// TestAttachmentBytesRoundTripExactly is the claim the whole surface rests on:
// what came out is what went in, byte for byte, and not "the same text".
func TestAttachmentBytesRoundTripExactly(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "att")

	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	art := &Artifact{
		Type: "attachment", Kind: "binary", Title: "the build log",
		Project: &project, OwnerUser: owner.ID, Visibility: VisibilityProjectOnly,
	}
	if err := db.WriteAttachment(ctx, art, attachmentPayload, &Event{
		Type: "attachment.write", Room: "attachments", Actor: owner.ID, Body: art.Title,
	}); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	got, content, err := db.ReadAttachment(ctx, p, art.ID)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if !bytes.Equal(content, attachmentPayload) {
		t.Fatalf("the bytes came back as %q, want %q", content, attachmentPayload)
	}
	if len(content) != len(attachmentPayload) {
		t.Fatalf("the bytes came back %d long, want %d", len(content), len(attachmentPayload))
	}
	if got.Type != "attachment" || got.Title != "the build log" {
		t.Errorf("the artifact came back as %s %q", got.Type, got.Title)
	}

	// The write is two rows and an entry in the log, like every other write
	// here: an attachment nothing in the log records is one no peer and no
	// timeline ever hears about.
	events, err := db.ListEvents(ctx, p, EventQuery{Type: "attachment.write", Limit: 200})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var wrote *Event
	for _, e := range events {
		if e.Artifact == art.ID {
			wrote = e
		}
	}
	if wrote == nil {
		t.Fatalf("nothing in the log records the attachment being written")
	}
	if wrote.SeqHLC != art.HLC {
		t.Errorf("the entry is at reading %d and the row at %d, want one reading for one write",
			wrote.SeqHLC, art.HLC)
	}
}

// TestAnAttachmentIsNotReadableByTheWrongPrincipal asserts what the other
// principal GETS, in both directions a caller can ask: by id, and in the list.
//
// The bytes are a new read path onto rows that already have a read rule, and a
// new path is exactly where that rule gets re-implemented by hand and drifts.
// So this is not a test that the code calls the filter - it is a test that a
// stranger holding the id is told the same thing they would be told about a row
// that was never written.
func TestAnAttachmentIsNotReadableByTheWrongPrincipal(t *testing.T) {
	ctx, db := open(t)
	mine := declaredProject(t, ctx, db, "att-mine")
	theirs := declaredProject(t, ctx, db, "att-theirs")

	owner := &User{Handle: "owner-" + ulid.NewString()}
	stranger := &User{Handle: "stranger-" + ulid.NewString()}
	for _, u := range []*User{owner, stranger} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	art := &Artifact{
		Type: "attachment", Kind: "text", Title: "a capture of my own",
		Project: &mine, OwnerUser: owner.ID, Visibility: VisibilityProjectOnly,
	}
	if err := db.WriteAttachment(ctx, art, attachmentPayload, &Event{
		Type: "attachment.write", Room: "attachments", Actor: owner.ID, Body: art.Title,
	}); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	them := &Principal{UserID: stranger.ID, Project: theirs}
	gotArt, content, err := db.ReadAttachment(ctx, them, art.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger reading the attachment got (%v, %d bytes, %v), want ErrNotFound",
			gotArt, len(content), err)
	}
	if content != nil {
		t.Fatalf("the refusal handed over %d bytes", len(content))
	}

	list, err := db.ListArtifacts(ctx, them, ArtifactQuery{Type: "attachment"})
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	for _, a := range list {
		if a.ID == art.ID {
			t.Fatalf("the attachment is in a stranger's list: %s", a.ID)
		}
	}

	// And the other half, so this is a permission check rather than a check
	// that nobody can read anything: the owner gets the bytes.
	_, content, err = db.ReadAttachment(ctx, &Principal{UserID: owner.ID, Project: mine}, art.ID)
	if err != nil {
		t.Fatalf("the owner cannot read their own attachment: %v", err)
	}
	if !bytes.Equal(content, attachmentPayload) {
		t.Fatalf("the owner got %q", content)
	}
}

// TestAnAttachmentRowWithNoBytesSaysSo is the case the fabric will produce: the
// artifact replicates to a peer and the payload does not. The reader is a
// principal the filter has already let through, so they are told what is
// actually wrong rather than being handed an empty file to debug against.
func TestAnAttachmentRowWithNoBytesSaysSo(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "att-nobytes")

	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Written as an ordinary artifact, which is what a pulled row is: the type
	// is 'attachment' and there is no attachment_bytes row behind it.
	art := &Artifact{
		Type: "attachment", Kind: "text", Title: "pulled from somewhere else",
		Project: &project, OwnerUser: owner.ID, Visibility: VisibilityProjectOnly,
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	got, content, err := db.ReadAttachment(ctx, &Principal{UserID: owner.ID, Project: project}, art.ID)
	if !errors.Is(err, ErrNoBytes) {
		t.Fatalf("reading a bytesless attachment gave %v, want ErrNoBytes", err)
	}
	if content != nil {
		t.Fatalf("it handed over %d bytes", len(content))
	}
	if got == nil || got.ID != art.ID {
		t.Fatalf("it did not hand back the artifact it found")
	}
}
