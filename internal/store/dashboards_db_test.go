package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The wire: create, upsert and set-fields actually ask the dashboard and
// metric shape checks, the way openspec's are proven. The checks themselves
// are asked directly in dashboards_test.go.

func dashboardRowIn(t *testing.T, ctx context.Context, db *DB, p *Principal,
	project string, tiles any,
) *Artifact {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"tiles": tiles})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: DashboardKind,
		Project: &project, OwnerUser: p.UserID, Title: "the dashboard", Fields: raw,
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("write dashboard: %v", err)
	}
	return art
}

func TestCreateArtifactRefusesAHuskDashboard(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "dash-create")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	for _, tiles := range []any{
		nil,
		[]any{},
		[]map[string]any{{"kind": "bar", "label": "trend", "metric": "cells"}},
	} {
		raw := []byte("")
		if tiles != nil {
			var err error
			if raw, err = json.Marshal(map[string]any{"tiles": tiles}); err != nil {
				t.Fatalf("fields: %v", err)
			}
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: DashboardKind,
			Project: &project, OwnerUser: p.UserID, Title: "husk", Fields: raw,
		}
		err := db.CreateArtifact(ctx, art)
		if err == nil {
			t.Fatalf("a dashboard with tiles %v was written - a page that declares nothing must not exist", tiles)
		}
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal the doors map to 400: %v", err, err)
		}
	}
}

func TestCreateArtifactRefusesANamelessReading(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "dash-metric")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	for name, fields := range map[string]map[string]any{
		"no fields":  nil,
		"no name":    {"value": 1200},
		"no value":   {"name": "cells"},
		"null value": {"name": "cells", "value": nil},
	} {
		raw := []byte("")
		if fields != nil {
			var err error
			if raw, err = json.Marshal(fields); err != nil {
				t.Fatalf("fields: %v", err)
			}
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: MetricKind,
			Project: &project, OwnerUser: p.UserID, Title: "reading", Fields: raw,
		}
		err := db.CreateArtifact(ctx, art)
		if err == nil {
			t.Fatalf("a reading with %s was written - a metric that carries nothing must not exist", name)
		}
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal the doors map to 400: %v", err, err)
		}
	}
}

func TestUpsertArtifactRefusesToStripTheTiles(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "dash-upsert")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := dashboardRowIn(t, ctx, db, p, project, []map[string]any{
		{"kind": "number", "label": "cells done", "metric": "cells"},
	})

	// The same row, restated with the tiles dropped. An upsert must refuse
	// it, not turn the page blank.
	husked := *art
	husked.Fields = nil
	if err := db.UpsertArtifact(ctx, &husked); err == nil {
		t.Fatal("an upsert that drops the tiles was accepted")
	} else {
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal: %v", err, err)
		}
	}

	// And the stored row still declares what it did - a refusal writes
	// nothing.
	stored, err := db.ReadArtifact(ctx, p, art.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	tiles, err := DashboardTilesOf(stored)
	if err != nil || len(tiles) != 1 {
		t.Fatalf("the stored row's tiles read %v (%v) - the refusal must not husk the row", tiles, err)
	}
}

// TestPruneSeriesHoldsASeriesDown runs the prune against a real database,
// because a SQL string that only ever compiles is not a query anybody has run.
// The prune is amortised - it fires once a series is past TWICE its allowance -
// so the arms below are written around that threshold rather than around the
// allowance itself.
func TestPruneSeriesHoldsASeriesDown(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "dash-prune")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	name := "prune.series." + ulid.NewString()

	push := func(i int, retain any) {
		t.Helper()
		f := map[string]any{"name": name, "value": i}
		if retain != nil {
			f["retain"] = retain
		}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: MetricKind,
			Project: &project, OwnerUser: p.UserID, Title: name, Fields: raw,
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := db.sql.QueryRowContext(ctx,
			`SELECT count(*) FROM artifacts
			  WHERE coalesce(tombstone, false) = false AND kind = $1 AND fields->>'name' = $2`,
			MetricKind, name).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// UNDER TWICE THE ALLOWANCE, NOTHING IS DELETED. A series sitting at its
	// limit must not be rewritten on every single push.
	for i := 1; i <= 6; i++ {
		push(i, map[string]any{"points": 3})
	}
	if got := count(); got != 6 {
		t.Fatalf("at twice the allowance the series is left alone, got %d rows, want 6", got)
	}

	// PAST IT, THE SERIES IS CUT BACK TO THE ALLOWANCE ITSELF - not to twice it.
	push(7, map[string]any{"points": 3})
	if got := count(); got != 3 {
		t.Fatalf("past twice the allowance the series is cut to it, got %d rows, want 3", got)
	}

	// AND WHAT SURVIVED IS THE NEWEST, which is the half a count alone cannot
	// prove: a prune that kept the OLDEST three would pass the arm above.
	got, err := db.SeriesOf(ctx, p, []string{name}, 10)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 1 || len(got[0].Points) != 3 {
		t.Fatalf("expected one series of three points, got %+v", got)
	}
	var kept []string
	for _, pt := range got[0].Points {
		kept = append(kept, string(pt.Value))
	}
	if strings.Join(kept, ",") != "5,6,7" {
		t.Fatalf("the newest three survive, oldest first - got %v", kept)
	}

	// A SERIES THAT SAYS NOTHING IS NOT PRUNED AT SIX ROWS, because its default
	// allowance is far above them. Same door, different producer.
	quiet := "prune.quiet." + ulid.NewString()
	for i := 1; i <= 6; i++ {
		raw, err := json.Marshal(map[string]any{"name": quiet, "value": i})
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: MetricKind,
			Project: &project, OwnerUser: p.UserID, Title: quiet, Fields: raw,
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("push quiet %d: %v", i, err)
		}
	}
	var n int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM artifacts
		  WHERE coalesce(tombstone, false) = false AND kind = $1 AND fields->>'name' = $2`,
		MetricKind, quiet).Scan(&n); err != nil {
		t.Fatalf("count quiet: %v", err)
	}
	if n != 6 {
		t.Fatalf("a series under the default allowance keeps everything, got %d rows, want 6", n)
	}
}
