package main

import (
	"strings"
	"testing"
)

// A FLAG THE COMMAND DROPS IS A WRONG ANSWER SHAPED LIKE A RIGHT ONE.
//
// Go's flag package stops at the first non-flag argument, so
// `flowy get /api/x --jq .y` parses nothing and prints the whole document as
// though no filter had been asked for. That is what this verb's first live run
// did, and it looked like a working command.
//
// The first fix broke the other order: pulling out the first argument that does
// not start with a dash takes `.project` - the VALUE of --jq - as the path. A
// pre-scan cannot know which flags take values, so the flag package does it:
// parse, take the first positional, parse again from there.
//
// Both orders are asserted because fixing one by breaking the other is exactly
// what happened, and a test for only the second would have called it fixed.
//
// ASKED OF THE PARSING, NOT OF THE COMMAND. The first cut drove getCmd with an
// empty token, which still reached the network and took forty seconds against
// the deploy-window retry - a test waiting on a real socket to prove an
// argument was read.
func TestGetTakesFlagsOnEitherSideOfThePath(t *testing.T) {
	for _, c := range []struct {
		name, wantPath, wantJQ string
		args                   []string
	}{
		{"flag after the path", "/api/merge-queue", ".x",
			[]string{"/api/merge-queue", "--jq", ".x"}},
		{"flag before the path", "/api/merge-queue", ".x",
			[]string{"--jq", ".x", "/api/merge-queue"}},
		{"flags on both sides", "/api/merge-queue", ".x",
			[]string{"--url", "http://h", "/api/merge-queue", "--jq", ".x"}},
		{"no flags at all", "/api/merge-queue", "",
			[]string{"/api/merge-queue"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseGetArgs(c.args)
			if err != nil {
				t.Fatalf("parse %v: %v", c.args, err)
			}
			if got.path != c.wantPath {
				t.Errorf("path is %q, want %q - the path was read from the wrong argument", got.path, c.wantPath)
			}
			if got.jq != c.wantJQ {
				t.Errorf("jq is %q, want %q - a dropped filter prints the whole document and looks like it worked", got.jq, c.wantJQ)
			}
		})
	}
}

// THE DOOR LIST IS THE NODE'S OWN, so a typo is refused here rather than 404'd
// there - a shell script cannot tell 404-the-row from 404-the-typo, and the
// second reads as "the row is gone" and sends somebody looking for it.
func TestGetRefusesAPathThatIsNotADoor(t *testing.T) {
	for _, c := range []struct {
		path   string
		refuse bool
	}{
		{"/api/merge-queue", false},
		{"/api/artifact/01M0EXAMPLE", false},
		{"/api/merge-que", true},
		{"/api/merge/01M0/renew", true}, // POST-only: not a GET door
		{"/api/artifact", true},         // right prefix, wrong shape
	} {
		err := knownDoor(c.path)
		if c.refuse && err == nil {
			t.Errorf("%s was accepted and is not a GET door", c.path)
		}
		if !c.refuse && err != nil {
			t.Errorf("%s was refused: %v", c.path, err)
		}
	}
	if err := knownDoor("/api/merge-que"); err == nil ||
		!strings.Contains(err.Error(), "not a door") {
		t.Error("the refusal does not say what is wrong")
	}
}
