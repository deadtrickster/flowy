package store

import (
	"fmt"
	"strings"
)

// ACTIVE MEANS SOMEBODY IS ON IT, AND THE ROW HAS TO BE ABLE TO SAY WHO.
//
// A queue row carries two facts about the same thing. The status says where the
// work is - todo, active, done - and the assignee says who is carrying it. They
// were written through two doors that had never heard of each other: status
// through POST /api/artifact/{id}/status and SetTodoStatus, assignee through
// POST /api/todo/{id}/assignee, AssignTodo and ClaimTodo. So `active` with
// nobody on it was a pair a row could hold, and the board showed it - a row
// that says somebody is working on it and that nobody is.
//
// It is the same missing half the queue has now learned twice: a state set by
// one event and cleared by another that nobody joined up - gating that no
// re-gate cleared, a hold that outlived its holder. Both were repaired
// afterwards by a reader that knew better. This one is not, because a reader
// that knows better is a second opinion about what the row means, and the two
// opinions are exactly what produced the bad pair in the first place.
//
// SO THE PAIR IS REFUSED AT THE STATEMENT, NOT CORRECTED AT THE READ. There are
// four statements in this package that write a local artifact row -
// createArtifact, upsertArtifact, setArtifactFields and setArtifactStatus - and
// every one of them asks this before it writes. A door added tomorrow inherits
// the rule by writing through one of them, which is what a rule kept per surface
// never gets: POST /api/artifacts, mem_write, the room's raise door and the FUSE
// mount can all set a status, and only two of them can set an assignee at all.
//
// THE MERGE IS DELIBERATELY NOT ONE OF THE FOUR. sync.go has its own INSERT and
// keeps it: federation carries what a peer wrote, and a node that refused a
// peer's row would be a node that silently diverges from it. What arrives from
// elsewhere is that node's business. What is written here is this one's.
//
// WHY THIS DIRECTION AND NOT THE OTHER TWO. The alternatives were to have the
// status door CLAIM the row for whoever moved it, or to have the READ refuse to
// answer with a pair that cannot be true.
//
//   - Claiming through the status door would make "move this to active" a
//     last-write-wins claim: two agents marking the same row active would both
//     be told they own it, which is the exact failure ClaimTodo's compare-and-set
//     was written to end five times over on 2026-08-17/18. A claim is a race and
//     has to be a CAS; a status move is not one and must not quietly become one.
//   - Refusing at the read leaves the bad pair on the row and makes every reader
//     carry the rule. The row is still wrong, it is only harder to see.
//
// Refusing the write is the one that makes the state unrepresentable, and it
// costs the caller one extra call in the case where they had not said who was
// doing the work - which is a case where nobody could have answered "who".
//
// DONE NEEDS NO CARRIER, and neither does todo. Finished work is finished
// whether or not the person who did it is still named on it, and outstanding
// work with nobody on it is most of the queue. Only `active` makes a claim about
// a person, so only `active` has to be able to name one.
//
// PUTTING WORK DOWN IS NOT REFUSED, IT MOVES BOTH FACTS. An assignee cleared on
// an active row would fail this check, and refusing there would mean an agent
// that cannot finish also cannot hand back - it would hold the row forever, and
// lifecyclepath.go names active->todo as the move that must never need an
// apology. So AssignTodo and ClaimTodo take the row back to `todo` in the same
// write, with the status entry in the log beside the assignment entry. There is
// no "unless the caller says otherwise" because there is no otherwise: the only
// way to leave a row active is to name somebody else carrying it, and that is a
// handover rather than a release.

// ActiveUnownedError is the pair refused: a queue row that says work is in
// flight and cannot say whose.
//
// It names the row and, when the write was moving the status, the state it was
// coming from - a caller who is told "you cannot do that" without being told
// what to do instead reads it as the node being broken. The sentence names the
// two ways out, because both are real: claim it, or say who has it.
type ActiveUnownedError struct {
	Todo string
}

func (e ActiveUnownedError) Error() string {
	return fmt.Sprintf("%s cannot be %s with nobody carrying it: %s means somebody is on it, "+
		"and this row says nobody is. Take it first - POST /api/todo/%s/assignee with "+
		"{assignee, expect} or todo_assign over MCP - or name whoever is doing the work",
		e.Todo, ActiveStatus, ActiveStatus, e.Todo)
}

// depRefusal marks this as the caller's mistake rather than a broken node, so
// every door already maps it to a 400 instead of reporting the store as down.
// It is the interface every other queue refusal satisfies, which is what keeps
// one refusal from arriving as three different kinds of failure.
func (e ActiveUnownedError) depRefusal() {}

// checkQueueRow is the invariant, asked of the row a statement is about to
// write.
//
// It reads the pair the way every other reader reads it - TodoStatusOf for the
// column, AssigneeOf for the field with its OWNER-line fallback - so a row this
// refuses is exactly a row the board would have drawn as active and unowned.
// Reading either one differently here would refuse rows nobody can see are
// wrong, or pass rows everybody can.
//
// Anything that is not a queue item passes: a bug's lifecycle is the issue
// workflow's and its `active` is not this vocabulary's word at all.
func checkQueueRow(a *Artifact) error {
	if !IsQueueItem(a) {
		return nil
	}
	if TodoStatusOf(a) != ActiveStatus {
		return nil
	}
	if strings.TrimSpace(AssigneeOf(a)) != "" {
		return nil
	}
	return ActiveUnownedError{Todo: a.ID}
}

// putDownStatus is where a row goes when the work is put down: back to `todo`
// if it was active, and nowhere at all otherwise.
//
// An empty answer means this write has no business with the status column,
// which is the ordinary case - a handover from one carrier to another changes
// who is on it and not where it is, and a release from a done row does not
// reopen anything.
func putDownStatus(a *Artifact, name string) string {
	if strings.TrimSpace(name) != "" {
		return ""
	}
	if TodoStatusOf(a) != ActiveStatus {
		return ""
	}
	return TodoStatus
}
