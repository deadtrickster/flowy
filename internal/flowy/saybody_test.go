package flowy

import (
	"os"
	"strings"
	"testing"
)

// A LONE `-` IS STDIN, AND A DASH AMONG WORDS IS A WORD.
//
// MEASURED before this test existed: `flowy say --url U - <<EOF` posted the
// literal string "-" as the message, with exit 0 and the same success line a
// real message gets, because an argument was given and the argument wins. Six
// rows were filed with a body of "-" that evening, two of them merge rows
// carrying the evidence a reviewer reads, plus every note written across four
// hours and about ten messages in a room.
//
// The arms are the three states the caller can be in, and the third is the one
// that made this a defect rather than an inconvenience: stdin held the text the
// whole time.
func TestALoneDashMeansStdin(t *testing.T) {
	withStdin := func(t *testing.T, text string, fn func()) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		old := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = old; r.Close() }()
		go func() { _, _ = w.WriteString(text); w.Close() }()
		fn()
	}

	// THE ONE THAT WAS BROKEN. A dash with a heredoc behind it reads the
	// heredoc, not the dash.
	withStdin(t, "the measurement, on stdin\n", func() {
		got, err := bodyOrStdin([]string{"-"}, "say", "usage")
		if err != nil {
			t.Fatalf("a lone dash with stdin behind it was refused: %v", err)
		}
		if got != "the measurement, on stdin" {
			t.Fatalf("body is %q, want what was on stdin - a dash argument still won", got)
		}
	})

	// A DASH AMONG WORDS IS A WORD. `flowy say - hello` is a message that
	// starts with a dash, and treating it as stdin would take a body away from
	// a caller who typed one.
	withStdin(t, "not this\n", func() {
		got, err := bodyOrStdin([]string{"-", "hello"}, "say", "usage")
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if got != "- hello" {
			t.Fatalf("body is %q, want %q", got, "- hello")
		}
	})

	// AND AN ORDINARY ARGUMENT IS UNTOUCHED.
	withStdin(t, "not this either\n", func() {
		got, err := bodyOrStdin([]string{"landed", "it"}, "say", "usage")
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if got != "landed it" {
			t.Fatalf("body is %q", got)
		}
	})
}

// A LONE DASH WITH NOTHING BEHIND IT IS REFUSED, in the verb's own words.
//
// This is the arm that keeps the fix from trading one silent loss for another:
// `flowy say -` with an empty pipe must not post an empty message, and it must
// not post a dash either. The refusal that already existed for "no argument and
// no stdin" is the one it falls into.
func TestALoneDashWithNothingBehindItIsRefused(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	w.Close() // closed with nothing written: EOF straight away

	got, err := bodyOrStdin([]string{"-"}, "say", "usage-goes-here")
	if err != nil {
		t.Fatalf("an empty pipe behind a dash errored rather than answering empty: %v", err)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("an empty pipe behind a dash produced %q", got)
	}
	// The verbs check emptiness themselves and say so with their own usage -
	// see cliSay, which refuses "nothing to say". What matters here is that a
	// dash never becomes the body.
	if got == "-" {
		t.Fatal("the dash became the body, which is the whole defect")
	}
}
