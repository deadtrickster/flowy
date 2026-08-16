package main

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The speaker's name is stamped when there is one and left out when there is
// not. A message with no name in its meta is what every message in every room
// written before the node recorded one looks like, and a reader tells the two
// apart by the key being absent - so an empty string written into it would make
// the whole existing log claim to have been said by nobody.
func TestTheSpeakersNameIsStampedOnlyWhenThereIsOne(t *testing.T) {
	p := &store.Principal{UserID: "01HUSERA", AgentID: "01HAGENTA"}

	named := speakerMeta(p, "agent", "orchestrator")
	if named["actor_name"] != "orchestrator" {
		t.Fatalf("the speaker's name is not on the message: %v", named)
	}
	if named["actor_kind"] != "agent" || named["actor_user"] != "01HUSERA" {
		t.Fatalf("a name displaced what meta already said about the speaker: %v", named)
	}

	nameless := speakerMeta(p, "user", "")
	if _, found := nameless["actor_name"]; found {
		t.Fatalf("a speaker with no name was stamped with one anyway: %v", nameless)
	}
}

// And a name is the node's to stamp, exactly as the rest of the speaker is. The
// key sits under the actor_ prefix for that reason: a client that could write
// its own actor_name would be putting a name it chose in front of every reader
// of the room, on a row that is correctly signed and correctly actored.
func TestClientMetaCannotCarryASpeakersName(t *testing.T) {
	stripped := speakerStripped(json.RawMessage(
		`{"actor_name":"the operator","topic":"kept"}`))

	var fields map[string]any
	if err := json.Unmarshal(stripped, &fields); err != nil {
		t.Fatalf("the stripped meta does not parse: %v", err)
	}
	if _, found := fields["actor_name"]; found {
		t.Fatalf("a client's chosen name survived: %s", stripped)
	}
	if fields["topic"] != "kept" {
		t.Fatalf("stripping took what meta is for with it: %s", stripped)
	}
}
