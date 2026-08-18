package main

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A QUEUE ROW REMEMBERS WHO RAISED IT, and a row that is not queue work does
// not grow a field it has no use for.
//
// raiser was stamped by POST /api/chat/{room}/todo and by the MCP raise tool
// and by nothing else, so every row written through POST /api/artifacts - which
// is what the console's /new page and every script here uses - carried no
// reporter. Measured on the live node the day this was found: the six most
// recent todos, raiser on zero of them.
func TestADirectlyCreatedQueueRowRemembersWhoRaisedIt(t *testing.T) {
	for _, tc := range []struct {
		what string
		art  *store.Artifact
		who  string
		want string
	}{
		{
			what: "a todo with nobody named",
			art:  &store.Artifact{Kind: "todo"},
			who:  "fish",
			want: "fish",
		},
		{
			what: "a todo that already names its raiser",
			art:  &store.Artifact{Kind: "todo", Fields: json.RawMessage(`{"raiser":"somebody-else"}`)},
			who:  "fish",
			want: "somebody-else",
		},
	} {
		got, err := raiserDefault(tc.art, tc.who)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if got != nil {
			tc.art.Fields = got
		}
		if raised := store.RaiserOf(tc.art); raised != tc.want {
			t.Errorf("%s: raiser is %q, want %q", tc.what, raised, tc.want)
		}
	}
}

// NOT A NOTE, NOT A REPORT, NOT A DIAGRAM. raiser answers "where did this work
// come from", and a row that is not work has no answer to give.
func TestANonQueueRowIsNotGivenARaiser(t *testing.T) {
	art := &store.Artifact{Kind: "note"}
	got, err := raiserDefault(art, "fish")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("a note was given fields: %s", got)
	}
}

// AND NOTHING IS INVENTED FOR A CALLER WITH NO NAME. store.SeatHandle answers
// "" for a user with no handle, and the row then says nobody rather than an id
// - which is the whole reason this does not use chatActor.
func TestACallerWithNoHandleStampsNothing(t *testing.T) {
	art := &store.Artifact{Kind: "todo"}
	got, err := raiserDefault(art, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("a nameless caller was written into the row: %s", got)
	}
}

// The fields a create carried are KEPT. Adding one key by replacing the object
// would drop a room, a category or an assignee - a worse bug than the one this
// fixes, and silent.
func TestStampingARaiserKeepsTheFieldsTheCallerSent(t *testing.T) {
	art := &store.Artifact{Kind: "todo", Fields: json.RawMessage(`{"room":"general","category":"fix"}`)}
	got, err := raiserDefault(art, "fish")
	if err != nil {
		t.Fatal(err)
	}
	art.Fields = got

	var fields map[string]any
	if err := json.Unmarshal(art.Fields, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"room": "general", "category": "fix", "raiser": "fish"} {
		if fields[key] != want {
			t.Errorf("%s is %v, want %q", key, fields[key], want)
		}
	}
}
