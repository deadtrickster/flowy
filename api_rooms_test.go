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

// Deny-by-default from the parameter guard covers the new doors: none of them
// reads a query parameter, so none of them may accept one. This asserts the
// absence deliberately - a route quietly gaining a parameter without declaring
// it is the exact drift routeParams exists to stop.
func TestRoomRoutesDeclareNoQueryParameters(t *testing.T) {
	for _, pattern := range []string{
		"POST /api/rooms",
		"GET /api/rooms",
		"POST /api/rooms/{room}/invite",
		"POST /api/rooms/{room}/leave",
	} {
		if params, ok := routeParams[pattern]; ok {
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
