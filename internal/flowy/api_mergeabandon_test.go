package flowy

// What the abandon door does before it ever reaches the store.
//
// The strict decoder matters more here than on most routes for a reason that is
// specific to this verb: the ONLY thing separating an abandon from a bare
// unlock is the reason, so a body whose reason is misspelt - `why`, `note`,
// `message` - would decode to an abandon with no reason at all. Refusing the
// unknown field turns that into a 400 naming it, instead of a refusal about a
// missing reason that the caller can see plainly in the body they just sent.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// abandonBody posts one body at the abandon door of a server WITH NO DATABASE.
// The missing database is the assertion: a body that got past the decoder would
// nil-deref rather than return a status, so a status here proves the door
// stopped short of the write.
func abandonBody(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	s := &server{}
	r := httptest.NewRequest("POST", "/api/merge/01HMERGE/abandon", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMergeAbandon(w, r)
	return w
}

func TestTheAbandonDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	w := abandonBody(t, `{"why":"gate went red"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a misspelt reason was answered with %d, not 400 - it reached the store as a reasonless abandon", w.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the refusal was not json: %v (%s)", err, w.Body.String())
	}
	if !strings.Contains(got["error"], "why") {
		t.Fatalf("the refusal does not name the field that was wrong: %q", got["error"])
	}
}

// And the shape the door is for decodes, so the refusal above is a rule about
// unknown fields rather than a door nobody can open.
func TestTheAbandonRequestCarriesItsReason(t *testing.T) {
	var req mergeAbandonRequest
	if err := json.Unmarshal([]byte(`{"reason":"gate went red on the vendored fixture"}`), &req); err != nil {
		t.Fatalf("a well-formed abandon did not decode: %v", err)
	}
	if req.Reason != "gate went red on the vendored fixture" {
		t.Fatalf("reason = %q", req.Reason)
	}
}
