package flowy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	} else if note, _ := cut["truncated_note"].(string); note == "" {
		// Only worth asking once the flag IS set. Asked unconditionally, this
		// fires on a missing flag too and reports "the flag is set and nothing
		// says what to do about it" about an answer that set no flag - which is
		// the check describing a state that is not the one it found.
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

// TestDiscloseCutUsesBothSignals pins the three outcomes against each other,
// because the interesting one cannot be reached by the count alone.
//
// The count and the page are separate statements with no snapshot around them,
// and on an append-only stream the count is the earlier reading - so a set that
// crosses the limit between them leaves total <= got with the page full. A
// disclosure that only compared the count against what it received would say
// nothing there, on the busiest stream, which is the one being tailed.
func TestDiscloseCutUsesBothSignals(t *testing.T) {
	// The count proves rows were left behind: say how many.
	exact := map[string]any{}
	discloseCut(exact, 3, 9, 3)
	if got, _ := exact["truncated"].(bool); !got {
		t.Errorf("3 of 9 did not disclose: %v", exact)
	}
	if note, _ := exact["truncated_note"].(string); !strings.Contains(note, "3 of 9") {
		t.Errorf("the count proved 6 rows are missing and the note does not say so: %q", note)
	}

	// THE RACE. The count was taken before the page and under-reports, so it
	// proves nothing - but the page is full, which does.
	raced := map[string]any{}
	discloseCut(raced, 400, 400, 400)
	if got, _ := raced["truncated"].(bool); !got {
		t.Errorf("a full page with a count that proves nothing did not disclose - this is "+
			"the false negative the second signal exists for: %v", raced)
	}
	if note, _ := raced["truncated_note"].(string); strings.Contains(note, "of 400") {
		t.Errorf("the hedged case claimed a number it cannot stand behind: %q", note)
	}

	// Neither: a short page under its limit, with the count agreeing.
	whole := map[string]any{}
	discloseCut(whole, 2, 2, 400)
	if _, said := whole["truncated"]; said {
		t.Errorf("a short page claimed it was cut: %v", whole)
	}

	// And the difference that makes the first two distinguishable at all.
	if exact["truncated_note"] == raced["truncated_note"] {
		t.Errorf("the exact and hedged notes are identical, so a reader cannot tell a "+
			"counted shortfall from a full page: %q", exact["truncated_note"])
	}
}

// TestReadyDisclosesThePageNotTheFilteredSubset pins the ordering inside
// handleReady: ?ready=true narrows the rows AFTER the store has cut the page, so
// the question "was the page cut" has to be asked of what the store handed over.
//
// The discriminator is a full page that the filter empties. Three unassigned
// todos are all not-ready, so ?ready=true with limit=2 returns NO items over a
// page that filled - and a check comparing the filtered length against the page
// size would see 0 < 2 and call the answer complete.
func TestReadyDisclosesThePageNotTheFilteredSubset(t *testing.T) {
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

	project := "ready-page-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}
	s := &server{db: db, node: "test-node"}

	for i := 0; i < 3; i++ {
		art := &store.Artifact{
			ID: ulid.NewString(), Type: store.MemoryType, Kind: "todo",
			Project: &project, OwnerUser: p.UserID,
			Title: "unassigned, so not ready",
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("write todo %d: %v", i, err)
		}
	}

	ask := func(target string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
		w := httptest.NewRecorder()
		s.handleReady(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", target, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode: %v", target, err)
		}
		return body
	}

	// The page filled and the filter took everything off it.
	filtered := ask("/api/ready?limit=2&ready=true")
	if n, _ := filtered["count"].(float64); n != 0 {
		t.Fatalf("expected the ready filter to empty a page of unassigned todos, got count %v - "+
			"the fixture is wrong and the assertion below would be about nothing", n)
	}
	if got, _ := filtered["truncated"].(bool); !got {
		t.Errorf("a page that filled at 2 and was then emptied by ?ready=true did not "+
			"disclose: %v\nThis is the ordering the handler exists to get right - the "+
			"filtered length is the size of a subset of a page, not the page.", filtered)
	}

	// And the same page unfiltered, which is the reading that would pass either way.
	unfiltered := ask("/api/ready?limit=2")
	if got, _ := unfiltered["truncated"].(bool); !got {
		t.Errorf("a page of 2 over three todos did not disclose: %v", unfiltered)
	}

	// A page larger than the set says nothing, filtered or not - the witness that
	// the flag is about the limit and not about the filter.
	whole := ask("/api/ready?limit=50&ready=true")
	if _, said := whole["truncated"]; said {
		t.Errorf("a page of 50 over three todos claimed it was cut: %v", whole)
	}
}
