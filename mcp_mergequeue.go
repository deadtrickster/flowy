package main

// The merge queue surface: reading the queue, and asking it whether something
// may land.
//
// The rule lives in internal/store/mergequeue.go and it already refuses
// correctly. What was missing until this file is that NOTHING ASKED IT. A store
// that says no to nobody is a comment with tests, and the day this was written
// had enough of those - four merges went in by hand against a tip their gate had
// never seen, and the rule that would have caught all four existed in a
// conversation rather than behind a door.
//
// So there is exactly one verb here and it answers one question: given where the
// target is RIGHT NOW, what in this queue may land. It is deliberately a read
// rather than a merge - flowy does not run git, and a tool that claimed to merge
// would be lying about who does the work. The caller merges; this says whether
// the caller is allowed to believe its own gate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

var mergeTools = []tool{
	{
		Name: "merge_gate",
		Description: "Declare that a gate is now MEASURING a merge request, or record " +
			"the verdict it produced. Declare it when the run STARTS, not when it " +
			"finishes: for the whole length of a run, nothing else records that " +
			"somebody is measuring against a tip, so anybody who lands meanwhile " +
			"silently destroys the evidence and the runner finds out afterwards by " +
			"reading a number that is already worthless. Call it again with " +
			"gated_tip when the run reports.",
		InputSchema: object(props{
			"id":  str("The merge request."),
			"run": str("The run doing the measuring, so a claim of green points at a log."),
			"gated_tip": str("The commit the gate measured. Leave it out while the run " +
				"is still going - that is what says the verdict is not in yet."),
		}, []string{"id", "run"}),
		call: mergeGate,
	},
	{
		Name: "merge_blocked",
		Description: "Say that you could NOT take a merge request, and why - its branch " +
			"is checked out in somebody's worktree, a rebase would conflict, whatever " +
			"stopped you. A skip that only goes in your own log leaves the queue " +
			"showing a row nobody can take as a row waiting its turn, which is how " +
			"three rows sat unpickable for twenty minutes while a drainer woke every " +
			"ninety seconds and found nothing it was allowed to work on. It takes no " +
			"lock: this is the verb for a caller that never got that far. The next " +
			"declaration clears it, because taking the row disproves whatever stopped " +
			"the last caller taking it.",
		InputSchema: object(props{
			"id":  str("The merge request you could not take."),
			"why": str("What stopped you, in a sentence a person can act on."),
		}, []string{"id", "why"}),
		call: mergeBlockedTool,
	},
	{
		Name: "merge_queue",
		Description: "The merge queue: every merge request you can read that is not " +
			"done, and whether each one may land on the target AS IT IS NOW. Pass the " +
			"target's current tip - read it from git, do not guess it - and each item " +
			"comes back admissible or refused with the reason. A request whose gate " +
			"measured a different tip is REFUSED: its checks are evidence about a tree " +
			"that no longer exists, which is how a queue of individually green branches " +
			"turns a target red. Without a tip you get the queue and no verdicts, which " +
			"is a list rather than an answer.",
		InputSchema: object(props{
			"target": str("Which target to ask about, e.g. master. Default master."),
			"target_tip": str("The commit the target is on right now. Leave it out to " +
				"list the queue without deciding anything."),
			"scope": enum("Narrow to one scope.", memScopes),
			"room":  str("Only the requests raised in this chat room."),
		}, nil),
		call: mergeQueueTool,
	},
}

// mergeEntry is one request and what this reader may conclude about it.
//
// Admissible and Reason are a pair on purpose: a bare false with no sentence is
// the refusal shape that sends somebody to measure the wrong thing, which
// happened repeatedly today. The reason names both tips.
type mergeEntry struct {
	Item     *store.Artifact `json:"item"`
	Branch   string          `json:"branch"`
	Target   string          `json:"target"`
	GatedTip string          `json:"gated_tip"`
	GateRun  string          `json:"gate_run"`
	Status   string          `json:"status"`
	// Red is what the last run found when it did not pass: the tip it measured,
	// when, and one line about it. Absent when there is none, so a row nobody
	// has failed on says nothing rather than saying "not red" on every line.
	//
	// It is why this field exists at all: a red used to live in a file named
	// red-<row>-<tip> on whichever box ran the gate, so the queue showed a
	// finished failed run as work in progress and a second drainer would have
	// measured the same broken tree. A verdict is a fact about the row, and a
	// fact about the row belongs where everybody reads the row.
	Red *mergeRed `json:"red,omitempty"`
	// Blocked is why the last caller could not take this row at all - a branch
	// held in somebody's worktree, a rebase that would conflict. Absent when
	// there is none, and absent once it has aged out: see store.BlockedAt.
	Blocked    *mergeBlocked `json:"blocked,omitempty"`
	Admissible *bool         `json:"admissible,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	// KnownIssue is the row somebody wrote about this refusal, under the same
	// key and in the same shape the HTTP door uses - see knownissue.go. An agent
	// reading a no it did not expect is the reader this exists for: it is the
	// one that otherwise re-derives the diagnosis from source, correctly, forty
	// minutes after somebody else already filed it.
	KnownIssue *store.KnownIssue `json:"known_issue,omitempty"`
	// code carries the refusal's token as far as the batch lookup below, and no
	// further.
	code string
}

// mergeBlocked is the last skip: why nothing could take the row.
type mergeBlocked struct {
	Why string `json:"why"`
	At  string `json:"at,omitempty"`
	By  string `json:"by,omitempty"`
}

func blockedOf(item *store.Artifact, now time.Time) *mergeBlocked {
	why := store.BlockedAt(item, now)
	if why == "" {
		return nil
	}
	return &mergeBlocked{Why: why, At: store.BlockedAtOf(item), By: store.BlockedByOf(item)}
}

// mergeRed is the last verdict that said no, as a reader sees it.
type mergeRed struct {
	Tip  string `json:"tip"`
	Base string `json:"base,omitempty"`
	At   string `json:"at,omitempty"`
	Note string `json:"note,omitempty"`
}

// redOf reads the red a row carries, or nil. A declaration clears it - see
// applyGate - so what this returns is always about the run that is current, not
// about one three landings ago.
func redOf(item *store.Artifact) *mergeRed {
	tip := store.RedTipOf(item)
	if tip == "" {
		return nil
	}
	return &mergeRed{
		Tip:  tip,
		Base: store.GatedBaseOf(item),
		At:   store.RedAtOf(item),
		Note: store.RedNoteOf(item),
	}
}

func mergeQueueTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Target    string `json:"target"`
		TargetTip string `json:"target_tip"`
		Scope     string `json:"scope"`
		Room      string `json:"room"`
		Limit     int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, "", a.Limit)
	if err != nil {
		return nil, err
	}
	q.Kinds = []string{store.MergeKind}
	q.NotStatus = store.DoneStatus
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}

	target := strings.TrimSpace(a.Target)
	if target == "" {
		target = store.DefaultMergeTarget
	}
	tip := strings.TrimSpace(a.TargetTip)

	out := make([]mergeEntry, 0, len(list))
	for _, item := range list {
		// A request for master does not answer about a release line, and
		// mixing them is how a queue lands the right branch on the wrong
		// thing.
		if !strings.EqualFold(store.TargetOf(item), target) {
			continue
		}
		e := mergeEntry{
			Item:     item,
			Branch:   store.BranchOf(item),
			Target:   store.TargetOf(item),
			GatedTip: store.GatedTipOf(item),
			GateRun:  store.GateRunOf(item),
			Status:   store.TodoStatusOf(item),
			Red:      redOf(item),
			Blocked:  blockedOf(item, time.Now().UTC()),
		}
		// No tip, no verdict. Answering "admissible" against a tip nobody
		// stated would be the always-true check this whole surface exists to
		// prevent, and it would be worse here because it would be believed.
		if tip != "" {
			err := store.MergeAdmissible(item, tip)
			ok := err == nil
			e.Admissible = &ok
			if err != nil {
				e.Reason = err.Error()
				e.code = store.RefusalCodeOf(err)
			}
		}
		out = append(out, e)
	}

	// The rows explaining what was refused, one query for the page. No deployed
	// tip to consider here: this door never guesses one, so every refusal is
	// about the item and is explained as itself.
	codes := make([]string, 0, len(out))
	for _, e := range out {
		if e.code != "" {
			codes = append(codes, e.code)
		}
	}
	if found := knownIssues(ctx, m.db, p, codes, q.ScopeAll); found != nil {
		for i := range out {
			out[i].KnownIssue = store.PickKnownIssue(found, out[i].code)
		}
	}

	return map[string]any{
		"target":     target,
		"target_tip": tip,
		"items":      out,
		// Said plainly rather than left to be inferred from a missing key,
		// because a caller that treats "no verdict" as "yes" is the failure
		// this is guarding.
		"decided": tip != "",
	}, nil
}

// mergeGate declares a run against a request, or records what it measured.
//
// It is deliberately the same verb for both, because they are the same fact at
// two moments: this run is about this branch. Splitting them into "start" and
// "finish" would let a caller record a verdict for a run nobody ever declared,
// which is exactly the invisible window this exists to close.
func mergeGate(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		ID       string `json:"id"`
		Run      string `json:"run"`
		GatedTip string `json:"gated_tip"`
		GatedRef string `json:"gated_ref"`
		Result   string `json:"result"`
		Note     string `json:"note"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.ID) == "" {
		return nil, errors.New("merge_gate needs the request id")
	}
	// The same store verb the HTTP door calls. Two implementations of "declare a
	// gate" would drift, and what they would drift about is whether a run was
	// ever declared - which is the one thing this is for.
	// The same word-to-verb decision the HTTP door makes, and an unknown word is
	// refused rather than read as a pass: a caller who typed `fail` and got a
	// green recorded would have the queue admitting a branch their own run
	// rejected.
	var (
		art   *store.Artifact
		entry *store.Event
		err   error
	)
	switch strings.ToLower(strings.TrimSpace(a.Result)) {
	case "", "pass", "green":
		art, entry, err = m.db.SetMergeGate(ctx, p, a.ID, a.Run, a.GatedTip, a.GatedRef)
	case "red", "fail", "failed":
		art, entry, err = m.db.SetMergeRed(ctx, p, a.ID, a.Run, a.GatedTip, a.GatedRef, a.Note)
	default:
		return nil, fmt.Errorf("result %q is not one of pass, red - and a word this door "+
			"does not know must not be read as a pass", a.Result)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": art, "event": entry}, nil
}

// mergeBlockedTool records why this caller could not take a request. Same store
// verb the HTTP door calls, for the reason the gate gives: two implementations
// of "why nothing could take it" would drift about the thing a reader believes.
func mergeBlockedTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		ID  string `json:"id"`
		Why string `json:"why"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.ID) == "" {
		return nil, errors.New("merge_blocked needs the request id")
	}
	art, entry, err := m.db.SetMergeBlocked(ctx, p, a.ID, a.Why)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": art, "event": entry}, nil
}
