package flowy

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// THE LINE A PERSON READS AFTER A MESSAGE LANDS NAMES THE PROJECT AND THE
// SPEAKER, because those are the two things that can be wrong without anything
// saying so.
//
// MEASURED: two messages sent from this machine without a token in the
// environment fell back to ~/.config/flowy/token - an operator credential in
// project "pa" - and both printed `said in #general` and exited 0. They landed
// in pa's #general, as the operator. Every seat reads flowy's #general, so
// nobody saw them, including the sender.
func TestSaidWhereNamesTheProjectAndTheSpeaker(t *testing.T) {
	project := func(s string) *string { return &s }
	meta := func(name string) json.RawMessage {
		b, err := json.Marshal(map[string]string{"actor_name": name})
		if err != nil {
			t.Fatalf("meta: %v", err)
		}
		return b
	}

	for _, c := range []struct {
		name string
		said store.Event
		want string
	}{
		{
			"the ordinary case, and the one that would have shown the mistake",
			store.Event{Room: "general", Project: project("flowy"), Meta: meta("orchestrator")},
			"said in flowy/#general as orchestrator",
		},
		{
			"the same room in another project reads differently",
			store.Event{Room: "general", Project: project("pa"), Meta: meta("operator")},
			"said in pa/#general as operator",
		},
		{
			// An unfamiliar id in this line is the signal that the credential
			// was not the one the caller meant, so it is printed rather than
			// blanked.
			"no name recorded falls back to the actor id",
			store.Event{Room: "general", Project: project("flowy"), Actor: "01M05TQ76D8Q4Q6NGBJ0SKT0TB"},
			"said in flowy/#general as 01M05TQ76D8Q4Q6NGBJ0SKT0TB",
		},
		{
			// A direct message has no project by design - see privateEventSQL -
			// so the line must not invent one or print an empty segment.
			"a projectless event says the room alone",
			store.Event{Room: "general", Meta: meta("orchestrator")},
			"said in #general as orchestrator",
		},
		{
			"nothing recorded at all still says where",
			store.Event{Room: "general"},
			"said in #general",
		},
	} {
		if got := saidWhere(&c.said); got != c.want {
			t.Fatalf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// AND IT IS BUILT FROM THE ANSWER, NOT FROM THE REQUEST.
//
// This is the arm that makes the rest worth anything: a line built from what
// was asked for can only describe the attempt, which is exactly how a message
// that went somewhere else reads as one that arrived. The node's answer says
// pa; the caller asked for general in flowy; the line must say pa.
func TestSaidWhereDescribesWhatHappenedNotWhatWasAsked(t *testing.T) {
	pa := "pa"
	// What the caller typed was --room general with a flowy token in mind.
	// What the node wrote is this.
	said := store.Event{Room: "general", Project: &pa, Actor: "01M03E6M1G6R1XM38K67PS6EZ9"}
	got := saidWhere(&said)
	want := "said in pa/#general as 01M03E6M1G6R1XM38K67PS6EZ9"
	if got != want {
		t.Fatalf("got %q, want %q - the line described the request rather than the write", got, want)
	}
}
