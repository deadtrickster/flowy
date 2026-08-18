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
	"strings"

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
	Item       *store.Artifact `json:"item"`
	Branch     string          `json:"branch"`
	Target     string          `json:"target"`
	GatedTip   string          `json:"gated_tip"`
	GateRun    string          `json:"gate_run"`
	Status     string          `json:"status"`
	Admissible *bool           `json:"admissible,omitempty"`
	Reason     string          `json:"reason,omitempty"`
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
	art, entry, err := m.db.SetMergeGate(ctx, p, a.ID, a.Run, a.GatedTip)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": art, "event": entry}, nil
}
