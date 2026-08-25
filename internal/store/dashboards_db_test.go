package store

import (
	"context"
	"encoding/json"
	"errors"
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
