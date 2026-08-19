package store

// HOW THE WORK IS SPREAD, computed where the rows are.
//
// The operator, 2026-08-18: "add a new probe - the task distribution - you dont
// hold it yourself - orchestrator is assigned to almost everything as far as i
// see. The protocol is simple - if somebody assigned to more than 50% of tasks
// - check wtf is going on - if more than 80% of tasks - stop and rebalance."
//
// They were right, and the board said so the moment anybody asked it: 34 open
// rows, orchestrator carrying 20 of them - 59%, over the first threshold, with
// nobody in a position to notice because the seat that would notice was the
// seat holding them.
//
// WHY IT IS NOT HELD BY THE ONE IT MEASURES. That is the operator's own
// framing and it is the whole design: a probe that a busy agent has to run
// about itself is a probe that reports last when it matters most. Computing it
// here means every reader gets the same answer from the same rows, and the nag
// becomes an HTTP call rather than a jq pipeline in four different scripts.
//
// THE DENOMINATOR IS THE PART THAT GOES WRONG, so it is stated once here: OPEN
// rows - not done, not withdrawn - and UNOWNED rows count in it. They are work
// this fleet is carrying whether or not a name is on them, and leaving them out
// would make one seat's share rise every time somebody files a row nobody has
// taken, which is the opposite of what the number is for.

import (
	"sort"
)

// WorkloadShare is one participant's part of the open board.
type WorkloadShare struct {
	Assignee string  `json:"assignee"`
	Open     int     `json:"open"`
	Share    float64 `json:"share"`
}

// Workload is how the open rows are spread, and what to do about it.
//
// Verdict is the operator's protocol, in their words: "ok" below half, "check"
// past it, "rebalance" past 80%. It names the state rather than describing it,
// because three surfaces read this and a sentence they each paraphrase is three
// slightly different rules.
type Workload struct {
	Open     int             `json:"open"`
	Unowned  int             `json:"unowned"`
	Shares   []WorkloadShare `json:"shares"`
	Top      string          `json:"top"`
	TopShare float64         `json:"top_share"`
	Verdict  string          `json:"verdict"`
	// Threshold is the check line, kept under its old name so nothing that
	// already reads it breaks.
	Threshold float64 `json:"threshold"`
	// Check and Rebalance are BOTH lines, reported because both are applied.
	//
	// The answer used to carry one number while the verdict was decided by two,
	// so a reader seeing `rebalance` beside `threshold: 0.5` could not tell
	// which line had been crossed - and the difference is the whole difference
	// between "look at this" and "hand some back", which is what the operator
	// asked for in those words.
	Check     float64 `json:"check"`
	Rebalance float64 `json:"rebalance"`
}

// The two thresholds, named rather than written twice.
const (
	WorkloadCheck     = 0.50
	WorkloadRebalance = 0.80
)

// WorkloadOf folds a set of rows into the share each participant carries.
//
// It takes rows rather than reading them so that the arithmetic can be tested
// without a database - the defect that hid in GatingAt for a day was invisible
// for exactly the opposite reason.
func WorkloadOf(rows []*Artifact) Workload {
	counts := map[string]int{}
	w := Workload{
		Threshold: WorkloadCheck,
		Check:     WorkloadCheck,
		Rebalance: WorkloadRebalance,
	}
	for _, a := range rows {
		if a == nil || DoneAt(a) {
			continue
		}
		w.Open++
		who := AssigneeOf(a)
		if who == "" || NobodyName(who) {
			w.Unowned++
			continue
		}
		counts[who]++
	}
	for who, n := range counts {
		// The denominator is every open row, unowned included. See the head
		// comment: they are work in flight and a share computed over claimed
		// rows alone would rise as the board fills with rows nobody has taken.
		w.Shares = append(w.Shares, WorkloadShare{
			Assignee: who, Open: n, Share: float64(n) / float64(w.Open),
		})
	}
	sort.Slice(w.Shares, func(i, j int) bool {
		if w.Shares[i].Open != w.Shares[j].Open {
			return w.Shares[i].Open > w.Shares[j].Open
		}
		return w.Shares[i].Assignee < w.Shares[j].Assignee
	})
	if len(w.Shares) > 0 {
		w.Top, w.TopShare = w.Shares[0].Assignee, w.Shares[0].Share
	}
	// ONE PARTICIPANT AND NOTHING UNCLAIMED NEVER FLAGS. flowy-claude's point,
	// narrowed by a test that caught it too wide: 100% is the only number a lone
	// carrier can have, so it is not a finding - but a board where one seat holds
	// three rows and three more sit unowned is NOT that case. Their share could
	// have come out differently the moment anybody took one, which is precisely
	// what makes it a measurement rather than an artefact of the arithmetic.
	switch {
	case w.Open == 0:
		w.Verdict = "empty"
	case len(w.Shares) < 2 && w.Unowned == 0:
		w.Verdict = "alone"
	case w.TopShare > WorkloadRebalance:
		w.Verdict = "rebalance"
	case w.TopShare > WorkloadCheck:
		w.Verdict = "check"
	default:
		w.Verdict = "ok"
	}
	return w
}

// DoneAt reports whether a row is finished, by the one word every surface here
// uses for it. Split out so WorkloadOf reads the same rule the board does.
func DoneAt(a *Artifact) bool {
	return a.Status == DoneStatus || a.Tombstone
}
