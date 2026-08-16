package store

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestADirectMessageIsInvisibleToAThirdPrincipal is the whole feature in one
// test, and the assertion that carries it is the THIRD principal's.
//
// "Both parties can read it" passes under a completely broken implementation -
// the one where everybody can read it - so it proves nothing on its own. What
// this asserts is that carol, who is in alice's project and reads every other
// message alice writes, does not read this one: through ReadEvent, through the
// list every API read goes out of, and through the private narrowing itself.
//
// It runs against the same predicate the wire does. The gate then asks the same
// question over HTTP as a third token, because a rule that is only true inside
// the process is a rule that has not been tested.
func TestADirectMessageIsInvisibleToAThirdPrincipal(t *testing.T) {
	ctx, db := open(t)

	px := declaredProject(t, ctx, db, "dm-px")
	py := declaredProject(t, ctx, db, "dm-py")

	alice := &User{Handle: "dm-alice-" + ulid.NewString()}
	bob := &User{Handle: "dm-bob-" + ulid.NewString()}
	carol := &User{Handle: "dm-carol-" + ulid.NewString()}
	for _, u := range []*User{alice, bob, carol} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	aliceAgent := &Agent{UserID: alice.ID, Kind: "claude", Project: px}
	bobAgent := &Agent{UserID: bob.ID, Kind: "opencode", Project: py}
	for _, a := range []*Agent{aliceAgent, bobAgent} {
		if err := db.InsertAgent(ctx, a); err != nil {
			t.Fatalf("insert agent: %v", err)
		}
	}
	// py may read px, so carol is not the only principal with a reason to
	// expect alice's messages: bob's project holds a live edge into hers, and a
	// grant must not reach a direct message either.
	if err := db.InsertGrant(ctx, &Grant{FromProject: py, ToProject: px, GrantedBy: alice.ID}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	room := declaredProject(t, ctx, db, "dm-room")
	// The control. An ordinary room message from alice, in her project, which
	// carol reads - so "carol cannot read the DM" is a statement about the DM
	// and not about carol having been left out of everything.
	home := px
	public := &Event{
		Type: ChatEventType, Project: &home, Room: room, Actor: alice.ID,
		Body: "said in the room, where the project reads it",
	}
	if err := db.AppendEvent(ctx, public); err != nil {
		t.Fatalf("append the room message: %v", err)
	}

	// The direct message: no project, no room, addressed at bob.
	dm := &Event{
		Type: ChatEventType, Actor: alice.ID, Addressee: bob.ID,
		Body: "between the two of us",
	}
	if err := db.AppendEvent(ctx, dm); err != nil {
		t.Fatalf("append the direct message: %v", err)
	}
	if !dm.Private {
		t.Fatalf("the event was written with no project, no room and an addressee "+
			"and did not come back marked private: %+v", dm)
	}

	// And one from bob back to alice, in the same thread, so the test covers a
	// reply and not only an opening message.
	reply := &Event{
		Type: ChatEventType, Actor: bob.ID, Addressee: alice.ID,
		Thread: dm.Thread, Parents: []string{dm.ID}, Body: "and it stays that way",
	}
	if err := db.AppendEvent(ctx, reply); err != nil {
		t.Fatalf("append the reply: %v", err)
	}

	readers := map[string]*Principal{
		"alice":         {UserID: alice.ID, Project: px},
		"alice's agent": {UserID: alice.ID, AgentID: aliceAgent.ID, Project: px},
		"bob":           {UserID: bob.ID, Project: py},
		"bob's agent":   {UserID: bob.ID, AgentID: bobAgent.ID, Project: py},
		// THE ONE THAT MATTERS. Same project as the sender, reads the room, and
		// is not a party.
		"carol in px": {UserID: carol.ID, Project: px},
		// The operator's escape hatch is not asked for here, so an operator is
		// an ordinary reader and reads no more than carol does.
		"an operator not asking for scope=all": {
			UserID: ulid.NewString(), Project: px, Operator: true,
		},
	}
	// What each of them may read, spelled out rather than derived: an
	// expectation computed from the rule under test tests nothing.
	want := map[string]map[string]bool{
		"alice":                                {"room": true, "dm": true, "reply": true},
		"alice's agent":                        {"room": true, "dm": true, "reply": true},
		"bob":                                  {"room": true, "dm": true, "reply": true},
		"bob's agent":                          {"room": true, "dm": true, "reply": true},
		"carol in px":                          {"room": true, "dm": false, "reply": false},
		"an operator not asking for scope=all": {"room": true, "dm": false, "reply": false},
	}
	events := map[string]*Event{"room": public, "dm": dm, "reply": reply}

	for who, reader := range readers {
		for name, e := range events {
			_, err := db.ReadEvent(ctx, reader, e.ID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				t.Fatalf("read %s as %s: %v", name, who, err)
			}
			if got := err == nil; got != want[who][name] {
				t.Errorf("%s reads the %s message %v, want %v", who, name, got, want[who][name])
			}
		}

		// The list is where a leak actually goes out - /api/events, the inbox,
		// the activity timeline and a room read are all this call - so the
		// per-id answer above is not enough on its own.
		listed, err := db.ListEvents(ctx, reader, EventQuery{Type: ChatEventType})
		if err != nil {
			t.Fatalf("list events as %s: %v", who, err)
		}
		seen := map[string]bool{}
		for _, e := range listed {
			seen[e.ID] = true
		}
		for name, e := range events {
			if seen[e.ID] != want[who][name] {
				t.Errorf("the event list gave %s the %s message %v, want %v",
					who, name, seen[e.ID], want[who][name])
			}
		}

		// And the private narrowing hands over nothing the filter would not
		// have handed over anyway: it is a narrowing, so it can only ever
		// return a subset of the list above.
		private, err := db.ListEvents(ctx, reader, EventQuery{Type: ChatEventType, Private: true})
		if err != nil {
			t.Fatalf("list direct messages as %s: %v", who, err)
		}
		for _, e := range private {
			if !seen[e.ID] {
				t.Errorf("%s reads %s through the private narrowing and not through the "+
					"ordinary list: the narrowing is widening", who, e.ID)
			}
			if !e.Private {
				t.Errorf("%s got %s back from the private narrowing and it is not a "+
					"direct message", who, e.ID)
			}
		}
		if want[who]["dm"] && len(private) != 2 {
			t.Errorf("%s is a party to the conversation and the private read returned %d "+
				"of its 2 messages", who, len(private))
		}
		if !want[who]["dm"] && len(private) != 0 {
			t.Errorf("%s is not a party and the private read returned %d messages",
				who, len(private))
		}
	}
}

// TestADirectMessageThreadKnowsItsParties is the other half of the rule: the
// party set is fixed by the first message, and the send path is what keeps it
// that way.
//
// The read filter cannot see this. Every row it judges names exactly one
// addressee and looks perfectly private on its own, so a thread with three
// people in it and two on every message would pass every read test in this file
// while being a conversation nobody was told the shape of.
func TestADirectMessageThreadKnowsItsParties(t *testing.T) {
	ctx, db := open(t)

	px := declaredProject(t, ctx, db, "party-px")
	alice := &User{Handle: "pt-alice-" + ulid.NewString()}
	bob := &User{Handle: "pt-bob-" + ulid.NewString()}
	carol := &User{Handle: "pt-carol-" + ulid.NewString()}
	for _, u := range []*User{alice, bob, carol} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	dm := &Event{
		Type: ChatEventType, Actor: alice.ID, Addressee: bob.ID, Body: "just us",
	}
	if err := db.AppendEvent(ctx, dm); err != nil {
		t.Fatalf("append the direct message: %v", err)
	}

	pt, err := db.ReadPrivateThread(ctx, dm.Thread)
	if err != nil {
		t.Fatalf("read the private thread: %v", err)
	}
	switch {
	case !pt.Exists:
		t.Fatal("the thread holds a message and reads back as empty")
	case !pt.Private:
		t.Fatal("a thread holding one direct message and nothing else is not all private")
	case pt.NamedByTask:
		t.Fatal("no task names this thread and it says one does")
	case !pt.HasParty(alice.ID) || !pt.HasParty(bob.ID):
		t.Fatalf("the parties are %v, want alice and bob", pt.Participants)
	case pt.HasParty(carol.ID):
		t.Fatalf("carol is not in the conversation and the party set says she is: %v",
			pt.Participants)
	case pt.HasParty(""):
		t.Fatal("an empty id is nobody, and the party set answered to it")
	}

	// A room message in the thread is what makes it not a private conversation,
	// whichever way round the two were written.
	home := px
	mixed := &Event{
		Type: ChatEventType, Project: &home, Room: "general", Actor: alice.ID,
		Thread: dm.Thread, Body: "and now the room can see the thread id",
	}
	if err := db.AppendEvent(ctx, mixed); err != nil {
		t.Fatalf("append the room message: %v", err)
	}
	pt, err = db.ReadPrivateThread(ctx, dm.Thread)
	if err != nil {
		t.Fatalf("read the private thread again: %v", err)
	}
	if pt.Private {
		t.Fatal("a thread with a room message in it still reads as all private")
	}

	// A parent that is not a direct message is refused to the send path, so the
	// DAG stays closed and "this thread is private" stays a property of the
	// whole thread.
	reader := &Principal{UserID: alice.ID, Project: px}
	public, err := db.PublicParents(ctx, reader, []string{dm.ID, mixed.ID, mixed.ID})
	if err != nil {
		t.Fatalf("read the parents: %v", err)
	}
	if len(public) != 1 || public[0] != mixed.ID {
		t.Fatalf("the public parents are %v, want just the room message %s", public, mixed.ID)
	}
}

// TestTheMergeRefusesAPublicEventInAPrivateThread holds the invariant at the
// door a client cannot reach.
//
// Every rule about a private conversation leans on "a private thread holds
// nothing but direct messages": whether a reply may join it, whether a task may
// name it, what a client is told it is. The three local write paths refuse a
// public message into one, and this is the same refusal for a row arriving from
// a peer - where nobody is being misled by a message box, and what is at stake
// is the invariant itself. Without it a node could hold a thread that is half a
// conversation and half a room, and the next private reply into it would be
// refused here and taken over there.
//
// The direct message beside it still lands, and that half matters as much: a DM
// crosses a node boundary by definition, because the whole point of addressing
// somebody is that they may be somewhere else.
func TestTheMergeRefusesAPublicEventInAPrivateThread(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "dmmerge")
	pusher := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	other := "u-" + ulid.NewString()
	at := packed(t, db)

	// The conversation, arriving from the peer: the pusher's own private
	// message to somebody who only exists over there.
	opening := &Event{
		ID: ulid.NewString(), Type: ChatEventType, Actor: pusher.UserID, Addressee: other,
		Thread: ulid.NewString(), Body: "opened over there", SeqHLC: at + 1, Node: "peer-node",
	}
	res, err := db.SyncApplyFrom(ctx, pusher, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{opening}}))
	if err != nil {
		t.Fatalf("apply the direct message: %v", err)
	}
	if res.Applied["events"] != 1 {
		t.Fatalf("a replicated direct message applied %d rows, want 1: %+v",
			res.Applied["events"], res.Reasons)
	}
	if private, err := db.ThreadIsPrivate(ctx, opening.Thread); err != nil || !private {
		t.Fatalf("the thread reads back private=%v (err %v) after one direct message", private, err)
	}

	// And the row that would break it: the same person, the same thread, but a
	// message carrying a project - which everybody in that project reads.
	home := project
	public := &Event{
		ID: ulid.NewString(), Type: ChatEventType, Project: &home, Room: "general",
		Actor: pusher.UserID, Thread: opening.Thread, Body: "and now the project can see it",
		SeqHLC: at + 2, Node: "peer-node",
	}
	res, err = db.SyncApplyFrom(ctx, pusher, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{public}}))
	if err != nil {
		t.Fatalf("apply the room message: %v", err)
	}
	if res.Applied["events"] != 0 || res.Refused["events"] != 1 {
		t.Fatalf("a room message into a private thread applied %d and was refused %d, want 0 and 1: %+v",
			res.Applied["events"], res.Refused["events"], res.Reasons)
	}
	if _, err := db.GetEvent(ctx, public.ID); err == nil {
		t.Fatal("the refused row is in the log anyway")
	}
	if private, err := db.ThreadIsPrivate(ctx, opening.Thread); err != nil || !private {
		t.Fatalf("the thread stopped being private after a refusal: private=%v err=%v", private, err)
	}
}
