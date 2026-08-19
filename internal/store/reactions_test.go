package store

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAReactionIsAnAckAndItsRetractionIsAnEntry drives the verb end to end.
func TestAReactionIsAnAckAndItsRetractionIsAnEntry(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rx")
	me := &User{Handle: "me-" + ulid.NewString()}
	them := &User{Handle: "them-" + ulid.NewString()}
	for _, u := range []*User{me, them} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	mine := &Principal{UserID: me.ID, Project: project}
	theirs := &Principal{UserID: them.ID, Project: project}

	said := &Event{
		Type: "chat", Project: &project, Room: "general", Actor: them.ID,
		Body: "the drainer is green",
	}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("say something to react to: %v", err)
	}

	if _, err := db.React(ctx, mine, "general", said.ID, "👀", true); err != nil {
		t.Fatalf("react: %v", err)
	}
	if _, err := db.React(ctx, theirs, "general", said.ID, "👀", true); err != nil {
		t.Fatalf("their react: %v", err)
	}
	on, err := db.ReactionsOn(ctx, mine, []string{said.ID})
	if err != nil {
		t.Fatalf("read reactions: %v", err)
	}
	got := on[said.ID]
	if len(got) != 1 || got[0].Emoji != "👀" {
		t.Fatalf("the message carries %+v, want one 👀", got)
	}
	// ACTORS AND NOT A COUNT. In a room of four seats, who acked is the signal
	// and how many is the summary of it - a fold that kept only the number
	// could not answer the question the channel exists for.
	if len(got[0].Actors) != 2 {
		t.Fatalf("👀 is on it from %v, want both", got[0].Actors)
	}
	if !got[0].Mine {
		t.Errorf("this reader put one of those there and the fold says otherwise")
	}

	// The retraction is an ENTRY, so the log still says it was acked and then
	// was not - and it takes off only this principal's own.
	if _, err := db.React(ctx, mine, "general", said.ID, "👀", false); err != nil {
		t.Fatalf("un-react: %v", err)
	}
	on, err = db.ReactionsOn(ctx, mine, []string{said.ID})
	if err != nil {
		t.Fatalf("read after the retraction: %v", err)
	}
	got = on[said.ID]
	if len(got) != 1 || len(got[0].Actors) != 1 || got[0].Actors[0] != them.ID {
		t.Fatalf("after taking mine back the message carries %+v, want theirs alone", got)
	}
	if got[0].Mine {
		t.Errorf("the fold still says this reader is on it after they took it back")
	}
	// And the record of both is still in the log, which is the whole reason a
	// retraction is not a delete.
	log, err := db.ReactionsOn(ctx, theirs, []string{said.ID})
	if err != nil {
		t.Fatalf("their read: %v", err)
	}
	if len(log[said.ID]) != 1 {
		t.Fatalf("the other reader sees %+v", log[said.ID])
	}
	if log[said.ID][0].Mine != true {
		t.Errorf("the other reader's own ack does not read as theirs")
	}

	// Re-acking after a retraction works, which is what last-write-wins per
	// (message, actor, emoji) buys and what a set of ids would have lost.
	if _, err := db.React(ctx, mine, "general", said.ID, "👀", true); err != nil {
		t.Fatalf("re-react: %v", err)
	}
	on, _ = db.ReactionsOn(ctx, mine, []string{said.ID})
	if len(on[said.ID]) != 1 || len(on[said.ID][0].Actors) != 2 {
		t.Fatalf("re-acking left %+v, want both back on", on[said.ID])
	}
}

// TestAReactionIsAnEmojiAndNotASecondChat holds the ceiling at the door the
// verb owns, and TestTheCeilingIsHeldWhereverAReactionIsWritten holds it at the
// one the verb does not.
func TestAReactionIsAnEmojiAndNotASecondChat(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rx")
	me := &User{Handle: "me-" + ulid.NewString()}
	if err := db.InsertUser(ctx, me); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: me.ID, Project: project}
	said := &Event{Type: "chat", Project: &project, Room: "general", Actor: me.ID, Body: "hm"}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("say: %v", err)
	}

	for _, bad := range []struct{ why, body string }{
		{"a paragraph", strings.Repeat("no", 400)},
		{"a sentence", "seen it, will look after lunch"},
		{"nothing at all", "   "},
		{"two lines", "👀\n👍"},
	} {
		if _, err := db.React(ctx, p, "general", said.ID, bad.body, true); err == nil {
			t.Errorf("%s was accepted as a reaction", bad.why)
		}
	}
	// And the thing it is for is not refused with them: a family sequence is
	// seven runes and is an emoji somebody actually sends, so a ceiling that
	// cut it would be a ceiling set by counting characters rather than by
	// looking at one.
	for _, ok := range []string{"👀", "👍", "🔥", "👨‍👩‍👧‍👦"} {
		if _, err := db.React(ctx, p, "general", said.ID, ok, true); err != nil {
			t.Errorf("%q was refused as a reaction: %v", ok, err)
		}
	}
}

// TestTheCeilingIsHeldWhereverAReactionIsWritten is the arm that matters,
// because the verb is one of three doors and the other two are the ones a
// hand-written POST and a hostile peer come through. AppendEvent is where both
// of those land.
func TestTheCeilingIsHeldWhereverAReactionIsWritten(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rx")
	me := &User{Handle: "me-" + ulid.NewString()}
	if err := db.InsertUser(ctx, me); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	fat := &Event{
		Type: EventReactionAdd, Project: &project, Room: "general", Actor: me.ID,
		Body: strings.Repeat("x", 4000),
	}
	if err := db.AppendEvent(ctx, fat); err == nil {
		t.Error("a reaction of 4000 characters was written straight into the log " +
			"by the door the verb does not own")
	}
	// And the same door still takes an ordinary chat message of any length,
	// which is what says the rule is about reactions rather than about bodies.
	long := &Event{
		Type: "chat", Project: &project, Room: "general", Actor: me.ID,
		Body: strings.Repeat("x", 4000),
	}
	if err := db.AppendEvent(ctx, long); err != nil {
		t.Errorf("a 4000-character chat message was refused with the reactions: %v", err)
	}
	// And a well-formed one through the same door lands, so the refusal above
	// is the ceiling rather than the type being unwritable here at all.
	fine := &Event{
		Type: EventReactionAdd, Project: &project, Room: "general", Actor: me.ID,
		Body: "👀", Parents: []string{ulid.NewString()},
	}
	if err := db.AppendEvent(ctx, fine); err != nil {
		t.Errorf("a one-glyph reaction was refused by the same door: %v", err)
	}
}

// TestAReactionOnAMessageYouCannotReadIsRefused, and its positive control.
func TestAReactionOnAMessageYouCannotReadIsRefused(t *testing.T) {
	ctx, db := open(t)

	theirs := declaredProject(t, ctx, db, "rx-theirs")
	mine := declaredProject(t, ctx, db, "rx-mine")
	them := &User{Handle: "them-" + ulid.NewString()}
	me := &User{Handle: "me-" + ulid.NewString()}
	for _, u := range []*User{me, them} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	stranger := &Principal{UserID: me.ID, Project: mine}
	author := &Principal{UserID: them.ID, Project: theirs}

	said := &Event{
		Type: "chat", Project: &theirs, Room: "general", Actor: them.ID,
		Body: "in a project the other reader is not in",
	}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("say: %v", err)
	}
	if _, err := db.React(ctx, stranger, "general", said.ID, "👀", true); err == nil {
		t.Fatal("a message in a project this reader is not in was reacted to")
	}
	// The control, so this is about readability rather than about reactions
	// being broken: the author reacts to their own message and it lands.
	if _, err := db.React(ctx, author, "general", said.ID, "👀", true); err != nil {
		t.Fatalf("the author could not react to their own message: %v", err)
	}
	// And the reaction is not visible to the reader who cannot see the message,
	// which is what carrying the id and nothing copied out of it buys.
	on, err := db.ReactionsOn(ctx, stranger, []string{said.ID})
	if err != nil {
		t.Fatalf("stranger's read: %v", err)
	}
	if len(on[said.ID]) != 0 {
		t.Errorf("a reader who cannot open the message sees %+v on it", on[said.ID])
	}
}

// TestAReactionBelongsToTheRoomTheMessageIsIn.
func TestAReactionBelongsToTheRoomTheMessageIsIn(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "rx")
	me := &User{Handle: "me-" + ulid.NewString()}
	if err := db.InsertUser(ctx, me); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: me.ID, Project: project}
	said := &Event{Type: "chat", Project: &project, Room: "general", Actor: me.ID, Body: "here"}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("say: %v", err)
	}
	if _, err := db.React(ctx, p, "elsewhere", said.ID, "👀", true); err == nil {
		t.Error("a message said in general was reacted to in elsewhere")
	}
	if _, err := db.React(ctx, p, "general", said.ID, "👀", true); err != nil {
		t.Errorf("and the same reaction in the right room was refused: %v", err)
	}
}
