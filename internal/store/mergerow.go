package store

import "fmt"

// A merge request that does not say which branch it would land.
//
// The rule itself is not new - mcp_merge.go has refused it since merge requests
// were filed through the memory door. What is new is WHERE it is asked. It was
// asked at one door, and this fleet has learned the same lesson from four
// different bugs today: a rule kept per surface is a rule the next surface
// forgets. POST /api/artifacts with type memory, kind merge and no branch went
// straight past it and wrote a row the queue can never land - BranchOf reads
// empty, so nothing can rebase it, gate it or fast-forward it - and it sits in
// the queue looking exactly like work somebody could pick up.
//
// It is asked beside checkQueueRow, at the same three statements and for the
// same stated reason: every local write of a row goes through one of them, and
// the signature is over the row, so a row that must not exist must not be
// signed into existence.
//
// SCOPE, deliberately: this refuses a merge request with no branch. It does not
// refuse branch, target, gated_tip or gate_run on rows that are not merge
// requests - that is an argument-level rule about what a caller MEANT, it lives
// in mergeFields where the arguments are, and nothing downstream reads those
// fields on a todo anyway. One rule, in the place that can enforce it for every
// door, is worth more than two rules that disagree about their reach.
//
// Measured before it was written: 115 merge rows on the live node, none of them
// branchless. So this refuses nothing that exists and nothing that has ever
// worked - it closes the door the bad row would come through next.

// MergeRowWithoutBranchError is a merge request filed with no branch.
//
// It names the field and the door, because a caller told only "refused" reads
// it as the node being broken rather than as one word missing from their write.
type MergeRowWithoutBranchError struct {
	Row string
}

func (e MergeRowWithoutBranchError) Error() string {
	return fmt.Sprintf("%s is a %s request and does not say which branch it would land: "+
		"send fields.%s. A merge request with no branch cannot be rebased, gated or "+
		"fast-forwarded, so it would sit in the queue looking like work nobody can do",
		e.Row, MergeKind, BranchField)
}

// depRefusal marks this as the caller's mistake rather than a broken node, so
// every door already maps it to a 400 - the same interface every other queue
// refusal satisfies, which is what keeps one refusal from arriving as three
// different kinds of failure.
func (e MergeRowWithoutBranchError) depRefusal() {}

// checkMergeRow is the invariant, asked of the row a statement is about to
// write.
//
// Anything that is not a merge request passes. The kind is read through
// EntityType, which is how every other reader asks this question of a memory
// row: identity is the kind when the type is memory, and the type otherwise.
func checkMergeRow(a *Artifact) error {
	if a == nil || !IsEntityType(a, MergeKind) {
		return nil
	}
	if BranchOf(a) != "" {
		return nil
	}
	return MergeRowWithoutBranchError{Row: a.ID}
}
