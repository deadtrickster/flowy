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
