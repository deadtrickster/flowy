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
			Reason: "it is not a merge queue item",
		}
	}
	tip := normalizeTip(targetTip)
	if tip == "" {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a),
			Reason: "the tip it would land on was not stated, and a comparison against nothing always passes",
		}
	}
	gated := GatedTipOf(a)
	if gated == "" {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a), TargetTip: tip,
			Reason: "no gate has measured it - there is no verdict to be stale",
		}
	}
	if gated != tip {
		return &ErrMergeNotAdmissible{
			Item: a.ID, Branch: BranchOf(a), Target: TargetOf(a),
			GatedTip: gated, TargetTip: tip,
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
