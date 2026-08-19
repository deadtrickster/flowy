package main

// GET /api/merge-queue - the merge queue as a browser can read it.
//
// This exists because of a gap I made and did not see until somebody needed it:
// merge_queue is an MCP TOOL, and THE CONSOLE CANNOT CALL MCP. So the admission
// rule, the one piece of tonight's work whose whole purpose is to be consulted
// before a merge, was unreachable from the surface a person actually looks at.
// A door only agents can knock on is half a door.
//
// The verdicts are computed HERE rather than in the console, deliberately. The
// browser has no git, no store and no permission filter, and a second
// implementation of "may this land" in TypeScript would be a second answer that
// disagrees with the first one on the day it matters.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeQueueItem is one request as the console draws it.
//
// Flat on purpose: the panel should not have to reach into fields to find the
// branch, and every reader that has had to do that today got it wrong at least
// once - status from .fields, owner from body text, assignee from two places.
type mergeQueueItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Type is the row's own artifact type, sent so that a caller building a
	// link to it does not have to know that a merge row is stored as a memory.
	// The console restated that convention by hand, as four other views did,
	// and a reference every caller reassembles from a remembered rule is a
	// reference one of them eventually assembles wrong - see 01M08FK999.
	Type     string `json:"type"`
	Project  string `json:"project,omitempty"`
	Branch   string `json:"branch"`
	Target   string `json:"target"`
	GatedTip string `json:"gated_tip"`
	GateRun  string `json:"gate_run"`
	// GateRef is where the evidence lives when that is not Branch - the
	// integration branch a union actually measured. A lander reading only
	// Branch lands one commit of a sixteen-commit union and calls it done; it
	// happened twice in one night and nobody lied.
	GateRef string `json:"gated_ref,omitempty"`
	// Red is the last verdict that said no: the tip that was measured and found
	// broken, the base it was measured from, when, and one line about it.
	// Absent when there is none.
	//
	// Until this existed a red lived in a file named red-<row>-<tip> on
	// whichever box ran the gate. The queue therefore showed a finished failed
	// run as work in progress - gating true, status active - and the only way to
	// learn a branch was broken was to ask the agent who ran it. A verdict is a
	// fact about the row.
	Red *mergeQueueRed `json:"red,omitempty"`
	// Blocked is why the last caller that tried could not take this row at all -
	// a branch held in somebody's worktree, a rebase that would conflict.
	// Absent when there is none, and absent once it is older than
	// store.BlockBelievedFor: a skip is a fact about a moment, and an old answer
	// about a fast-moving fact is furniture rather than evidence.
	Blocked *mergeQueueBlocked `json:"blocked,omitempty"`
	// Queued is WHEN THIS ROW JOINED THE QUEUE, and it is the field this list
	// is now sorted by. It was absent, and a sort by a value the view does not
	// carry is a sort nobody can check: a reader who suspected the order was
	// wrong had to fetch /api/artifact/<id> once per row to see it. Two of us
	// did exactly that tonight.
	Queued     time.Time `json:"queued"`
	Status     string    `json:"status"`
	Assignee   string    `json:"assignee,omitempty"`
	Admissible *bool     `json:"admissible,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	// Held says the target is reserved by another declarer's lock - a WAIT, as
	// against Admissible's verdict about this row's own evidence. Distinct on
	// purpose: collapsing them is how an agent re-gates when it should sleep
	// and sleeps when it should re-gate.
	Held bool `json:"held,omitempty"`
	// Gating is true while a run is MEASURING this branch and has not reported.
	//
	// The rule as first written protected the lander and nobody else: "a branch
	// lands only on the tip its gate measured" says nothing about the gate
	// ALREADY RUNNING, so every landing silently invalidated every in-flight run
	// and the invalidated party found out by reading a number that was already
	// worthless. That happened twice in one hour on the night this was written.
	//
	// A run is in flight when its request names the run and has no verdict yet.
	// Recording the verdict afterwards is what made the window invisible; naming
	// the run when it STARTS is what makes it visible.
	Gating bool `json:"gating"`
	// KnownIssue is the row that explains this refusal, when somebody has
	// written one - see knownissue.go. It rides beside the reason rather than
	// arriving as a banner over the page, because the whole point is that it
	// reaches the reader ATTACHED TO THE THING THAT PROVOKED THE QUESTION. A
	// banner is a second announcement, and announcing is what already failed.
	KnownIssue *store.KnownIssue `json:"known_issue,omitempty"`
	// code is the refusal's own token, kept here only to resolve the above in
	// one query after the page is built. Unexported, so it never reaches the
	// wire: the client has the code inside known_issue when there is a row, and
	// no use for it when there is not.
	code string
}

func (s *server) handleMergeQueue(w http.ResponseWriter, r *http.Request) {
	response, err := s.readMergeQueue(r)
	if err == nil {
		scope := answerScopeOf(r, principalOf(r))
		response.Project, response.AllProjects = scope.Project, scope.All
	}
	if err != nil {
		if errors.Is(err, errBadQueueParam) {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// errBadQueueParam is a caller's mistake in the query rather than a broken
// node, so the door answers 400 with the sentence rather than 500 with nothing.
var errBadQueueParam = errors.New("bad queue parameter")

// mergeQueueAnswer builds what the queue says, without writing it.
//
// It is split from the handler for the door that WAITS on this answer - see
// handleMergeQueueWait, which asks the same question on a loop and needs the
// same answer to compare against itself. A second computation of "what the
// queue says" would be two queues that disagree about when something changed,
// which is the shape this file already refuses for admissible.
func (s *server) readMergeQueue(r *http.Request) (mergeQueueAnswer, error) {
	p := principalOf(r)
	q := r.URL.Query()

	room, err := roomArg(q.Get("room"))
	if err != nil {
		return mergeQueueAnswer{}, fmt.Errorf("%w: %s", errBadQueueParam, err)
	}
	target := strings.TrimSpace(q.Get("target"))
	if target == "" {
		target = store.DefaultMergeTarget
	}

	// WHERE THE TIP COMES FROM, and the answer says which, because a verdict is
	// only as good as the tip it was measured against and a console that cannot
	// tell the caller's tip from the node's own would be exactly the confusion
	// this rule exists to end.
	//
	//   stated   - the caller read it from git and passed it. The real answer.
	//   landed   - nobody stated one, so the node offers the last sha a land
	//              through the merge door recorded. Fresh by construction: it
	//              moves exactly when a land moves it.
	//   deployed - nothing has landed through the door, so the node offers the
	//              commit IT WAS BUILT FROM. A browser has no git, so this is
	//              the best a page can do on its own, and it is honest: it
	//              answers "may this land on what is running here", which is a
	//              real question, and NOT "may this land on master right now",
	//              which it cannot know.
	//
	// The landed link exists because the deployed one froze: a deploy held for
	// an evening left every bare queue read answering against a tip a dozen
	// landings old, refusing green branches all night for reasons that were
	// already false.
	tip, tipFrom := strings.TrimSpace(q.Get("target_tip")), "stated"
	stated := tip != ""
	if tip == "" {
		// THE PAGE'S TIP IS THE ASKED-FOR PROJECT'S. Each ROW is judged against
		// its own below - see rowTip - because one tip for a page that lists
		// several projects is an answer about whichever of them happened to
		// land last.
		//
		// MEASURED, and it is why this exists: after landings began keying on
		// the project, an unfiltered read asked for "" and got the legacy row,
		// which held a tip three landings old. /api/merge-queue answered
		// bbb3c16 while ?project=flowy and git both said 28505f3 - so every
		// caller that states no project judged rows against a stale base, and
		// MergeAdmissible refused correct work for a reason that was not true.
		// A fallback that answers confidently is worse than one that says
		// nothing.
		if landed, err := s.db.LandedTipOf(r.Context(), q.Get("project"), target); err == nil && landed != nil {
			tip, tipFrom = landed.Tip, "landed"
		} else if bs := strings.TrimSpace(buildStamp); bs != "" && bs != "src" {
			tip, tipFrom = bs, "deployed"
		} else {
			tipFrom = "none"
		}
	}

	list, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type:      store.MemoryType,
		Kind:      store.MergeKind,
		Project:   q.Get("project"),
		Room:      room,
		NotStatus: store.DoneStatus,
		ScopeAll:  scopeAll(r, p),
		Limit:     intParam(q.Get("limit")),
		// IN THE ORDER THEY WERE QUEUED, because this list is consumed in turn
		// rather than browsed. The drainer takes the first row of this answer
		// it can work, so whatever this door sorts by IS the queue discipline -
		// and until now that was `updated DESC`, which made every write a
		// promotion. See store.ArtifactQuery.QueuedOrder for what that cost.
		QueuedOrder: true,
	})
	if err != nil {
		return mergeQueueAnswer{}, err
	}

	// The landing lock on this target, read once for every row. A row held by
	// another declarer's lock is a WAIT and not a verdict about its evidence,
	// and the difference is spelled out below where the two meet.
	// The same project the tip was read for - a page that judged rows against
	// one project's tip and one project's lock would be answering about two.
	lock, err := s.db.MergeLockOf(r.Context(), q.Get("project"), target)
	if err != nil {
		return mergeQueueAnswer{}, err
	}
	now := time.Now()
	lockLive := lock.Live(now)

	items := make([]mergeQueueItem, 0, len(list))
	for _, a := range list {
		if !strings.EqualFold(store.TargetOf(a), target) {
			continue
		}
		// AGAINST ITS OWN PROJECT'S TIP. A stated ?target_tip= still wins for
		// every row - a caller who measured a tip is asking about that tree -
		// and otherwise each row is judged against where ITS project's target
		// actually is.
		items = append(items, queueItemOf(a, s.rowTip(r, tip, stated, a, target), lock, lockLive, time.Now()))
	}

	// The rows explaining these refusals, in one query for the whole page, and
	// only when something was actually refused.
	//
	// WHY THE DEPLOY CODE COMES FIRST when the tip is the node's build stamp.
	// Under that fallback every item is judged against whenever somebody last
	// deployed, so a page of refusals can be entirely an artefact of the node
	// being behind - which happened, and cost an agent three gate runs and
	// another forty minutes of re-derivation. Each item's own reason is true and
	// is the wrong thing to read first. If nobody has written a row about the
	// deploy, this falls straight through to the item's own case.
	codes := make([]string, 0, len(items)+1)
	for _, it := range items {
		if it.code != "" {
			codes = append(codes, it.code)
		}
	}
	// Nothing was refused, so nothing needs explaining, and the query does not
	// run - a queue where everything may land is the common case and must not
	// pay for this.
	if len(codes) > 0 && tipFrom == "deployed" {
		codes = append([]string{store.RefusalMergeTipDeployed}, codes...)
	}
	if found := knownIssues(r.Context(), s.db, p, codes, scopeAll(r, p)); found != nil {
		for i := range items {
			if items[i].code == "" {
				continue
			}
			if tipFrom == "deployed" {
				items[i].KnownIssue = store.PickKnownIssue(found,
					store.RefusalMergeTipDeployed, items[i].code)
				continue
			}
			items[i].KnownIssue = store.PickKnownIssue(found, items[i].code)
		}
	}

	// How many runs are measuring right now. A lander reads this before merging:
	// landing while somebody is gating invalidates their evidence, and the queue
	// saying so is the difference between a decision and an accident.
	gating := 0
	for _, it := range items {
		if it.Gating {
			gating++
		}
	}

	// A NAMED ANSWER, not a map, and that is the point of the type rather than
	// a tidiness. Every reader of this door has had to rebuild the shape by
	// hand: q.sh read `tip` where the payload says `target_tip`, and quoted a
	// stale lock as current - two decisions changed by a second reader kept in
	// step by somebody remembering. `flowy queue` decodes THIS type, so the
	// client and the server cannot drift.
	response := mergeQueueAnswer{
		Target:    target,
		TargetTip: tip,
		Gating:    gating,
		TipFrom:   tipFrom,
		Items:     items,
		// Stated rather than inferred from a missing field. A console that
		// treats "no verdict" as "admissible" is the failure this endpoint is
		// guarding against, and leaving it to be worked out from an absent key
		// is how that happens.
		Decided: tip != "",
	}
	// The lock rides the answer even when nothing is held, as held:false - the
	// caller deciding to declare wants "the target is free" as a fact, not as
	// the absence of a key they have to know the meaning of.
	response.Lock = &mergeQueueLock{}
	if lock != nil {
		response.Lock = &mergeQueueLock{
			Held:       lockLive,
			Holder:     lock.Holder,
			HolderName: lock.HolderName,
			// WHICH WORK the target is held for, which is half of why the lock
			// records it. Every session of a seat shares a principal, so
			// "held by claude-host" is a sentence a claude-host session reads
			// as "held by me" - and the seat that wanted to know which of its
			// three sessions held master had to message two agents and wait to
			// learn something one read should have answered. The store had it
			// an hour before this door said it.
			Item:    lock.Item,
			Until:   lock.Until,
			TakenAt: lock.TakenAt,
		}
	}
	return response, nil
}

// mergeQueueAnswer is what GET /api/merge-queue says, as a type both ends read.
type mergeQueueAnswer struct {
	Target    string           `json:"target"`
	TargetTip string           `json:"target_tip"`
	Gating    int              `json:"gating"`
	TipFrom   string           `json:"tip_from"`
	Items     []mergeQueueItem `json:"items"`
	Decided   bool             `json:"decided"`
	Lock      *mergeQueueLock  `json:"lock,omitempty"`
	// Changed and Cursor are the wait door's answer only - see
	// api_mergequeuewait.go. They ride this type rather than a wrapper so the
	// plain read and the waited read are one shape to a client.
	Changed *bool  `json:"changed,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	// WHICH PROJECT THIS QUEUE IS. `target` is a branch NAME, and every repo's
	// is called master - so a queue answer said "master" and nothing about
	// which master. With more than one project on a node that is not a label,
	// it is the difference between two queues. See api_scope.go, and 01M0DZP4HS
	// for the same name-is-not-an-identity problem one layer down in the lock.
	Project     string `json:"project,omitempty"`
	AllProjects bool   `json:"all_projects,omitempty"`
}

// mergeQueueLock is the landing lock as this door reports it. Held is false
// rather than the key being absent, for the reason above.
type mergeQueueLock struct {
	Held       bool      `json:"held"`
	Holder     string    `json:"holder,omitempty"`
	HolderName string    `json:"holder_name,omitempty"`
	Item       string    `json:"item,omitempty"`
	Until      time.Time `json:"until,omitempty"`
	TakenAt    time.Time `json:"taken_at,omitempty"`
}

// mergeQueueRed is the last red as a browser reads it.
type mergeQueueRed struct {
	Tip  string `json:"tip"`
	Base string `json:"base,omitempty"`
	At   string `json:"at,omitempty"`
	Note string `json:"note,omitempty"`
}

// queueRedOf reads the red a row carries, or nil. A declaration clears it, so
// this is always about the current run rather than one three landings ago.
func queueRedOf(a *store.Artifact) *mergeQueueRed {
	tip := store.RedTipOf(a)
	if tip == "" {
		return nil
	}
	return &mergeQueueRed{
		Tip:  tip,
		Base: store.GatedBaseOf(a),
		At:   store.RedAtOf(a),
		Note: store.RedNoteOf(a),
	}
}

// mergeQueueBlocked is the last skip as a browser reads it.
type mergeQueueBlocked struct {
	Why string `json:"why"`
	At  string `json:"at,omitempty"`
	By  string `json:"by,omitempty"`
}

// queueBlockedOf reads the skip a row carries, or nil - including nil for one
// that has aged out. A declaration clears it too, so what survives here is a
// reason nobody has disproved by taking the row.
func queueBlockedOf(a *store.Artifact, now time.Time) *mergeQueueBlocked {
	why := store.BlockedAt(a, now)
	if why == "" {
		return nil
	}
	return &mergeQueueBlocked{
		Why: why,
		At:  store.BlockedAtOf(a),
		By:  store.BlockedByOf(a),
	}
}

// queueItemOf is one row of the queue, as this door describes it.
//
// It is a function rather than the body of a loop because there are two doors
// onto it now: GET /api/merge-queue builds the page, and
// GET /api/merge/{id}/admissible answers about one row for a caller about to
// spend a gate run on it. 01M0B8JFXS counted four hand-written answers to that
// question across this fleet in one day - one in pre-gate.sh, one in drain.sh,
// one in a curl, one in a python shim - and twice two of us disagreed about
// whether a branch was landable. Both disagreements were readings of the same
// rows through different code. A second implementation here would be the fifth.
//
// The clock is a parameter rather than four calls to time.Now(). One row was
// being judged against four readings taken microseconds apart, which is
// harmless and is also the shape that makes a row report itself as gating and
// not-gating in the same answer.
func queueItemOf(
	a *store.Artifact, tip string, lock *store.MergeLock, lockLive bool, now time.Time,
) mergeQueueItem {
	it := mergeQueueItem{
		ID:       a.ID,
		Title:    a.Title,
		Type:     a.Type,
		Branch:   store.BranchOf(a),
		Target:   store.TargetOf(a),
		GatedTip: store.GatedTipOf(a),
		GateRun:  store.GateRunOf(a),
		GateRef:  store.GateRefOf(a),
		Red:      queueRedOf(a),
		Blocked:  queueBlockedOf(a, now.UTC()),
		Queued:   a.Created,
		Status:   store.TodoStatusOf(a),
		Assignee: store.AssigneeOf(a),
	}
	if a.Project != nil {
		it.Project = *a.Project
	}
	// Believed for a bounded time, not forever - see store.GatingAt. A run
	// that died leaves a declaration nobody will ever clear, and the first
	// version of this told the whole room not to land for twenty minutes
	// after a green run had already landed.
	it.Gating = store.GatingAt(a, now)
	if tip != "" {
		err := store.MergeAdmissible(a, tip)
		ok := err == nil
		it.Admissible = &ok
		if err != nil {
			it.Reason = err.Error()
			it.code = store.RefusalCodeOf(err)
		}
	}
	// HELD IS NOT NOT-ADMISSIBLE, and the two must never share a boolean.
	// "The target is reserved until T" says WAIT; "your evidence is stale"
	// says RE-GATE. A caller that cannot tell them apart does one when it
	// means the other, and the row that asked for this lock was explicit:
	// an agent refused because somebody else is mid-land should be told so,
	// with the name and the time, not folded into a bare false.
	if lockLive && store.GateActorOf(a) != lock.Holder {
		it.Held = true
		held := &store.ErrTargetHeld{Target: lock.Target, Held: lock, Now: now}
		if it.Reason == "" {
			it.Reason = held.Error()
		} else {
			it.Reason = fmt.Sprintf("%s; and %s", it.Reason, held.Error())
		}
	}
	return it
}

// rowTip is the tip THIS row is judged against.
//
// A page-level tip was right while one project filed merge rows and became
// wrong the moment landings started keying on the project: the page asks for
// one project (or none) and the list can hold rows from several, so a row from
// project B was compared against project A's last landing.
//
// A stated ?target_tip= wins for every row, because a caller who measured a tip
// is asking about that tree and not about the store's opinion of it.
//
// A row whose project has never landed gets the page's tip rather than nothing:
// "no tip" makes MergeAdmissible refuse for a reason that is about the store
// rather than the branch, and the page's tip is the same answer this door gave
// before projects were in the key.
func (s *server) rowTip(
	r *http.Request, pageTip string, stated bool, a *store.Artifact, target string,
) string {
	if stated {
		return pageTip
	}
	project := ""
	if a != nil && a.Project != nil {
		project = strings.TrimSpace(*a.Project)
	}
	if project == "" {
		return pageTip
	}
	if landed, err := s.db.LandedTipOf(r.Context(), project, target); err == nil && landed != nil {
		return landed.Tip
	}
	return pageTip
}
