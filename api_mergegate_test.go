package main

// What the gate door does with a field it does not know.
//
// The answer has to be "refuse and name it", because on this route a dropped
// field is not a lost value - it is a different verb. `{"run":"r","tip":"abc"}`
// was meant as a verdict; with `tip` dropped it is a declaration with no tip,
// and a declaration takes the landing lock on master for fifteen minutes that
// nothing but a land or the expiry releases. The caller got a 200 and no way
// back.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gateBody posts one body at the gate door of a server WITH NO DATABASE.
//
// The missing database is the assertion, not a shortcut: every refusal this
// test cares about happens in the decoder, before s.db is touched. A body that
// reached the store would nil-deref instead of returning a status, so a passing
// case here is proof the door stopped short of the write.
func gateBody(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	s := &server{}
	r := httptest.NewRequest("POST", "/api/merge/01HMERGE/gate", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMergeGate(w, r)
	return w
}

func TestTheGateDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	// The exact body from the incident: a verdict with the tip misspelt.
	w := gateBody(t, `{"run":"acceptance","tip":"966bf6a"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a misspelt field was answered with %d, not 400 - it reached the store as a declaration", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the refusal was not json: %v (%s)", err, w.Body.String())
	}
	// Naming the field is most of the value. "bad request" would leave the
	// caller re-reading their own body for the typo.
	if !strings.Contains(got["error"], "tip") {
		t.Fatalf("the refusal does not name the field that was wrong: %q", got["error"])
	}

	// And the general case, so this is a rule about unknown fields rather than
	// one known misspelling.
	if w := gateBody(t, `{"run":"acceptance","gated_tip":"966bf6a","note":"green"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field alongside good ones was answered with %d, not 400", w.Code)
	}
}

// The other half: the fix must not have shut the door on the bodies it is for.
// These are decoded rather than posted because a well-formed body is supposed
// to reach the store, and the store is what this test does without.
func TestTheGateDoorStillTakesTheFieldsItDocuments(t *testing.T) {
	for _, body := range []string{
		`{"run":"acceptance"}`,
		`{"run":"acceptance","gated_tip":"966bf6a"}`,
		`{"run":"acceptance","gated_tip":"966bf6a","gated_ref":"integration/nightly"}`,
	} {
		var req mergeGateRequest
		r := httptest.NewRequest("POST", "/api/merge/01HMERGE/gate", strings.NewReader(body))
		if err := decodeJSON(r, &req); err != nil {
			t.Fatalf("a body the door documents was refused: %s: %v", body, err)
		}
		if req.Run != "acceptance" {
			t.Fatalf("the run did not survive the decode of %s: %+v", body, req)
		}
	}

	var req mergeGateRequest
	r := httptest.NewRequest("POST", "/api/merge/01HMERGE/gate",
		strings.NewReader(`{"run":"acceptance","gated_tip":"966bf6a","gated_ref":"integration/nightly"}`))
	if err := decodeJSON(r, &req); err != nil {
		t.Fatalf("the full verdict body was refused: %v", err)
	}
	if req.GatedTip != "966bf6a" || req.GatedRef != "integration/nightly" {
		t.Fatalf("a verdict lost a field it spelled correctly: %+v", req)
	}
}

// The land door is the gate door's other half and had the same loose decoder.
// A misspelt `sha` there refuses with "too short to name a commit", which is a
// true sentence about a mistake the caller did not make.
func TestTheLandDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest("POST", "/api/merge/01HMERGE/land",
		strings.NewReader(`{"tip":"966bf6a2e1f0"}`))
	w := httptest.NewRecorder()
	s.handleMergeLand(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a misspelt sha was answered with %d, not 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tip") {
		t.Fatalf("the refusal does not name the field that was wrong: %s", w.Body.String())
	}

	var req mergeLandRequest
	ok := httptest.NewRequest("POST", "/api/merge/01HMERGE/land",
		strings.NewReader(`{"sha":"966bf6a2e1f0"}`))
	if err := decodeJSON(ok, &req); err != nil || req.SHA != "966bf6a2e1f0" {
		t.Fatalf("the correctly spelt land body did not survive: %+v %v", req, err)
	}
}
