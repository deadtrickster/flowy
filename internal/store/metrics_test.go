package store

import "testing"

// The rule the anomaly pass exists to keep: below MetricMinSamples readings
// there is no verdict, and the answer says so with the count rather than
// reporting "normal" - which is what a dashboard with no history would
// otherwise show for a node nobody has ever looked at.
func TestJudgeRefusesBelowTheMinimum(t *testing.T) {
	for n := 0; n < MetricMinSamples; n++ {
		history := make([]float64, n)
		for i := range history {
			history[i] = 10
		}
		got := Judge("corpus.artifacts", 900, history)
		if got.Verdict != VerdictInsufficient {
			t.Fatalf("with %d readings the verdict is %q, want %q",
				n, got.Verdict, VerdictInsufficient)
		}
		if got.Samples != n || got.Required != MetricMinSamples {
			t.Fatalf("with %d readings it reported %d of %d", n, got.Samples, got.Required)
		}
		if got.Reason == "" {
			t.Fatalf("with %d readings it refused without saying why", n)
		}
		// A refusal must not carry a baseline: a number beside the word
		// "insufficient" is a number somebody will read as the finding.
		if got.Baseline != 0 || got.Z != 0 {
			t.Fatalf("a refusal carried a baseline %g and a z of %g", got.Baseline, got.Z)
		}
	}
}

// With enough history the verdict is a distance from what this node has
// actually seen, and not a threshold anybody chose.
func TestJudgeComparesAgainstRecordedHistory(t *testing.T) {
	history := []float64{10, 11, 9, 10, 12, 8, 10, 11, 10, 9}

	normal := Judge("collab.messages_24h", 11, history)
	if normal.Verdict != VerdictNormal {
		t.Fatalf("11 against a baseline of ~10 is %q, want %q", normal.Verdict, VerdictNormal)
	}
	if normal.Baseline == 0 || normal.Sigma == 0 {
		t.Fatalf("a verdict came back with no baseline: %+v", normal)
	}

	unusual := Judge("collab.messages_24h", 400, history)
	if unusual.Verdict != VerdictUnusual {
		t.Fatalf("400 against a baseline of ~10 is %q, want %q", unusual.Verdict, VerdictUnusual)
	}
	if unusual.Z < AnomalyZ {
		t.Fatalf("z is %g, want at least %g", unusual.Z, AnomalyZ)
	}
	if unusual.Reason == "" {
		t.Fatal("an unusual verdict did not say what it was unusual against")
	}
}

// A series that has never moved has no spread to be unusual against, so the
// same value is normal and a different one is the whole finding. Dividing by a
// zero sigma would otherwise report every reading as infinitely far out.
func TestJudgeHandlesAFlatSeries(t *testing.T) {
	flat := []float64{4, 4, 4, 4, 4, 4, 4, 4, 4}
	if got := Judge("perms.denied_24h", 4, flat); got.Verdict != VerdictNormal {
		t.Fatalf("the same value against a flat series is %q, want %q", got.Verdict, VerdictNormal)
	}
	got := Judge("perms.denied_24h", 5, flat)
	if got.Verdict != VerdictUnusual {
		t.Fatalf("a change against a flat series is %q, want %q", got.Verdict, VerdictUnusual)
	}
	if got.Reason == "" {
		t.Fatal("the flat-series verdict did not say what it rests on")
	}
}

// A search term is characters, not a pattern: somebody looking for "100%" is
// not asking for a wildcard, and a term that smuggled one in would widen a read
// rather than narrow it.
func TestLikeEscaped(t *testing.T) {
	for term, want := range map[string]string{
		"plain":            "plain",
		"100%":             `100\%`,
		"a_b":              `a\_b`,
		`back\ ` + "slash": `back\\ slash`,
	} {
		if got := likeEscaped(term); got != want {
			t.Errorf("likeEscaped(%q) = %q, want %q", term, got, want)
		}
	}
}

// The scope filter for spans is a per-principal rule, and a nil principal is
// nobody: it must read nothing at all rather than everything.
func TestSpanFilterRefusesNobody(t *testing.T) {
	a := &args{}
	if got := SpanFilterSQL(nil, "s", a, true); got != "FALSE" {
		t.Fatalf("SpanFilterSQL(nil) = %q, want FALSE", got)
	}
	if len(a.vals) != 0 {
		t.Fatalf("a refusing filter still bound %d parameters", len(a.vals))
	}
	// ?scope=all is the operator's alone, here as everywhere else.
	notOperator := &Principal{UserID: "u", Project: "pa"}
	b := &args{}
	if got := SpanFilterSQL(notOperator, "s", b, true); got == "TRUE" {
		t.Fatal("scope=all widened the span filter for a principal that is not the operator")
	}
	operator := &Principal{UserID: "u", Project: "pa", Operator: true}
	c := &args{}
	if got := SpanFilterSQL(operator, "s", c, true); got != "TRUE" {
		t.Fatalf("scope=all for the operator = %q, want TRUE", got)
	}
}

// The delivery span's id is derived from the row's, which is what makes
// applying the same delta twice one delivery rather than two.
func TestDeliverSpanIDIsStable(t *testing.T) {
	first := DeliverSpanID("01EVENT")
	if first != DeliverSpanID("01EVENT") {
		t.Fatal("the same event produced two span ids")
	}
	if first == DeliverSpanID("01OTHER") {
		t.Fatal("two events produced one span id")
	}
	if len(first) != 16 {
		t.Fatalf("a span id is %d hex digits, want 16", len(first))
	}
}

// The trace id rides an event's meta, and what comes back out has to be a trace
// id: meta is a jsonb column a peer can put anything in.
func TestTraceOfMeta(t *testing.T) {
	const good = "aabbccddeeff00112233445566778899"
	if got := TraceOfMeta([]byte(`{"trace":"` + good + `"}`)); got != good {
		t.Fatalf("TraceOfMeta gave %q, want %q", got, good)
	}
	for _, meta := range []string{
		``, `null`, `[]`, `"a string"`, `{}`, `{"trace":""}`, `{"trace":123}`,
		`{"trace":"not-a-trace"}`, `{"trace":"00000000000000000000000000000000"}`,
	} {
		if got := TraceOfMeta([]byte(meta)); got != "" {
			t.Errorf("TraceOfMeta(%s) = %q, want none", meta, got)
		}
	}
}
