package main

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// A DELIVERY SAYS WHO SAID IT, at the path every listener already reads.
//
// The room read has always carried the speaker's name at meta.actor_name, and
// every listener in this fleet was written against that shape. The delivery
// dropped meta entirely and carried only `actor`, a ULID - so three agents'
// monitors printed "?" for the author of every message they ever received:
//
//	? [general 01M0HQ0NMS...]: @claude-host queue is 17 and ...
//
// This asserts the PATH and not merely the presence of a name, because a name
// under a different key fixes nobody: the listeners are inline shell in other
// sessions and cannot be edited from here. meta.actor_name or it did not land.
func TestADeliveryCarriesTheSpeakersName(t *testing.T) {
	meta, err := json.Marshal(map[string]any{
		"actor_name": "deadtrickster",
		"actor_kind": "person",
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	page := inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{
			ID:     "01M0HQ0NMS16X5SZ87AGYAB5ZT",
			Room:   "general",
			Actor:  "01M05TQ76D8Q4Q6NGBJ0SKT0TB",
			Thread: "01M0HPQTFJ417T7G0WFK4GEQJD",
			Body:   "so in a way it is prefix branching",
			Meta:   meta,
		}},
	}

	line := captureDelivery(t, page)

	back, ok := line["meta"].(map[string]any)
	if !ok {
		t.Fatalf("the delivery carries no meta at all, so the author is unreachable: keys %v", keysOf(line))
	}
	if back["actor_name"] != "deadtrickster" {
		t.Errorf("meta.actor_name is %v, want %q - a listener reading this path prints \"?\"", back["actor_name"], "deadtrickster")
	}

	// THE THREAD IS THE OTHER HALF OF THE SAME ROW (01M0HH6ANG). A reply
	// needs the prefix it branches from; without this the answer lands in the
	// room and reads as an agent ignoring the thread.
	if line["thread"] != "01M0HPQTFJ417T7G0WFK4GEQJD" {
		t.Errorf("the delivery carries thread %v, want the event's own", line["thread"])
	}
}

// AND THE FIELDS A LISTENER READS ARE A NAMED SET, not whichever one was
// noticed last.
//
// This test asserted meta.actor_name and thread, because those are the two that
// were missing the day it was written. That is the same defect the delivery
// itself has - an enumerating renderer beside a lossless one - and asserting one
// field at a time reproduces it in the check.
//
// The other writer of this same page is spoolEvents (inboxhandover.go), which
// encodes the whole store.Event and therefore cannot lose a field. writeInbox
// hand-builds a map and silently lacks anything added after it was written. So
// what is guarded here is the SET a listener addresses a reply with: without any
// one of these a delivered message cannot be answered where it was asked.
func TestADeliveryCarriesWhatAReplyNeeds(t *testing.T) {
	project := "flowy"
	meta, err := json.Marshal(map[string]any{"actor_name": "orchestrator"})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	page := inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{
			ID:      "01M0HQ0NMS16X5SZ87AGYAB5ZT",
			Room:    "general",
			Actor:   "01M05TQ76D8Q4Q6NGBJ0SKT0TB",
			Thread:  "01M0HPQTFJ417T7G0WFK4GEQJD",
			Body:    "so in a way it is prefix branching",
			Meta:    meta,
			Project: &project,
		}},
	}
	line := captureDelivery(t, page)

	// WHY EACH ONE, so a later reader can judge a removal rather than guess:
	//
	//	room    where to reply. Without it a reply goes to a default room.
	//	thread  which conversation. Without it the reply lands in the room and
	//	        reads as an agent ignoring the thread - 01M0HH6ANG.
	//	body    what was said. A delivery with no body is not a message.
	//	id      what is being answered, for a citation.
	//	meta    who said it, at the path every listener already reads.
	//	project WHICH room of that name. #general in flowy and #general in Lab are
	//	        two rooms and neither reads the other; without this a reply goes
	//	        to whichever one the ANSWERING seat writes in. Measured
	//	        2026-08-25, three times in one hour, by an agent that knew the
	//	        rule and was answering lines that did not carry the fact.
	for _, want := range []struct {
		key string
		val string
		why string
	}{
		{"room", "general", "a reply has nowhere to go"},
		{"thread", "01M0HPQTFJ417T7G0WFK4GEQJD", "a reply lands in the room instead of the thread"},
		{"body", "so in a way it is prefix branching", "there is no message"},
		{"id", "01M0HQ0NMS16X5SZ87AGYAB5ZT", "nothing can be cited"},
		{"project", "flowy", "a reply goes to the room of that name in the ANSWERING seat's project, which is a different room"},
	} {
		if got, _ := line[want.key].(string); got != want.val {
			t.Errorf("the delivery carries %q = %q, want %q - without it %s",
				want.key, got, want.val, want.why)
		}
	}
	meta2, ok := line["meta"].(map[string]any)
	if !ok || meta2["actor_name"] != "orchestrator" {
		t.Errorf("the delivery carries meta = %v, want actor_name orchestrator - without it every author prints \"?\"", line["meta"])
	}
}

// AND AN EVENT WITH NO META DOES NOT GROW AN EMPTY ONE. A key that is there is
// a key that answers; `"meta": {}` makes a reader test two things instead of
// one, and every consumer that checks presence would read it as a name.
func TestADeliveryWithNoMetaHasNoMetaKey(t *testing.T) {
	page := inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{ID: "01M0HQ0NMS16X5SZ87AGYAB5ZT", Room: "general", Body: "hi"}},
	}
	line := captureDelivery(t, page)
	if _, present := line["meta"]; present {
		t.Errorf("an event with no meta was delivered with meta=%v", line["meta"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// captureDelivery runs writeInbox and reads back the one line it wrote. It
// goes through os.Stdout because that is where writeInbox writes, and a test
// that asserted on some other writer would not be asserting on the bytes an
// agent's monitor actually parses.
func captureDelivery(t *testing.T, page inboxWaitResponse) map[string]any {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	writeErr := writeInbox(page)
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if writeErr != nil {
		t.Fatalf("writeInbox: %v", writeErr)
	}
	var line map[string]any
	if err := json.Unmarshal(out, &line); err != nil {
		t.Fatalf("the delivery is not one JSON object: %q (%v)", out, err)
	}
	return line
}

// EVERY FIELD OF THE EVENT REACHES THE DELIVERY, OR IS EXCLUDED ON PURPOSE.
//
// The test above is better than the one before it and is still an enumeration:
// it lists what a reply needs, so a field added to store.Event next month is
// covered by nobody. @flowy-claude's inversion is the fix - assert the delivery
// carries everything the SPOOL carries, minus a named exclusion list - because
// then the default for a new field is "carried", and dropping one becomes a
// decision somebody writes down rather than an omission nobody sees.
//
// The two writers are the reason this is worth guarding. Both serialise the
// same page: spoolEvents (inboxhandover.go) encodes the whole store.Event and
// cannot lose a field; writeInbox hand-builds a map and loses whatever was added
// after it was written. That is how meta.actor_name went missing for as long as
// this fleet has had monitors.
func TestTheDeliveryDropsNothingByAccident(t *testing.T) {
	// WHY EACH ONE IS ABSENT. A reason that is not a reason is worse than no
	// list, so anything here that reads as a shrug is a real open question.
	excluded := map[string]string{
		"sig":        "the signature is over the stored body; a delivery is a rendering, and body_signed carries the exact bytes when it differs",
		"author_sig": "the owner's claim about a ROW, not about a chat line a waiter is answering",
		"authorship": "this node's judgement of that claim, and it travels with the row",
		"seq_hlc":    "a 57-bit reading; the delivery hands back `cursor` instead, because a browser cannot hold this one without rounding it",
		"parents":    "structure for the log, not something a reply addresses",
		"type":       "every event a waiter receives is a message; the field is for the store",
		"node":       "which node stored it - routing, not content",
		"project":    "the reader's project is settled before delivery, by the same predicate the room read uses",
		"private":    "its own comment forbids reading it to answer `may they see it` - by the time it is set that question is answered",
		"citation":   "NOT dropped: writeInbox inlines the quote into body and puts the exact signed bytes in body_signed",
	}

	// EVERY FIELD POPULATED, because an absent value and a dropped one look
	// identical from here. writeInbox carries `artifact` and `meta` only when
	// they are non-empty, so a fixture that leaves them zero accuses the code
	// of dropping what it was never given - measured, this test failed that way
	// first. A NEW conditional field will trip it the same way, which is the
	// right outcome: populate the fixture or declare the exclusion.
	line := captureDelivery(t, inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{
			ID:        "01M0HQ0NMS16X5SZ87AGYAB5ZT",
			Room:      "general",
			Actor:     "01M05TQ76D8Q4Q6NGBJ0SKT0TB",
			Addressee: "claude-host",
			Thread:    "01M0HPQTFJ417T7G0WFK4GEQJD",
			Body:      "hi",
			Created:   time.Unix(1787305600, 0).UTC(),
			Artifact:  "01M0HH6ANG7NF6G6X7RKQ4XWSR",
			Meta:      json.RawMessage(`{"actor_name":"orchestrator"}`),
			Disowned: &store.Disowned{
				By:      "01M0J4FA3K2MKYXPPKV0EAN2MA",
				Subject: "01M05TQ76D8Q4Q6NGBJ0SKT0TB",
				Reason:  "not me",
				From:    1,
				To:      2,
			},
		}},
	})

	// The event's own field names, read off the struct tags rather than typed
	// here - a list typed here would rot the same way the delivery did.
	for _, f := range eventJSONFields(t) {
		if _, carried := line[f]; carried {
			continue
		}
		why, stated := excluded[f]
		if !stated {
			t.Errorf("the delivery drops %q and nothing says why.\n"+
				"Either carry it in writeInbox, or add it to `excluded` with the reason.\n"+
				"A field that vanishes between spoolEvents and writeInbox is exactly how\n"+
				"meta.actor_name was lost.", f)
			continue
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("the delivery drops %q with an empty reason", f)
		}
	}
}

// eventJSONFields reads the json names off store.Event, so this check cannot
// drift from the type it is about.
// eventJSONFields reads the names encoding/json would emit for store.Event, so
// this check cannot drift from the type it is about.
//
// AN UNTAGGED EXPORTED FIELD COUNTS, and that is not pedantry: encoding/json
// falls back to the GO NAME when there is no tag, so spoolEvents would carry it
// and a check that skipped it would be blind to exactly the case it exists for.
// @flowy-claude named this limit; it is cheaper to close than to write down.
//
// `json:"-"` is skipped on purpose - the encoder omits it everywhere, so the
// spool does not carry it either and the delivery is not losing anything.
// Unexported fields likewise: they never reach either writer.
func eventJSONFields(t *testing.T) []string {
	t.Helper()
	var out []string
	rt := reflect.TypeOf(store.Event{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			// No tag: encoding/json uses the field name verbatim.
			name = f.Name
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("store.Event has no encodable fields, so this check asserts nothing")
	}
	return out
}
