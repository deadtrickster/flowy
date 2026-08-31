package store

// The name of the seat a message was written TO.
//
// Author (authors.go) is this rule applied to who wrote a row; this is the same
// rule applied to the other end of the same row. A room read by four seats is
// read as names, not ids, and the addressee was the one id left unresolved:
// MessageList drew shortId(addressee, 8), so a message addressed to
// 01M05YCEFY6BQAR2WPMMXTYVG2 rendered as "to MMXTYVG2" beside a speaker chip
// that said "claude-host" - the same person, named twice, one of them as eight
// characters of a ULID.
//
// WHY THE PAGE COULD NOT DO IT. meta.mentions carries "name:id" pairs stamped
// at write time, so a message that named somebody with an @ has the name on the
// wire already. `flowy say --to NAME` sets an addressee WITHOUT writing a
// mention into the body, and measured on the dogfood node one of three
// addressed messages had no mentions at all - and it was the one being drawn as
// an id. A client-side rule would have named two of them and left the third
// looking like a different kind of thing.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

// FillAddresseeNames resolves the display name for each event's addressee, in
// place. One query for the page, for FillAuthorNames' reason: every row is then
// judged against one state of the registries.
//
// A failure is the caller's to decide, as with the two fills beside it - the
// events are what was asked for and the name is an annotation on them.
func (d *DB) FillAddresseeNames(ctx context.Context, events []*Event) error {
	if len(events) == 0 {
		return nil
	}
	// THE STAMPED NAME FIRST, and for speakerNameOfEvent's reason rather than
	// for speed: meta.mentions records what somebody was called AT THE MOMENT
	// THEY WERE NAMED, so resolving the id afresh would silently rename every
	// message ever addressed to somebody whose handle later changed. Matched by
	// id and not by position - the first pair is the addressee for a message
	// addressed BY an @name, and a message can carry both a --to addressee and
	// mentions of other people.
	ids := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		if e == nil || e.Addressee == "" {
			continue
		}
		if name := stampedMentionName(e.Meta, e.Addressee); name != "" {
			e.AddresseeName = name
			continue
		}
		if seen[e.Addressee] {
			continue
		}
		seen[e.Addressee] = true
		ids = append(ids, e.Addressee)
	}
	if len(ids) == 0 {
		return nil
	}

	// The two arms and their nullif are FillAuthorNames', deliberately copied
	// rather than generalised: see that function for what each clause is for,
	// and note that a second spelling of "how this node names a principal" is
	// exactly what speakerNameOf's own comment refuses to have.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT u.id, u.handle
		   FROM users u
		  WHERE u.id = ANY($1) AND coalesce(nullif(u.handle, ''), '') <> ''
		 UNION ALL
		 SELECT a.id, coalesce(nullif(u.handle, ''), a.kind)
		   FROM agents a
		   LEFT JOIN users u ON u.id = a.user_id
		  WHERE a.id = ANY($1) AND coalesce(nullif(u.handle, ''), a.kind) <> ''`,
		pq.Array(ids))
	if err != nil {
		return fmt.Errorf("store: resolve addressee names: %w", err)
	}
	defer rows.Close()

	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("store: resolve addressee names: %w", err)
		}
		if _, ok := names[id]; !ok {
			names[id] = name
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: resolve addressee names: %w", err)
	}

	for _, e := range events {
		if e == nil || e.Addressee == "" || e.AddresseeName != "" {
			continue
		}
		// names[id] is "" for an id in neither table, which is the answer:
		// unnameable, never the id handed back as though it were a name.
		e.AddresseeName = names[e.Addressee]
	}
	return nil
}

// stampedMentionName is the name meta.mentions recorded for one principal, or
// "" when it recorded none.
//
// The encoding is "name:id" pairs separated by spaces and a name can hold
// neither character, so splitting is exact - the same parse web/src/lib/
// mentions.ts does, and mentions.go writes.
//
// Into map[string]json.RawMessage rather than map[string]string for
// speakerNameOfEvent's reason: meta is not all strings, and one non-string
// value would fail the whole unmarshal and drop the name off every event that
// had one.
func stampedMentionName(meta json.RawMessage, id string) string {
	if len(meta) == 0 || id == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return ""
	}
	raw, ok := fields["mentions"]
	if !ok {
		return ""
	}
	var pairs string
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return ""
	}
	for start := 0; start < len(pairs); {
		end := start
		for end < len(pairs) && pairs[end] != ' ' {
			end++
		}
		pair := pairs[start:end]
		start = end + 1
		colon := -1
		for i := 0; i < len(pair); i++ {
			if pair[i] == ':' {
				colon = i
				break
			}
		}
		if colon <= 0 {
			continue
		}
		if pair[colon+1:] == id {
			return pair[:colon]
		}
	}
	return ""
}
