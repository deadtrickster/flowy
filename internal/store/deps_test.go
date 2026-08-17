package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// todoIn writes one queue item owned by p, in p's project, at the visibility
// asked for. It goes in through UpsertArtifact rather than through a verb
// because a todo is an ordinary artifact - which is the claim the room and the
// assignee both make - and what these tests are about is what happens to it
// afterwards.
func todoIn(
	t *testing.T, ctx context.Context, db *DB, p *Principal,
	title, visibility, assignee string,
) *Artifact {
	t.Helper()

	fields, err := json.Marshal(map[string]any{RoomField: "build", AssigneeField: assignee})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	project := p.Project
	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: p.UserID, Title: title, Status: "todo",
		Visibility: visibility, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("write todo %q: %v", title, err)
	}
	return art
}

// finish moves an item to done, the way a status move does.
func finish(t *testing.T, ctx context.Context, db *DB, art *Artifact) {
	t.Helper()

	art.Status = DoneStatus
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("finish %q: %v", art.Title, err)
	}
}

// readyOf is what a principal's ready query says about one todo: whether it is
// in the answer at all, and whether it can be started.
func readyOf(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) (*Readiness, bool) {
	t.Helper()

	rows, err := db.Ready(ctx, p, ArtifactQuery{})
	if err != nil {
		t.Fatalf("ready as %s: %v", p.UserID, err)
	}
	for _, r := range rows {
		if r.Item != nil && r.Item.ID == id {
			return r, true
		}
	}
	return nil, false
}

// blockerIDs is what a readiness row says is in the way.
func blockerIDs(r *Readiness) []string {
	out := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		state := "unknown"
		switch {
		case b.Done:
			state = "done"
		case b.Known:
			state = "not done"
		}
		out = append(out, b.ID+"="+state)
	}
	return out
}

// THE ONE THAT MATTERS.
//
// A todo B is carrying, blocked by a todo B CANNOT SEE. B's ready set must not
// contain it - and must still not contain it once the blocker is finished,
// because B still cannot see that it was. Invisible blocks whether or not it is
// done: a reader who cannot read a blocker cannot confirm it is finished, so the
// node holds.
//
// This is the check the whole surface exists for, and it is the one an
// implementation nobody thought about fails. Skipping the ids you cannot resolve
// is the natural thing to write, it passes every same-project test, and it is a
// machine starting work whose dependency is not done.
//
// The last third is the other half of "per reader": A, who can see both ends,
// gets the opposite answer to B at the same moment, and both are right.
func TestABlockerTheReaderCannotSeeHoldsTheTodoDoneOrNot(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pd")
	elsewhere := declaredProject(t, ctx, db, "px")
	a := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	b := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	// B reaches A's project for shared rows, and for nothing narrower. That edge
	// is what makes this two principals looking at ONE graph rather than two
	// principals who simply cannot see each other at all.
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: elsewhere, ToProject: here, GrantedBy: a.UserID,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// The work B is carrying: shared, so B reads it.
	blocked := todoIn(t, ctx, db, a, "bench-test the gearbox", VisibilityShared, b.UserID)
	// What it is waiting on: project-only in A's project, so B cannot read it and
	// no grant reaches it.
	blocker := todoIn(t, ctx, db, a, "rebuild the gearbox", VisibilityProjectOnly, a.UserID)

	if _, err := db.ReadArtifact(ctx, b, blocker.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the fixture is wrong: B can already read the blocker (%v)", err)
	}
	if _, err := db.ReadArtifact(ctx, b, blocked.ID, false); err != nil {
		t.Fatalf("the fixture is wrong: B cannot read the todo it is carrying: %v", err)
	}

	// Before the edge, B can start it: carried, and nothing in the way.
	before, found := readyOf(t, ctx, db, b, blocked.ID)
	if !found {
		t.Fatal("B's queue does not hold the todo B is carrying")
	}
	if !before.Ready {
		t.Fatalf("the fixture is wrong: the todo is not ready before anything blocks it: %+v", before)
	}

	if _, err := db.AddDep(ctx, a, blocked.ID, blocker.ID); err != nil {
		t.Fatalf("add dep: %v", err)
	}

	// B reads the edge - it hangs off the todo B can read - and cannot resolve
	// the other end. So it holds.
	after, found := readyOf(t, ctx, db, b, blocked.ID)
	if !found {
		t.Fatal("B's queue lost the todo B is carrying")
	}
	if len(after.Blockers) != 1 {
		t.Fatalf("B sees %d blockers, want the one edge: %v", len(after.Blockers), blockerIDs(after))
	}
	if after.Blockers[0].ID != blocker.ID {
		t.Fatalf("B sees %v in the way, want %s", blockerIDs(after), blocker.ID)
	}
	if after.Blockers[0].Known {
		t.Fatalf("B is told it can read a blocker it cannot: %v", blockerIDs(after))
	}
	if after.Ready {
		t.Fatalf("a todo blocked by something B cannot see is ready for B: %v", blockerIDs(after))
	}

	// And now the half that a build which quietly resolved unknown ids as "done"
	// would also fail, in the other direction: the blocker finishes, B still
	// cannot see that it did, and the todo still holds.
	finish(t, ctx, db, blocker)
	if _, err := db.ReadArtifact(ctx, b, blocker.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finishing the blocker handed it to B (%v)", err)
	}
	done, found := readyOf(t, ctx, db, b, blocked.ID)
	if !found {
		t.Fatal("B's queue lost the todo after the blocker was finished")
	}
	if done.Blockers[0].Known || done.Blockers[0].Done {
		t.Fatalf("B is told a blocker B cannot read is finished: %v", blockerIDs(done))
	}
	if done.Ready {
		t.Fatalf("a finished blocker B cannot see made the todo ready for B: %v", blockerIDs(done))
	}

	// A can see both ends, so for A it IS ready - at the same moment, off the
	// same rows. Two readers disagreeing here is the design and not a bug: a
	// stored flag would be one answer, and it would be wrong for one of them.
	forA, found := readyOf(t, ctx, db, a, blocked.ID)
	if !found {
		t.Fatal("A's queue does not hold the todo")
	}
	if len(forA.Blockers) != 1 || !forA.Blockers[0].Known || !forA.Blockers[0].Done {
		t.Fatalf("A cannot resolve the blocker A wrote: %v", blockerIDs(forA))
	}
	if !forA.Ready {
		t.Fatalf("the todo is not ready for A, who can see the finished blocker: %v",
			blockerIDs(forA))
	}
}

// Ready is two conditions, and neither of them is enough on its own: every
// blocker done, AND somebody carrying it. Dropping the second is the easy
// mistake - it looks like a queue property rather than a dependency one - and it
// is how a drainer picks up work nobody has claimed, which is the collision this
// whole surface was built after.
func TestReadyIsDepsDoneAndAssignedAndNeitherAlone(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pd")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	// Four todos: the two conditions, crossed.
	neither := todoIn(t, ctx, db, p, "blocked and unowned", VisibilityProjectOnly, "")
	assignedOnly := todoIn(t, ctx, db, p, "blocked but owned", VisibilityProjectOnly, "a-bench")
	clearOnly := todoIn(t, ctx, db, p, "unblocked but unowned", VisibilityProjectOnly, "")
	both := todoIn(t, ctx, db, p, "unblocked and owned", VisibilityProjectOnly, "a-bench")
	blocker := todoIn(t, ctx, db, p, "the thing they are all waiting on", VisibilityProjectOnly, "a-bench")

	for _, art := range []*Artifact{neither, assignedOnly} {
		if _, err := db.AddDep(ctx, p, art.ID, blocker.ID); err != nil {
			t.Fatalf("add dep on %q: %v", art.Title, err)
		}
	}

	want := func(id, what string, ready bool) {
		t.Helper()
		r, found := readyOf(t, ctx, db, p, id)
		if !found {
			t.Fatalf("%s is not in the queue at all", what)
		}
		if r.Ready != ready {
			t.Fatalf("%s: ready is %v, want %v (assignee %q, blockers %v)",
				what, r.Ready, ready, r.Assignee, blockerIDs(r))
		}
	}
	want(neither.ID, "blocked and unowned", false)
	want(assignedOnly.ID, "blocked but owned", false)
	want(clearOnly.ID, "unblocked but unowned", false)
	want(both.ID, "unblocked and owned", true)

	// Finishing the blocker moves the one that was only waiting on it, and does
	// not move the one that was never carried.
	finish(t, ctx, db, blocker)
	want(assignedOnly.ID, "owned, and its blocker is now done", true)
	want(neither.ID, "unowned, and its blocker is now done", false)

	// And a done blocker is a resolved blocker rather than a vanished one: the
	// edge is still on the row, saying what it is that finished.
	r, _ := readyOf(t, ctx, db, p, assignedOnly.ID)
	if len(r.Blockers) != 1 || !r.Blockers[0].Done {
		t.Fatalf("the satisfied edge is not on the row as satisfied: %v", blockerIDs(r))
	}
}

// dep_remove unblocks, and the OLD EDGE IS STILL THERE. That is the whole reason
// this is an event: a field would have answered "what blocks this now" and
// destroyed "who said it did, and when they took it back" - the question that
// gets asked later by somebody working out why two agents built the same thing.
//
// So this asserts the log, not the adjacency: both entries, in the order they
// were written, with the seat that wrote each - and only then that the todo is
// ready again.
func TestRemovingADepUnblocksAndBothEntriesStayInTheLog(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pd")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	agent := &Principal{UserID: p.UserID, AgentID: "a-" + ulid.NewString(), Project: project}

	blocked := todoIn(t, ctx, db, p, "land the gate fix", VisibilityProjectOnly, "a-bench")
	blocker := todoIn(t, ctx, db, p, "work out what the gate is failing on", VisibilityProjectOnly, "a-bench")

	added, err := db.AddDep(ctx, p, blocked.ID, blocker.ID)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if r, _ := readyOf(t, ctx, db, p, blocked.ID); r.Ready {
		t.Fatal("the todo is ready with an unfinished blocker on it")
	}

	// The agent takes it back. A different seat on purpose: who said the queue
	// was ordered this way and who said it was not are two facts, and an entry
	// that folded an agent into the person behind it would lose one of them.
	removed, err := db.RemoveDep(ctx, agent, blocked.ID, blocker.ID)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if added.ID == removed.ID {
		t.Fatal("the removal reused the add's row, so the edge is gone rather than taken back")
	}

	after, _ := readyOf(t, ctx, db, p, blocked.ID)
	if len(after.Blockers) != 0 {
		t.Fatalf("the removed edge is still holding the todo: %v", blockerIDs(after))
	}
	if !after.Ready {
		t.Fatalf("the todo did not become ready when its only blocker was removed: %+v", after)
	}

	log, err := db.DepLog(ctx, p, blocked.ID)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want the add and the removal", len(log))
	}
	if log[0].ID != added.ID || log[0].Type != EventDepAdd {
		t.Fatalf("the edge that was taken back is not in the log as it was written: %+v", log[0])
	}
	if log[0].Blocker != blocker.ID || log[0].Todo != blocked.ID {
		t.Fatalf("the entry lost one of its two ends: %+v", log[0])
	}
	if log[0].Actor != p.UserID {
		t.Fatalf("the add is recorded against %q, want the person who wrote it", log[0].Actor)
	}
	if log[1].ID != removed.ID || log[1].Type != EventDepRemove {
		t.Fatalf("the removal read back as %+v", log[1])
	}
	if log[1].Actor != agent.AgentID {
		t.Fatalf("the removal is recorded against %q, want the agent that wrote it", log[1].Actor)
	}
	if log[0].SeqHLC >= log[1].SeqHLC {
		t.Fatalf("the entries are not in the order they were written: %d then %d",
			log[0].SeqHLC, log[1].SeqHLC)
	}
}

// A cycle is REFUSED, over the graph the writer can see, and the refusal names
// the way round the loop already goes. A queue that deadlocks silently is worse
// than one that says so.
//
// The second half is the honest limit of that. The check can only walk what the
// writer can read, so a loop assembled by two principals across a boundary is
// not caught at write time - and there, ready simply never fires for anything in
// it, which is the safe direction and is the same rule as an invisible blocker.
// Both halves are asserted, because claiming only the first would be claiming a
// guarantee this does not have.
func TestACycleIsRefusedAndNothingInOneIsEverReady(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pd")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	first := todoIn(t, ctx, db, p, "one", VisibilityProjectOnly, "a-bench")
	second := todoIn(t, ctx, db, p, "two", VisibilityProjectOnly, "a-bench")
	third := todoIn(t, ctx, db, p, "three", VisibilityProjectOnly, "a-bench")

	// one <- two <- three, so three is waiting on two is waiting on one.
	if _, err := db.AddDep(ctx, p, third.ID, second.ID); err != nil {
		t.Fatalf("three depends on two: %v", err)
	}
	if _, err := db.AddDep(ctx, p, second.ID, first.ID); err != nil {
		t.Fatalf("two depends on one: %v", err)
	}

	// Closing it: one waiting on three. Two hops away, so a check that only
	// looked at the direct edge would let this through.
	_, err := db.AddDep(ctx, p, first.ID, third.ID)
	if err == nil {
		t.Fatal("an edge that closes a three-node cycle was accepted")
	}
	var cycle CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("the refusal is %v, want one that says it is a cycle", err)
	}
	for _, id := range []string{first.ID, second.ID, third.ID} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("the refusal does not name %s, so it does not say where the loop is: %v",
				id, err)
		}
	}

	// A refusal is a refusal: nothing landed, and the graph is what it was.
	log, err := db.DepLog(ctx, p, first.ID)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("the refused edge is in the log anyway: %d entries", len(log))
	}
	if r, _ := readyOf(t, ctx, db, p, first.ID); !r.Ready {
		t.Fatalf("the refused edge blocked the todo it was refused on: %v", blockerIDs(r))
	}

	// The other half, and the honest limit. A loop assembled across a permission
	// boundary, where the principal who closes it cannot see the hop that makes
	// it a loop - so the write is NOT refused, because nothing refused it could
	// have known.
	//
	//   shared (A's, shared) -> hidden (A's, project-only) -> theirs (B's, shared)
	//   -> shared, written by B, who cannot read `hidden` and so walks from
	//   `shared` straight into the dark.
	//
	// What holds it then is the ready query, and it holds it for both of them.
	elsewhere := declaredProject(t, ctx, db, "px")
	other := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: elsewhere, ToProject: project, GrantedBy: p.UserID,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: project, ToProject: elsewhere, GrantedBy: other.UserID,
	}); err != nil {
		t.Fatalf("grant back: %v", err)
	}

	shared := todoIn(t, ctx, db, p, "shared", VisibilityShared, "a-bench")
	hidden := todoIn(t, ctx, db, p, "hidden", VisibilityProjectOnly, "a-bench")
	theirs := todoIn(t, ctx, db, other, "theirs", VisibilityShared, "a-bench")

	if _, err := db.AddDep(ctx, p, hidden.ID, theirs.ID); err != nil {
		t.Fatalf("hidden depends on theirs: %v", err)
	}
	if _, err := db.AddDep(ctx, p, shared.ID, hidden.ID); err != nil {
		t.Fatalf("shared depends on hidden: %v", err)
	}
	// B closes it. B reads both ends of its own edge and neither end of the one
	// in the middle, so the walk from `shared` stops at an id it cannot resolve.
	if _, err := db.ReadArtifact(ctx, other, hidden.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the fixture is wrong: B can read the hidden hop (%v)", err)
	}
	if _, err := db.AddDep(ctx, other, theirs.ID, shared.ID); err != nil {
		t.Fatalf("theirs depends on shared: %v - the write-time check reached past "+
			"what its writer can read", err)
	}

	// And nothing in it is ever ready, for either reader, with the id that is
	// holding it said out loud - which is the difference between a queue that has
	// stopped and a queue with nothing to do.
	for _, reader := range []*Principal{p, other} {
		for _, art := range []*Artifact{shared, hidden, theirs} {
			r, found := readyOf(t, ctx, db, reader, art.ID)
			if !found {
				if art == hidden && reader == other {
					continue // B cannot see it at all, which is not this claim
				}
				t.Fatalf("%s is not in %s's queue", art.Title, reader.UserID)
			}
			if r.Ready {
				t.Fatalf("%s is ready for %s despite being in a cycle: %v",
					art.Title, reader.UserID, blockerIDs(r))
			}
			if len(r.Blockers) != 1 {
				t.Fatalf("%s says nothing about what is holding it: %v",
					art.Title, blockerIDs(r))
			}
		}
	}
}

// The two refusals that are about the edge rather than about the graph: a todo
// on both ends, and an edge that is already there or was never there.
//
// A self-edge can never be satisfied, so storing it would produce a todo that is
// never ready with nothing on the row to say why - which is exactly the silent
// deadlock the cycle refusal exists to stop.
func TestATodoCannotDependOnItselfAndAnEdgeIsSaidOnce(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pd")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	todo := todoIn(t, ctx, db, p, "the one", VisibilityProjectOnly, "a-bench")
	other := todoIn(t, ctx, db, p, "the other", VisibilityProjectOnly, "a-bench")

	_, err := db.AddDep(ctx, p, todo.ID, todo.ID)
	if err == nil {
		t.Fatal("a todo was made to depend on itself")
	}
	var self SelfDepError
	if !errors.As(err, &self) {
		t.Fatalf("the refusal is %v, want one that says a todo cannot depend on itself", err)
	}
	if log, err := db.DepLog(ctx, p, todo.ID); err != nil {
		t.Fatalf("log: %v", err)
	} else if len(log) != 0 {
		t.Fatalf("the refused self-edge is in the log anyway: %d entries", len(log))
	}
	if r, _ := readyOf(t, ctx, db, p, todo.ID); !r.Ready {
		t.Fatalf("the refused self-edge blocked the todo: %v", blockerIDs(r))
	}

	// Said once. Every entry in the log is a real transition, or a reader has to
	// fold it before it can say when anything actually changed.
	if _, err := db.AddDep(ctx, p, todo.ID, other.ID); err != nil {
		t.Fatalf("add: %v", err)
	}
	var state DepStateError
	if _, err := db.AddDep(ctx, p, todo.ID, other.ID); !errors.As(err, &state) {
		t.Fatalf("adding the same edge twice was answered with %v", err)
	}
	if _, err := db.RemoveDep(ctx, p, todo.ID, other.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := db.RemoveDep(ctx, p, todo.ID, other.ID); !errors.As(err, &state) {
		t.Fatalf("removing an edge that is not there was answered with %v", err)
	}
	if log, err := db.DepLog(ctx, p, todo.ID); err != nil {
		t.Fatalf("log: %v", err)
	} else if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want only the two real transitions", len(log))
	}
}

// An edge names two todos the writer can read, and an id is a guess anybody can
// make. So an end out of reach is refused as an id that is not there - the
// answer a read of it would give - and so is an end that is readable and is not
// a queue item, because an edge onto a report is one nothing would ever read.
//
// What is NOT refused is the two ends being in different projects. That is the
// point of the surface, and a build that required one project would have made
// the cross-project case - the one this all exists for - impossible to write.
func TestAnEdgeNamesTwoReadableTodosAndMayCrossAProject(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pd")
	elsewhere := declaredProject(t, ctx, db, "px")
	a := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	b := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: here, ToProject: elsewhere, GrantedBy: a.UserID,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	mine := todoIn(t, ctx, db, a, "mine", VisibilityProjectOnly, "a-bench")
	hidden := todoIn(t, ctx, db, b, "hidden from A", VisibilityProjectOnly, "a-bench")
	shared := todoIn(t, ctx, db, b, "B's, and shared", VisibilityShared, "a-bench")

	var notATodo NotATodoError
	if _, err := db.AddDep(ctx, a, mine.ID, hidden.ID); !errors.As(err, &notATodo) {
		t.Fatalf("an edge onto a todo A cannot read was answered with %v", err)
	} else if notATodo.ID != hidden.ID {
		t.Fatalf("the refusal names %s, want the end that was out of reach", notATodo.ID)
	}
	if !errors.Is(errors.Unwrap(NotATodoError{ID: "x"}), ErrNotFound) {
		t.Fatal("the refusal does not read as an id that is not there")
	}

	// Readable, and not a queue item.
	report := &Artifact{
		ID: ulid.NewString(), Type: "report", Project: &here, OwnerUser: a.UserID,
		Title: "the gate run", Visibility: VisibilityProjectOnly,
	}
	if err := db.UpsertArtifact(ctx, report); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, err := db.AddDep(ctx, a, mine.ID, report.ID); !errors.As(err, &notATodo) {
		t.Fatalf("an edge onto a report was answered with %v", err)
	}

	// And the case that must work: two projects, one writer who reads both ends.
	if _, err := db.AddDep(ctx, a, mine.ID, shared.ID); err != nil {
		t.Fatalf("an edge across a project boundary was refused: %v", err)
	}
	r, found := readyOf(t, ctx, db, a, mine.ID)
	if !found {
		t.Fatal("A's queue lost the todo")
	}
	if len(r.Blockers) != 1 || !r.Blockers[0].Known || r.Blockers[0].Done {
		t.Fatalf("A cannot resolve the blocker in the other project: %v", blockerIDs(r))
	}
	if r.Ready {
		t.Fatalf("the todo is ready with an unfinished blocker in another project: %v",
			blockerIDs(r))
	}
}

// LiveDeps is the fold on its own: the latest entry per blocker decides, in
// first-added order. It is the adjacency's whole definition, and it is a pure
// function so that "the graph is a reading of the log" is a thing that can be
// asserted without a database in the way.
func TestLiveDepsFoldsTheLatestEntryPerBlocker(t *testing.T) {
	entry := func(kind, blocker string) DepEntry {
		return DepEntry{Type: kind, Todo: "t", Blocker: blocker}
	}
	got := LiveDeps([]DepEntry{
		entry(EventDepAdd, "a"),
		entry(EventDepAdd, "b"),
		entry(EventDepRemove, "a"),
		entry(EventDepAdd, "c"),
		entry(EventDepAdd, "a"), // said again after being taken back
		entry(EventDepRemove, "c"),
		entry(EventDepAdd, ""), // an entry whose other end this build cannot read
	})
	want := []string{"a", "b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the fold came out %v, want %v", got, want)
	}
	if len(LiveDeps(nil)) != 0 {
		t.Fatal("a todo with no entries has edges")
	}
}
