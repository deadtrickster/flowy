package flowy

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// readSource reads a file in this package so a test can assert about wiring
// that has no runtime handle - which route strings are registered, and which
// status a door answers with. It is a blunt instrument and deliberate: the
// alternative is a test that constructs a server, a database and a principal
// to prove that a line exists.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The room doors are registered, and registered under the paths the console
// will call. A handler nobody can reach is the same as no handler, and the
// console's whole reason for this row is that it stops hardcoding a list.
func TestRoomRoutesAreRegistered(t *testing.T) {
	src := readSource(t, "serve.go")
	for _, pattern := range []string{
		"POST /api/rooms",
		"GET /api/rooms",
		"POST /api/rooms/{room}/invite",
		"POST /api/rooms/{room}/leave",
	} {
		if !strings.Contains(src, `"`+pattern+`"`) {
			t.Errorf("serve.go does not register %q", pattern)
		}
	}
}

// The room doors take what they say they take - a route quietly gaining a
// parameter without declaring it is the drift routeParams exists to stop.
//
// This asked for the entry to be ABSENT when it was written, because absence
// was then the only way to say "takes nothing". It is now an entry that is
// EMPTY, and the difference is the point: absent meant nobody had looked, and
// these had been looked at. It fails if somebody deletes the entry as well as
// if somebody fills it in.
//
// GET /api/rooms IS THE EXCEPTION AND HERE IS WHY, which is what the older
// version of this test asked for by name.
//
// It takes `project`, because until it did, the console could list the projects
// a token may see and not one thing inside any of them - /projects landed
// saying exactly that on its own face. And `scope`, because the permission it
// checks is ReadableProjects, which is scope-aware everywhere else it is used;
// a door that asked that question with a different scope from the door beside
// it would give two answers to one question.
//
// The other three take nothing and still take nothing. A write with a query
// parameter is a write whose subject is in two places.
func TestRoomRoutesDeclareWhatTheyTake(t *testing.T) {
	for pattern, want := range map[string][]string{
		"POST /api/rooms":               {},
		"GET /api/rooms":                {"project", "scope"},
		"POST /api/rooms/{room}/invite": {},
		"POST /api/rooms/{room}/leave":  {},
	} {
		params, ok := routeParams[pattern]
		if !ok {
			t.Errorf("%s is not in routeParams - it is guarded, so it must say what it takes, "+
				"and {} is how it says none", pattern)
			continue
		}
		if len(params) != len(want) {
			t.Errorf("%s declares %v, want %v - a door that takes more than it says is the drift "+
				"this test exists to catch, and one that takes less has lost a feature", pattern, params, want)
			continue
		}
		for i, p := range want {
			if params[i] != p {
				t.Errorf("%s declares %v, want %v", pattern, params, want)
				break
			}
		}
	}
}

// A create that collides answers 409, not 400: "that name is in use" and "that
// name is not a name" send a caller to different places, and two people wanting
// the same room is not anybody's mistake.
func TestTakenRoomIsAConflictNotABadRequest(t *testing.T) {
	if http.StatusConflict == http.StatusBadRequest {
		t.Fatal("unreachable, but states the intent this file was written to hold")
	}
	src := readSource(t, "api_rooms.go")
	if !strings.Contains(src, "http.StatusConflict") {
		t.Error("api_rooms.go should answer a taken name with 409")
	}
	if !strings.Contains(src, "ErrRoomTaken") {
		t.Error("the 409 should be driven by the store's typed error, not by string matching")
	}
}
