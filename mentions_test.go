package main

import (
	"strings"
	"testing"
)

// What is a mention and what is only an @ sign.
//
// EMAIL@EXAMPLE.COM IS THE ONE THAT MATTERS. The naive version of this feature
// is a regex - `@(\w+)` - and it turns every email address anybody pastes into
// a room into a mention of somebody called example, silently addressing a
// message to whoever happens to hold that handle. It is also the version that
// looks right in review, because the case it gets wrong is not the case
// anybody writes the feature for. The @ has to start a word.
//
// The rest are the ones that make a mention usable in a sentence rather than
// only at the start of one: punctuation after a name belongs to the writer, a
// name at the start of a sentence is capitalised, and a stray @ is not an
// address to anybody.
func TestWhatCountsAsAMention(t *testing.T) {
	for _, c := range []struct {
		body string
		want []string
		why  string
	}{
		{"@a and @b", []string{"a", "b"}, "two names, in the order they were written"},
		{"email@example.com is not a mention", nil,
			"the @ is inside a word: this is an address, not a mention"},
		{"ask @name, then wait", []string{"name"},
			"the comma ends the clause, not the name"},
		{"ask @name.", []string{"name"}, "the full stop ends the sentence, not the name"},
		{"@i.khaprov please look", []string{"i.khaprov"},
			"a dot inside a name is part of it - people have handles like this"},
		{"@Name at the start", []string{"Name"},
			"as written; the case-folding is the resolver's job"},
		{"hi@", nil, "an @ with nothing after it addresses nobody"},
		{"hi @", nil, "and neither does one on its own"},
		{"@alice-01J8ZK and @alice-01J8ZK again", []string{"alice-01J8ZK"},
			"a handle carries its suffix, and a repeat is one mention"},
		{"see (@name) or [@other]", []string{"name", "other"},
			"brackets are punctuation on both sides"},
		{"@nobody-at-all is here", []string{"nobody-at-all"},
			"a name that means nobody is still a name: the resolver decides"},
	} {
		got := mentionNames(c.body)
		if len(got) != len(c.want) {
			t.Errorf("%q -> %v, want %v (%s)", c.body, got, c.want, c.why)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q -> %v, want %v (%s)", c.body, got, c.want, c.why)
				break
			}
		}
	}
}

// A name nothing answers to is left as text, and it is NOT an error. People
// type names that do not exist, and a message refused over a word in it is the
// worse failure by a distance - what somebody wrote is gone, and what they are
// told off for is prose.
func TestAnUnresolvedMentionIsPlainTextAndNotARefusal(t *testing.T) {
	found, err := resolveMentions("@nobody-at-all can you look", testRoster)
	if err != nil {
		t.Fatalf("an unknown name was an error: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a name nobody answers to resolved to %v", found)
	}
	if to := mentionAddressee(found); to != "" {
		t.Fatalf("a message addressed to nobody carries %q", to)
	}
}

// Several names in one message: the first is the addressee, the rest ride in
// meta. Which of the two this is has to be checked rather than assumed - the
// event carries one addressee, and a reader of meta that found the addressing
// there instead would be a console drawing a ring nobody was wearing.
func TestTheFirstMentionAddressesAndTheRestAreOnTheMessage(t *testing.T) {
	found, err := resolveMentions("@thatname and @somebodyelse, over to you", testRoster)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if to := mentionAddressee(found); to != "a-me" {
		t.Fatalf("addressed to %q, want the first mention a-me", to)
	}
	meta := mentionMeta(found)
	if !strings.Contains(meta, "thatname:a-me") || !strings.Contains(meta, "somebodyelse:a-other") {
		t.Fatalf("the mentions on the message are %q, want both pairs", meta)
	}
}

// testRoster is a node with two principals in it, so what a name resolves to
// is decided here rather than by whatever a database happens to hold.
func testRoster(names []string) (map[string]string, error) {
	roster := map[string]string{"thatname": "a-me", "somebodyelse": "a-other"}
	out := map[string]string{}
	for _, name := range names {
		if id, ok := roster[strings.ToLower(name)]; ok {
			out[strings.ToLower(name)] = id
		}
	}
	return out, nil
}
