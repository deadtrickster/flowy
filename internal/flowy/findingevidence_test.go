package flowy

// What the evidence door does with a field it does not know.
//
// The answer has to be "refuse and name it", for the reason the gate door's test
// gives about a different route: on this one a dropped field is not a lost
// value. `{"state":"verified","verified_sha":"67adbe04"}` was meant as a
// verified claim with the commit it rests on; with `verified_sha` dropped it is
// `verified` with no commit at all - which the store refuses, so the caller gets
// a 400 about a commit they believe they sent. Naming the field is the
// difference between fixing a typo and re-reading the store's rules.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// evidenceBody posts one body at the evidence door of a server WITH NO DATABASE.
//
// The missing database is the assertion, not a shortcut - gateBody's rule: every
// refusal this test cares about happens in the decoder, before s.db is touched.
// A body that reached the store would nil-deref instead of returning a status,
// so a passing case here is proof the door stopped short of the write.
func evidenceBody(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	s := &server{}
	r := httptest.NewRequest("POST", "/api/finding/01HFINDING/evidence", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleFindingEvidence(w, r)
	return w
}

func TestTheEvidenceDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	w := evidenceBody(t, `{"state":"verified","verified_sha":"67adbe04"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a misspelt commit field was answered with %d, not 400 - it reached the "+
			"store as a verified claim with no commit", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the refusal was not json: %v (%s)", err, w.Body.String())
	}
	if !strings.Contains(got["error"], "verified_sha") {
		t.Fatalf("the refusal does not name the field that was wrong: %q", got["error"])
	}
}

// The other half: the fix must not have shut the door on the bodies it is for.
// Decoded rather than posted, because a well-formed body is supposed to reach a
// store this server does not have.
func TestTheEvidenceDoorTakesTheBodiesItIsFor(t *testing.T) {
	for _, body := range []string{
		`{"state":"source"}`,
		`{"state":"reproduced"}`,
		`{"state":"verified","verified_on":"67adbe04","verified_at":"2026-08-07"}`,
		`{"state":"verified","verified_on":"67adbe04","last_run":"firecode-20260818-091120"}`,
	} {
		var req evidenceRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Errorf("a body this door is for did not decode: %s (%v)", body, err)
		}
	}
}
