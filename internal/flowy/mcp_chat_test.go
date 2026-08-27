package flowy

// The chat tools, against a real store.
//
// What is worth asserting here is not that the adapters compile: it is that a
// message said through a tool is INDISTINGUISHABLE from one said through the
// HTTP door - it carries the speaker's name, it is read back through the same
// filter, and an addressee reaches the seat it names. Those are the three things
// a second implementation of the say path would get wrong, and all three fail
// silently: a message with no name renders as anonymous, a message in a room
// somebody cannot read is a message nobody says is missing, and an addressee
// that reaches nobody looks exactly like one that was delivered.
//
// They need a database, so they sit out a plain `go test ./...` and run under
// ./run-tests.sh, the same way the store's own live tests do.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// chatStore dials the database the gate started. Without DATABASE_URL there is
// nothing to talk to.
func chatStore(t *testing.T) (context.Context, *store.DB) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, db
}

// seat is a principal that can speak: a person with a handle, an agent acting
// for them, and the project both are in. It is an agent's token rather than a
// person's because that is what reaches the MCP surface, and because the two are
// not interchangeable to the inbox - a message from a person wakes everybody by
// another rule entirely (see saidByAPerson), which would hide whether the
// addressee did any work.
type seat struct {
	p      *store.Principal
	handle string
}

func newSeat(t *testing.T, ctx context.Context, db *store.DB, project, name string) seat {
	t.Helper()

	handle := name + "-" + ulid.NewString()
	user := &store.User{Handle: handle}
	if err := db.InsertUser(ctx, user); err != nil {
		t.Fatalf("the test could not make a user to speak as: %v", err)
	}
	agent := &store.Agent{UserID: user.ID, Kind: "claude", Project: project}
	if err := db.InsertAgent(ctx, agent); err != nil {
		t.Fatalf("the test could not make an agent to speak as: %v", err)
	}
	return seat{
		p:      &store.Principal{UserID: user.ID, AgentID: agent.ID, Project: project},
		handle: handle,
	}
}

// declaredRoom is a project nobody else's rows are in, and a room name of its
// own, so a room read is a reading of this test and not of the gate's history.
func declaredRoom(t *testing.T, ctx context.Context, db *store.DB, name string) (project, room string) {
	t.Helper()

	project = name + "-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("the test could not declare the project it writes into: %v", err)
	}
	return project, "room-" + ulid.NewString()
}

// callChat runs a tool the way the server does: by name, out of allTools. That
// is deliberate rather than calling chatSay directly - a tool group that is
// written and never registered is a surface nothing can reach, and it looks
// exactly like a working one from the source.
func callChat(
	t *testing.T, ctx context.Context, db *store.DB, p *store.Principal, name string, args any,
) (map[string]any, error) {
	t.Helper()

	tl, ok := toolByName(name)
	if !ok {
		t.Fatalf("%s is not in allTools, so no agent can call it", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("the test's own arguments do not marshal: %v", err)
	}
	out, err := tl.call(ctx, &mcpServer{db: db, node: "test-node"}, p, raw)
	if err != nil {
		return nil, err
	}
	answer, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("%s answered with %T, which is not something a caller can read", name, out)
	}
	return answer, nil
}

// actorName is what the message says the speaker was called, or "" when it says
// nothing - the difference a reader has to be able to see, which is why the key
// is absent rather than empty when there is no name.
func actorName(t *testing.T, e *store.Event) string {
	t.Helper()

	if len(e.Meta) == 0 {
		return ""
	}
	var fields struct {
		ActorName string `json:"actor_name"`
	}
	if err := json.Unmarshal(e.Meta, &fields); err != nil {
		t.Fatalf("the message's meta does not parse: %v", err)
	}
	return fields.ActorName
}

// A message said through the tool is in the room, and it is signed with a name.
//
// The name is the half that would fail quietly. A say path that forgot
// speakerMeta would write a perfectly good row that every surface draws as
// anonymous - actor_name absent, the console falling back to an id - and the
// only sign of it is somebody reading the room and not knowing who spoke.
func TestAMessageSaidThroughTheToolLandsInTheRoomUnderTheSpeakersName(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "chat-say")
	speaker := newSeat(t, ctx, db, project, "sayer")

	body := "the gate is green on land/mcp-chat"
	out, err := callChat(t, ctx, db, speaker.p, "chat_say", map[string]any{
		"room": room, "text": body,
	})
	if err != nil {
		t.Fatalf("saying something in a room this principal can write to failed: %v", err)
	}
	said, ok := out["message"].(*store.Event)
	if !ok {
		t.Fatalf("chat_say answered with %T where the message should be", out["message"])
	}
	if said.Room != room || said.Body != body {
		t.Fatalf("the message landed as room %q body %q, want room %q body %q",
			said.Room, said.Body, room, body)
	}
	if said.Actor != speaker.p.AgentID {
		t.Errorf("the message is actored to %q, not to the principal that said it (%q)",
			said.Actor, speaker.p.AgentID)
	}
	if got := actorName(t, said); got != speaker.handle {
		t.Errorf("the message says it was said by %q, want %q - a message with no name "+
			"renders as anonymous in every room that reads it", got, speaker.handle)
	}
	if said.Thread == "" {
		t.Error("the message started no thread, so nothing can be said in reply to it")
	}

	// And it is in the room to be read, through the read door rather than by
	// looking at what the write returned.
	read, err := callChat(t, ctx, db, speaker.p, "chat_read", map[string]any{"room": room})
	if err != nil {
		t.Fatalf("reading back the room that was just spoken in failed: %v", err)
	}
	events, ok := read["events"].([]*store.Event)
	if !ok {
		t.Fatalf("chat_read answered with %T where the messages should be", read["events"])
	}
	var found *store.Event
	for _, e := range events {
		if e.ID == said.ID {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("the message is not in the room it was said in: %d messages came back", len(events))
	}
	if got := actorName(t, found); got != speaker.handle {
		t.Errorf("read back, the message says it was said by %q, want %q", got, speaker.handle)
	}
}

// A room whose messages this principal may not read answers exactly as a room
// nobody has ever spoken in: an empty window.
//
// That is the rule this whole node reads by - an id out of reach and an id that
// is not there get the same answer - and it is the one an extra read door is
// most likely to break, because the honest-looking alternative is to say "that
// room is not yours", which tells the caller a room exists and who is in it.
func TestARoomTheCallerCannotReadAnswersLikeARoomThatDoesNotExist(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "chat-closed")
	insider := newSeat(t, ctx, db, project, "insider")

	if _, err := callChat(t, ctx, db, insider.p, "chat_say", map[string]any{
		"room": room, "text": "this is said inside one project",
	}); err != nil {
		t.Fatalf("the insider could not say anything: %v", err)
	}

	// Somebody in another project entirely, with no grant into this one.
	elsewhere, _ := declaredRoom(t, ctx, db, "chat-outside")
	outsider := newSeat(t, ctx, db, elsewhere, "outsider")

	closed, err := callChat(t, ctx, db, outsider.p, "chat_read", map[string]any{"room": room})
	if err != nil {
		t.Fatalf("reading a room out of reach must answer, not fail: %v", err)
	}
	empty, err := callChat(t, ctx, db, outsider.p, "chat_read", map[string]any{
		"room": "room-" + ulid.NewString(),
	})
	if err != nil {
		t.Fatalf("reading a room nobody has spoken in must answer, not fail: %v", err)
	}

	if got := closed["events"].([]*store.Event); len(got) != 0 {
		t.Fatalf("a room in another project handed over %d messages", len(got))
	}
	if got := empty["events"].([]*store.Event); len(got) != 0 {
		t.Fatalf("a room nobody has spoken in handed over %d messages", len(got))
	}
	if closed["cursor"] != empty["cursor"] || closed["before"] != empty["before"] {
		t.Errorf("the two answers differ (%v/%v against %v/%v), so the read says which "+
			"rooms exist", closed["cursor"], closed["before"], empty["cursor"], empty["before"])
	}

	// And the message really is there, or the emptiness above proves nothing.
	mine, err := callChat(t, ctx, db, insider.p, "chat_read", map[string]any{"room": room})
	if err != nil {
		t.Fatalf("the insider could not read their own room: %v", err)
	}
	if len(mine["events"].([]*store.Event)) != 1 {
		t.Fatal("the room the outsider was refused is empty for its own project too, " +
			"so this test has been asserting nothing")
	}
}

// `to` names a seat, and the seat's inbox is where the message has to arrive.
//
// This is what makes an agent reachable by name: a waiter blocks on its inbox,
// the inbox delivers chat events, and the addressee is how one of them reaches
// one seat rather than the room. A handle goes in and a principal id comes out,
// because a handle is a spelling and can be changed under a message that stored
// one.
func TestAnAddressedMessageReachesTheNamedSeatsInbox(t *testing.T) {
	ctx, db := chatStore(t)
	project, room := declaredRoom(t, ctx, db, "chat-to")
	sender := newSeat(t, ctx, db, project, "sender")
	addressee := newSeat(t, ctx, db, project, "addressee")

	out, err := callChat(t, ctx, db, sender.p, "chat_say", map[string]any{
		"room": room, "text": "can you take the merge queue item?", "to": addressee.handle,
	})
	if err != nil {
		t.Fatalf("addressing a principal that exists failed: %v", err)
	}
	said := out["message"].(*store.Event)
	if said.Addressee != addressee.p.UserID {
		t.Fatalf("the message is addressed to %q, want the id behind the handle %q (%q) - "+
			"a message addressed to a spelling retargets when the spelling changes",
			said.Addressee, addressee.handle, addressee.p.UserID)
	}
	if out["to"] != addressee.p.UserID {
		t.Errorf("the answer says the message went to %v, so the sender cannot see which "+
			"principal the name resolved to", out["to"])
	}

	// The same message, unaddressed, for the comparison the assertion below needs.
	if _, err := callChat(t, ctx, db, sender.p, "chat_say", map[string]any{
		"room": room, "text": "and the gate is green, for anybody reading",
	}); err != nil {
		t.Fatalf("saying something to the room failed: %v", err)
	}

	// The inbox as the waiter reads it: chat events in this room that this
	// principal did not write.
	inbox, err := db.ListEvents(ctx, addressee.p, store.EventQuery{
		Type:      chatEventType,
		Room:      room,
		NotActors: []string{addressee.p.UserID, addressee.p.AgentID},
	})
	if err != nil {
		t.Fatalf("reading the addressee's inbox failed: %v", err)
	}

	var delivered, woken int
	for _, e := range inbox {
		delivered++
		if wakesFor(addressee.p, e, inboxFilter{addressed: true}) {
			woken++
			if e.ID != said.ID {
				t.Errorf("a message addressed to nobody woke the seat: %s", e.Body)
			}
		}
	}
	if delivered != 2 {
		t.Fatalf("the room's messages reached the addressee's inbox %d times, want 2", delivered)
	}
	if woken != 1 {
		t.Fatalf("%d of the room's messages would wake the named seat, want exactly the "+
			"addressed one - an addressee that wakes nobody is a message the sender "+
			"believes was delivered", woken)
	}
}

// A permission refusal has to arrive as a protocol error and not as a tool
// result, which is the rule written down at `forbidden` in mcp.go: a refusal in
// a success envelope is a refusal nobody notices, and mem_write cost an operator
// three hours of it once. A thread this principal may not write into is that
// kind of refusal; a room name with a slash in it is not - that is an ordinary
// answer to an ordinary question, and it comes back for the model to read.
func TestAThreadRefusalIsAProtocolErrorAndABadArgumentIsNot(t *testing.T) {
	var denied forbidden
	if !errors.As(chatRefusal(refuseChat(http.StatusForbidden, "not your conversation")), &denied) {
		t.Error("a permission refusal came back as an ordinary tool error, so a client " +
			"that checks the call rather than the flag reads it as the message having been said")
	}
	if errors.As(chatRefusal(refuseChat(http.StatusBadRequest, "room must be one segment")), &denied) {
		t.Error("a malformed argument was reported as a permission refusal, which tells " +
			"an agent no rewording will help when rewording is exactly what will")
	}
}

// The registration, without a database: a tool group that is written and never
// appended is a surface nothing can call, and it reads as working from the
// source. The rest of this file would skip on a machine with no Postgres and
// this is what would still be true there.
func TestTheChatToolsAreRegisteredAndTakeWhatTheyNeed(t *testing.T) {
	for _, want := range []struct {
		name     string
		required []string
	}{
		// A say has to name a room and carry something to say: a message with
		// no text is not a message, and a room is not defaultable - guessing
		// one puts an answer where the question was not asked.
		{"chat_say", []string{"room", "text"}},
		{"chat_read", []string{"room"}},
	} {
		tl, ok := toolByName(want.name)
		if !ok {
			t.Errorf("%s is not in allTools, so no agent can call it", want.name)
			continue
		}
		if tl.call == nil {
			t.Errorf("%s is listed with no implementation behind it", want.name)
		}
		req, _ := tl.InputSchema["required"].([]string)
		if len(req) != len(want.required) {
			t.Errorf("%s requires %v, want %v", want.name, req, want.required)
			continue
		}
		for i := range req {
			if req[i] != want.required[i] {
				t.Errorf("%s requires %v, want %v", want.name, req, want.required)
				break
			}
		}
	}
}
