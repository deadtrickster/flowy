package store

import "testing"

// roomName is the one place a name is judged, so these cases are the whole
// vocabulary of what a room may be called.
func TestRoomNameIsAnAddressNotATitle(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"general", "general"},
		{"  General  ", "general"}, // an address is case-insensitive
		{"build-2", "build-2"},
		{"a_b", "a_b"},
	} {
		got, err := roomName(c.in)
		if err != nil {
			t.Errorf("roomName(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("roomName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// "General" and "general" being two rooms is a bug nobody would file and
// everybody would hit, so the lowercasing above is load-bearing and this says
// what it is for.
func TestRoomNamesDifferingOnlyByCaseAreOneRoom(t *testing.T) {
	a, err := roomName("Handoffs")
	if err != nil {
		t.Fatal(err)
	}
	b, err := roomName("handoffs")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("%q and %q must be the same room", a, b)
	}
}

func TestRoomNameRefusesWhatCannotBeAnAddress(t *testing.T) {
	for _, in := range []string{"", "   ", "has space", "slash/es", "hash#", "emoji✨"} {
		if got, err := roomName(in); err == nil {
			t.Errorf("roomName(%q) = %q, want a refusal", in, got)
		}
	}
	long := ""
	for range 65 {
		long += "a"
	}
	if _, err := roomName(long); err == nil {
		t.Error("a 65 character room name should be refused")
	}
}

// The refusal has to name which of the two things is wrong. "403" sends a
// caller to re-read their token when the real answer is that they are in the
// room and simply not its owner.
func TestNotAMemberAndNotAnOwnerReadDifferently(t *testing.T) {
	if orDash("") == orDash(RoleMember) {
		t.Fatal("being absent and being a member must not read the same in a refusal")
	}
	if orDash("") != "not a member" {
		t.Errorf("an empty role reads %q", orDash(""))
	}
}

// The defect this file exists to prevent: a membership written under one
// identity and read under another. An invite named a user id while the list
// matched the caller's agent id, so an invited member was in the room and could
// not see it - no error, no empty result, just a role that never matched.
func TestAMembershipIsAPersonNotASeat(t *testing.T) {
	person := &Principal{UserID: "01USER", AgentID: "01AGENT"}
	if got := roomPrincipal(person); got != "01USER" {
		t.Fatalf("roomPrincipal = %q, want the user - a seat is not who was invited", got)
	}

	// The half that makes it worth having: voteActor and roomPrincipal answer
	// DIFFERENTLY for the same token, which is exactly the gap that opened.
	// A test that used a principal with no agent would pass either way.
	if actor, _ := voteActor(person); actor == roomPrincipal(person) {
		t.Fatal("this principal cannot show the bug - it needs an agent id that differs from its user id")
	}
}

// A second seat of the same person is the same member. Keying on the agent
// would mean every seat one human runs needs its own invitation, and starting a
// new seat would silently drop them from every room.
func TestEverySeatOfOnePersonIsOneMember(t *testing.T) {
	first := &Principal{UserID: "01USER", AgentID: "01SEAT-A"}
	second := &Principal{UserID: "01USER", AgentID: "01SEAT-B"}
	if roomPrincipal(first) != roomPrincipal(second) {
		t.Fatal("two seats of one person must be one member")
	}
}

// A token with no agent behind it - a person acting directly - is still a
// person, and reads the same rooms.
func TestAPersonWithNoSeatIsStillAMember(t *testing.T) {
	if got := roomPrincipal(&Principal{UserID: "01USER"}); got != "01USER" {
		t.Fatalf("roomPrincipal = %q, want the user", got)
	}
}
