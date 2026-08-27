package flowy

// The withdrawal door, in the two ways it can be got wrong.
//
// One is the shape every write door here has to hold: a field it does not know
// is a 400 naming it, because on this route a dropped field is not a lost value
// - `why` is the whole record of who claimed the block was fixed, and a
// withdrawal with it silently discarded is a block that vanished with nobody's
// name on it, which is precisely the state this door exists to make impossible.
//
// The other is the property that made the door worth building: it must never
// take the landing lock. A test driving the happy path would prove the write
// and say nothing about the one line that would put the fix back behind the
// lock it was written to get out from behind.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// unblockedBody posts one body at the door of a server WITH NO DATABASE. The
// missing database is the assertion: a body that got past the decoder would
// nil-deref rather than return a status, so a case that comes back 400 is proof
// the door stopped short of the write.
func unblockedBody(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	s := &server{}
	r := httptest.NewRequest("POST", "/api/merge/01HMERGE/unblocked", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMergeUnblocked(w, r)
	return w
}

func TestTheUnblockDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	// `reason` is the plausible misspelling: it is the word the queue's own
	// refusals use, so it is the one an agent reaches for.
	w := unblockedBody(t, `{"reason":"rebased it"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field was answered with %d, not 400 - the claim was dropped "+
			"and the block cleared with no account of itself", w.Code)
	}
	if !strings.Contains(w.Body.String(), "reason") {
		t.Errorf("the refusal does not name the field: %s", w.Body.String())
	}
}

func TestTheUnblockDoorNeverTakesTheLock(t *testing.T) {
	src, err := os.ReadFile("api_mergeunblocked.go")
	if err != nil {
		t.Fatalf("read api_mergeunblocked.go: %v", err)
	}
	text := string(src)

	// The CALL shape rather than the bare name, for the reason
	// api_mergerenew_test.go gives: this file's comments explain why taking is
	// forbidden, and a test that tripped on the explanation would teach the next
	// author to delete the reasoning instead of keeping the rule.
	//
	// SetMergeGate is in the list because it is the tempting shortcut: a
	// declaration does clear a skip, so "just declare" looks like the same fix
	// in fewer lines. It is not - it takes the target for fifteen minutes, which
	// is the exact thing that was unavailable to the seat that had done the
	// work, and it clears the verdict fields with it.
	for _, forbidden := range []string{"s.db.TakeMergeLock(", "s.db.SetMergeGate("} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the withdrawal door calls %s - the act that retires a false reason "+
				"must not require the lock the reason is keeping somebody from", forbidden)
		}
	}
	if !strings.Contains(text, "SetMergeUnblocked") {
		t.Error("the withdrawal door does not call SetMergeUnblocked")
	}
}
