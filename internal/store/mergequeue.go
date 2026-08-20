package store

import (
	"fmt"
	"strings"
)

// THE MERGE QUEUE: one door to the target, and the door refuses.
//
// A merge queue is not a list of branches waiting their turn. The list is the
// cheap part and it is not what the queue is for. What it is for is one rule
// that has been broken by hand four times today:
//
//	A BRANCH MAY ONLY LAND ON THE TIP ITS GATE ACTUALLY MEASURED.
//
// Every branch here was gated alone, green, and correct about itself - and a
// union of two green branches is not green, because neither gate ever saw the
// other's code. Master went red twice today with every contributing branch
// passing. That is not carelessness, it is arithmetic: N branches gated against
// tip T all merge into a tip that is no longer T, and the first one to land
// invalidates the evidence of every branch behind it.
//
// So the queue records WHAT TIP THE GATE MEASURED, and admission compares that
// against the tip the merge would actually land on. When they differ the answer
// is not a warning printed next to a merge that happens anyway - it is a
// REFUSAL, naming both tips. Advisory rules lose; today proved that about four
// separate advisory rules, including two I wrote myself.
//
// WHY THIS IS NOT A NEW DAG. Order between merges is dependency, and dependency
// is already an event with provenance, already permission-filtered, already
// computed per reader - see deps.go, whose reasoning about hanging the edge off
// the DEPENDENT is exactly as load-bearing here. A merge item is a work item;
// what blocks it is a dep edge. Building a second graph beside that one would
// mean two answers to "what is ready" and no way to tell which is lying.
//
// WHAT LIVES IN FIELDS. Branch, target and the gated tip ride fields the way
// as_of and supersedes ride a report: they are attributes of this one item, not
// relations, and none of them is a column any other read needs to join on.
const (
	// BranchField names the branch this item would land.
	BranchField = "branch"
	// TargetField names what it lands ON - master, unless something says
	// otherwise. A queue with an implicit target is one that cannot hold two
	// release lines at once, and that is a limit worth not building in.
	TargetField = "target"
	// GatedTipField is THE COMMIT THE GATE ACTUALLY MEASURED - the tip the
	// branch contained when its checks ran, not the tip it was branched from
	// and not the tip it hopes to land on.
	GatedTipField = "gated_tip"
	// GateRunField names the run that produced the verdict, so a claim of
	// green points at the log that says so rather than at somebody's memory
	// of it. A verdict with no run behind it is a self-report.
	GateRunField = "gate_run"
	// GatedBaseField is WHAT THE TARGET WAS WHEN THE RUN STARTED, which is a
	// different fact from GatedTipField and was being asked of it.
	//
	// One name was carrying two questions. The land door asks "did you test
	// what you are landing", which is gated_tip against the sha being landed.
	// The queue asks "has the ground moved since you measured", which is this
	// against the target's tip right now. For a fast-forward those can never
	// both hold: the branch tip B contains master M, so B != M by
	// construction, and demanding one value satisfy both is demanding B == M.
	//
	// The visible symptom was a queue where every pending item read
	// inadmissible and every landed one read admissible, because gated_tip
	// only equals the target's tip AFTER the fast-forward has happened. The
	// column answered "has this landed?" while claiming to answer "may this
	// land?".
	GatedBaseField = "gated_base"
)

// DefaultMergeTarget is where work lands when an item does not say.
const DefaultMergeTarget = "master"

// MergeKind is the work kind a merge queue item carries. It is a work kind, so
// it has a status and a lifecycle exactly as a todo does - a merge request that
// could not be moved through states would be a row nothing can drain.
const MergeKind = "merge"

// A commit is compared as an opaque token, so the queue does not need git: it
// needs to know whether two readings of "the tip" are THE SAME READING. Case and
// surrounding whitespace are noise from whatever printed it.
func normalizeTip(tip string) string {
	return strings.ToLower(strings.TrimSpace(tip))
}

// minTipLen is how much of a sha has to be present to be worth comparing.
//
// Seven is git's own abbreviation floor and the length every tool here prints.
// Shorter than that is not a commit anybody typed on purpose, and treating it as
// a prefix would make one branch match half the repository.
const minTipLen = 7

// sameCommit reports whether two readings name the same commit.
//
// A PREFIX MATCH, NOT A STRING EQUALITY, and it cost two refused landings to
// learn: claude-host recorded 9e31abb from `git log --oneline` and the node read
// master as 9e31abb4ecd5..., so the queue refused two green branches for
// "measured a different tip" when they had measured exactly the right one.
//
// Both forms are correct readings of one commit - git prints whichever length
// the caller asked for - so a comparison that demands identical strings is not
// checking what it claims to check. Requiring minTipLen on both sides is what
// keeps this from degenerating into "matches anything".
func sameCommit(a, b string) bool {
	a, b = normalizeTip(a), normalizeTip(b)
	if len(a) < minTipLen || len(b) < minTipLen {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

// BranchOf, TargetOf, GatedTipOf and GateRunOf read what an item was filed with.
// A missing target reads as the default rather than as empty, because every
// merge lands somewhere and a reader that has to remember the default is a
// reader that will forget it.
func BranchOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, BranchField)) }

func TargetOf(a *Artifact) string {
	if t := strings.TrimSpace(artifactString(a, TargetField)); t != "" {
		return t
	}
	return DefaultMergeTarget
}

func GatedTipOf(a *Artifact) string { return normalizeTip(artifactString(a, GatedTipField)) }

// GatedBaseOf is the target tip the gate ran from, or "" for a row gated
// before the field existed - which is the whole compatibility story: an item
// with no base is judged the old way rather than refused for lacking a field
// nobody could have written.
func GatedBaseOf(a *Artifact) string { return normalizeTip(artifactString(a, GatedBaseField)) }

func GateRunOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, GateRunField)) }

// artifactString is artifactField narrowed to the string case, which is what
// every field here is. A field holding a number or an object is not a branch
// name, and reading it as an empty one is the answer that makes admission refuse
// rather than the answer that makes it proceed on nonsense.
func artifactString(a *Artifact, key string) string {
	named := artifactField(a, key)
	if named == nil {
		return ""
	}
	s, _ := named.(string)
	return s
}

// ErrMergeNotAdmissible is what a refused admission is, so a caller can tell a
// queue that said no from a queue that could not be asked. The message carries
// both tips: a refusal that does not say what it compared is one the reader
// cannot act on, and "re-gate it" is not actionable without knowing against
// what.
type ErrMergeNotAdmissible struct {
	Item      string
	Branch    string
	Target    string
	GatedTip  string
	TargetTip string
	Reason    string
	// Code names WHICH refusal this is, as a stable token, so a row can be
	// written against it and handed to the reader who just hit it - see
	// knownissue.go, which holds the codes, the lookup and the method that
	// reads this field. Reason stays the sentence for a person; a lookup keyed
	// on prose would unhook every row the day somebody reworded it.
	//
	// A refusal added here should get one. Nothing breaks without it: an empty
	// code resolves to no row, which is what every door did before this
	// existed.
	Code string
}

func (e *ErrMergeNotAdmissible) Error() string {
	if e.GatedTip == "" {
		return fmt.Sprintf("merge %s (%s -> %s) is not admissible: %s",
			e.Item, e.Branch, e.Target, e.Reason)
	}
	return fmt.Sprintf("merge %s (%s -> %s) is not admissible: %s - it was gated on %s and %s is now at %s, so re-gate it on %s",
		e.Item, e.Branch, e.Target, e.Reason, e.GatedTip, e.Target, e.TargetTip, e.TargetTip)
}

// MergeAdmissible answers whether this item may land on targetTip RIGHT NOW.
//
// It is deliberately a pure function of the item and one string: the caller
// reads the tip from git, the queue decides, and the decision is testable
// without a repository, a database or a VM. The three refusals, in the order
// they are asked:
//
//   - the item is not a merge request at all
//   - it carries no gated tip, so nothing measured it. UNGATED IS NOT A SMALL
//     PROBLEM WITH THIS ITEM, it is the absence of the evidence the queue exists
//     to check, and it is refused in the same voice as a stale one.
//   - it was gated on a tip that is not where it would land. This is the one
//     that has cost the day.
//
// A green verdict against the tip it will actually land on is the only thing
// that admits. Note what is NOT asked: whether the branch is behind, whether it
// merges cleanly, whether anybody approved it. Those are git's answers and the
// room's answers. This function has exactly one opinion.
func MergeAdmissible(a *Artifact, targetTip string) error {
	if a == nil || a.Kind != MergeKind {
		return &ErrMergeNotAdmissible{
			Item:   idOf(a),
			Code:   RefusalMergeNotAnItem,
			Reason: "it is not a merge queue item",
		}
	}
	tip := normalizeTip(targetTip)
	if tip == "" {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a),
			Code:   RefusalMergeTipUnstated,
			Reason: "the tip it would land on was not stated, and a comparison against nothing always passes",
		}
	}
	// A ROW THAT HAS LANDED IS NOT A STALE ROW, and asking the staleness
	// question of one produces an answer that is true and useless.
	//
	// Measured 2026-08-20 on 01M0ESQD8Q, status done, landed_tip e6f1121,
	// master e6f1121:
	//
	//   not admissible: the target moved after its gate ran - it measured from
	//   19daf10 - it was gated on e6f1121 and master is now at e6f1121, so
	//   re-gate it on e6f1121
	//
	// The target did move: this row landed on it. gated_base is what master was
	// before, so EVERY landed row satisfies the moved-target test by
	// definition, and the advice is to spend five minutes re-gating something
	// with nothing left to do. A reader took it as a stall in the queue and
	// started diagnosing one.
	//
	// ASKED BEFORE THE GATE QUESTIONS, because "already landed" is true whether
	// or not a verdict was recorded. A closed row with no gated tip would
	// otherwise be refused as ungated, which is the same wrong answer wearing
	// the other code - so the ORDER is part of the fix and is asserted.
	if DoneAt(a) {
		fields, _ := ArtifactFields(a)
		landed, _ := fields[LandedTipField].(string)
		reason := "it has already landed"
		if landed != "" {
			reason += " as " + landed
		}
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a), TargetTip: tip,
			Code:   RefusalMergeLanded,
			Reason: reason,
		}
	}
	gated := GatedTipOf(a)
	if gated == "" {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a), TargetTip: tip,
			Code:   RefusalMergeUngated,
			Reason: "no gate has measured it - there is no verdict to be stale",
		}
	}
	// WHICH FACT IS COMPARED depends on which one the row carries.
	//
	// A row with a base was gated from a known target tip, and the question is
	// whether the target has moved since - base against tip. A row without one
	// predates the field, and the only comparison available is the old
	// gated_tip against tip. Judging an old row by the new rule would refuse
	// every row written before today for missing a field nobody could have
	// written, so the fallback is not politeness, it is the difference between
	// a migration and an outage.
	if base := GatedBaseOf(a); base != "" {
		if !sameCommit(base, tip) {
			return &ErrMergeNotAdmissible{
				Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a),
				GatedTip: gated, TargetTip: tip,
				Code:   RefusalMergeStaleGate,
				Reason: "the target moved after its gate ran - it measured from " + base,
			}
		}
		return nil
	}
	if !sameCommit(gated, tip) {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a),
			GatedTip: gated, TargetTip: tip,
			Code:   RefusalMergeStaleGate,
			Reason: "its gate measured a different tip",
		}
	}
	return nil
}

// idOf keeps the refusal readable when the thing handed in is not an artifact at
// all, which is a caller bug and should still produce a sentence rather than a
// panic.
func idOf(a *Artifact) string {
	if a == nil {
		return "(none)"
	}
	if a.ID == "" {
		return "(unidentified)"
	}
	return a.ID
}
