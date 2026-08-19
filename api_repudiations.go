package main

// GET /api/repudiations - who has disowned what, and what this node cannot read.
//
// The store half of repudiation landed a type, the marking half put a mark on
// every row that carries an author, and neither gave a person a place to ASK
// the question directly: which claims does this node hold, who made them, and
// over what. A reader could see a row marked and follow it to the repudiation
// by id, and could not go the other way.
//
// IT REPORTS WHAT IT CANNOT READ, which is the reason this door is worth having
// rather than a convenience. A repudiation whose window is unreadable is
// dropped by every other surface - the check refuses it, the marking list
// excludes it, the rows it would have covered read as not disowned. Silence
// there is safe and dishonest: "nobody disowned this" and "somebody disowned
// this and I cannot tell whom" become one answer. This is the one surface where
// saying so helps, because it is the only one whose reader came to look at
// repudiations. See store.UnreadableRepudiations.

import (
	"net/http"
)

func (s *server) handleRepudiations(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	// THE LIST IS PERMISSION-FILTERED, and it is the only repudiation surface
	// that is. The MARK on a row is computed against every repudiation this
	// node holds, because it annotates a row the caller can already read rather
	// than revealing one they cannot - see store.FillDisowned. Reading the
	// claim itself, with its body and its reason, is an ordinary artifact read
	// and obeys the ordinary rule.
	items, err := s.db.Repudiations(r.Context(), p)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// Not filtered, and it does not need to be: a count of rows this node
	// cannot parse names nobody and reveals nothing about what they say. The
	// ids travel so somebody can fetch one and find out whose encoder is wrong
	// - and a caller who may not read that row still gets the ordinary refusal
	// when they try.
	unreadable, err := s.db.UnreadableRepudiations(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	body := map[string]any{"items": items}
	if unreadable != nil {
		body["unreadable"] = unreadable
	}
	writeJSON(w, http.StatusOK, body)
}
