package main

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

// None of the room doors reads a query parameter, so none of them may accept
// one - a route quietly gaining a parameter without declaring it is the drift
// routeParams exists to stop.
//
// This asked for the entry to be ABSENT when it was written, because absence
// was then the only way to say "takes nothing". It is now an entry that is
// EMPTY, and the difference is the point: absent meant nobody had looked, and
// these four had been looked at. The assertion is the same one either way -
// these routes accept no parameters - and it now fails if somebody deletes the
// entry as well as if somebody fills it in.
func TestRoomRoutesDeclareNoQueryParameters(t *testing.T) {
	for _, pattern := range []string{
		"POST /api/rooms",
		"GET /api/rooms",
		"POST /api/rooms/{room}/invite",
		"POST /api/rooms/{room}/leave",
	} {
		params, ok := routeParams[pattern]
		if !ok {
			t.Errorf("%s is not in routeParams - it is guarded, so it must say what it takes, "+
				"and {} is how it says none", pattern)
			continue
		}
		if len(params) != 0 {
			t.Errorf("%s declares %v - if that is deliberate, say why here", pattern, params)
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
