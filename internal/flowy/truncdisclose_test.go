package flowy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAListingSaysWhenItFilled asks ONE door TWICE and varies only the limit.
//
// One reading cannot tell a door that discloses truncation from one that always
// sets the flag, or from one that never does - so the assertion is the
// DIFFERENCE between a page that filled and a page that did not, over the same
// rows in the same project.
func TestAListingSaysWhenItFilled(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	project := "trunc-door-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}
	s := &server{db: db, node: "test-node"}

	const rows = 4
	for i := 0; i < rows; i++ {
		art := &store.Artifact{
			ID: ulid.NewString(), Type: store.MemoryType, Kind: "todo",
			Project: &project, Title: "row for the truncation check",
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("write artifact %d: %v", i, err)
		}
	}

	ask := func(limit string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet,
			"/api/artifacts?type=memory&kind=todo&limit="+limit, nil)
		r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
		w := httptest.NewRecorder()
		s.handleListArtifacts(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("limit=%s: status %d, body %s", limit, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("limit=%s: decode: %v", limit, err)
		}
		return body
	}

	// A page smaller than the set: it filled, so there may be more past it.
	cut := ask("2")
	if got, _ := cut["truncated"].(bool); !got {
		t.Errorf("a page of 2 over %d rows did not say it filled.\n"+
			"Without that flag this answer is indistinguishable from the whole set, "+
			"which is how two readers reported a page as a fact. body=%v", rows, cut)
	}
	if note, _ := cut["truncated_note"].(string); note == "" {
		t.Errorf("the flag is set and nothing says what to do about it - a reader " +
			"that learns its answer was cut still needs the next move")
	}

	// A page larger than the set: it did not fill, so the flag is ABSENT -
	// absent rather than false, for the reason discloseTruncation states.
	whole := ask("50")
	if _, said := whole["truncated"]; said {
		t.Errorf("a page of 50 over %d rows claimed it filled: %v", rows, whole)
	}

	// And the witness that the two readings differ at all. If the door ignored
	// `limit` entirely both answers would be the same, and the assertions above
	// would be agreeing with a door that never looked at the parameter.
	cutList, _ := cut["artifacts"].([]any)
	wholeList, _ := whole["artifacts"].([]any)
	if len(cutList) == len(wholeList) {
		t.Fatalf("both pages held %d rows, so `limit` changed nothing and this test "+
			"measured a door that is not honouring the parameter it is about",
			len(cutList))
	}
}
