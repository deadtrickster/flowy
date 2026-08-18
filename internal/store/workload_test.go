package store

import "testing"

// row is one open todo carrying a name, or nobody.
func row(assignee string) *Artifact {
	a := &Artifact{Type: MemoryType, Kind: "todo", Status: "todo"}
	if assignee != "" {
		a.Fields = []byte(`{"assignee":"` + assignee + `"}`)
	}
	return a
}

// THE THRESHOLDS ARE THE OPERATOR'S, so the cases are theirs: over half is
// "check wtf is going on", over 80% is "stop and rebalance". The numbers below
// are chosen to sit either side of each line rather than comfortably inside it -
// a probe tested only at 10% and 90% would pass with the comparison written
// backwards.
func TestWorkloadNamesWhatToDoAboutTheShare(t *testing.T) {
	for _, c := range []struct {
		name    string
		rows    []*Artifact
		want    string
		top     string
		wantTop float64
	}{{
		name: "an even board is ok",
		rows: []*Artifact{row("a"), row("b"), row("a"), row("b")},
		want: "ok", top: "a", wantTop: 0.5,
	}, {
		// Exactly half is NOT over half. The operator said "more than 50%".
		name: "half is not more than half",
		rows: []*Artifact{row("a"), row("a"), row("b"), row("b")},
		want: "ok", top: "a", wantTop: 0.5,
	}, {
		name: "past half says check",
		rows: []*Artifact{row("a"), row("a"), row("a"), row("b")},
		want: "check", top: "a", wantTop: 0.75,
	}, {
		name: "past eighty says rebalance",
		rows: []*Artifact{row("a"), row("a"), row("a"), row("a"), row("a"), row("b")},
		want: "rebalance", top: "a", wantTop: 5.0 / 6.0,
	}, {
		// One participant carrying everything is not a finding: 100% is the only
		// number they could have had.
		name: "one carrier is alone, not unbalanced",
		rows: []*Artifact{row("a"), row("a"), row("a")},
		want: "alone", top: "a", wantTop: 1,
	}, {
		name: "an empty board is empty",
		rows: nil,
		want: "empty",
	}} {
		got := WorkloadOf(c.rows)
		if got.Verdict != c.want {
			t.Errorf("%s: verdict %q, want %q (top %s at %.2f)", c.name, got.Verdict, c.want, got.Top, got.TopShare)
		}
		if c.top != "" && got.Top != c.top {
			t.Errorf("%s: top is %q, want %q", c.name, got.Top, c.top)
		}
		if c.wantTop != 0 && (got.TopShare < c.wantTop-0.001 || got.TopShare > c.wantTop+0.001) {
			t.Errorf("%s: top share %.4f, want %.4f", c.name, got.TopShare, c.wantTop)
		}
	}
}

// UNOWNED ROWS ARE IN THE DENOMINATOR, and this is the case that says so: the
// same three rows carried by one seat read as 100% among claimed rows and 50%
// of the board. The second is the true one - work nobody has taken is still
// work this fleet is carrying, and leaving it out would raise somebody's share
// every time a row is filed that nobody claims.
func TestUnownedRowsCountInTheDenominator(t *testing.T) {
	rows := []*Artifact{row("a"), row("a"), row("a"), row(""), row(""), row("")}
	got := WorkloadOf(rows)
	if got.Open != 6 || got.Unowned != 3 {
		t.Fatalf("open %d unowned %d, want 6 and 3", got.Open, got.Unowned)
	}
	if got.TopShare < 0.499 || got.TopShare > 0.501 {
		t.Errorf("top share %.4f, want 0.5 - unowned rows dropped out of the denominator", got.TopShare)
	}
	if got.Verdict != "check" && got.Verdict != "ok" {
		t.Errorf("verdict %q on an exactly-half board", got.Verdict)
	}
}

// A finished row is not work. Without this the number only ever climbs, and the
// probe reports a board that was rebalanced hours ago.
func TestDoneRowsAreNotWork(t *testing.T) {
	done := row("a")
	done.Status = DoneStatus
	got := WorkloadOf([]*Artifact{done, row("b")})
	if got.Open != 1 {
		t.Fatalf("open %d, want 1 - a done row counted as work", got.Open)
	}
	if got.Top != "b" {
		t.Errorf("top is %q, want b", got.Top)
	}
}
