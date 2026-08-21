package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// IgnoredRoomsTag is the label the console writes on the note that lists the
// rooms a reader has asked not to be told about, and the label this node finds
// it by.
//
// It is a TAG and not a title, for the reason the console already learned about
// the closed-rooms note: a title has to be searched for inside a page, and past
// a couple of hundred personal notes the row falls off the end - where "not
// found" reads as "you have ignored nothing" and the next write files a SECOND
// row. A tag is exact, so absence means absence.
//
// The string is shared with web/src/lib/api.ts, which writes the row. It is
// duplicated rather than generated because there is no build step between the
// two, and a check asserts the two spellings agree.
const IgnoredRoomsTag = "console-ignored-rooms"

// CLOSE AND IGNORE ARE TWO AXES AND THIS IS THE SECOND ONE.
//
// The operator, after saying something into a room they had closed: "humans
// close windows to focus but dont want to miss. what would be a 'real close' is
// *ignoring*".
//
//	close    not in front of me       sidebar only, delivery untouched
//	ignore   do not tell me about it  no wake, no unread, still readable
//	leave    I am not a member        a permission act, and the wrong instrument
//	                                  for either of the first two
//
// WHY THE NODE HOLDS THIS AND THE CONSOLE HOLDS THE OTHER. Closing is a fact
// about one browser's sidebar and nothing else consults it. Ignoring has to
// stop a DELIVERY - the waiter that wakes an agent, the unread count, the
// mention that forces a turn - and every one of those is decided here. A list
// the console kept to itself could not suppress a message that never passes
// through a console.
//
// IT IS STILL A PERSONAL NOTE rather than a column, which is the same argument
// the closed-rooms note makes for itself: `visibility: personal` is a store
// rule and not a convention, so nobody else can read it and nobody else can be
// confused by it, and it costs no schema and no endpoint.
//
// READ ONCE PER WAIT, NOT ONCE PER EVENT. A wait tests every event it may
// deliver against this list, and a lookup inside that loop would be a round
// trip per message. The list is resolved before the loop and handed to the
// filter, which also means every event in one page is judged against ONE
// reading of the preference rather than against whatever it happened to be
// mid-page.
func ignoredRooms(ctx context.Context, db *store.DB, p *store.Principal) (map[string]bool, error) {
	rows, err := db.ListArtifacts(ctx, p, store.ArtifactQuery{
		Type:  store.MemoryType,
		Kind:  "note",
		Tags:  []string{IgnoredRoomsTag},
		Limit: 1,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	// A BODY THAT WILL NOT PARSE IS NOT A REASON TO GO SILENT, and the
	// direction of that failure is the whole point. An unreadable preference
	// here reads as "nothing is ignored", which delivers MORE than the reader
	// asked for; the opposite default would silence a room on the strength of a
	// corrupt note, and a reader cannot tell that from a quiet room.
	var names []string
	if err := json.Unmarshal([]byte(rows[0].Body), &names); err != nil {
		return nil, nil
	}
	out := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out, nil
}
