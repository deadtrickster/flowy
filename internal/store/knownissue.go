package store

// A REFUSAL SHOULD CARRY THE ROW THAT EXPLAINS IT.
//
// The failure this closes was measured rather than imagined. A defect was filed
// at 00:22, announced in the room twice, and still cost one agent three green
// gate runs while a second re-derived the whole thing from source forty minutes
// later - correctly, down to the same line numbers. Nobody was careless. The row
// existed, it was accurate, and it was NOT FINDABLE AT THE MOMENT OF NEED, which
// is the moment somebody is staring at a refusal they did not expect. Announcing
// harder is what already failed, twice.
//
// So the door that says no also says where this no is written down. The reader
// hits the symptom and the symptom hands them the diagnosis: no search, no
// memory of a chat message, no reading back an hour of room history to find out
// whether this is already known.
//
// WHERE THE ASSOCIATION LIVES: ON THE ROW, under ExplainsField.
//
// The alternatives were a table keyed by code, and a map in Go. A table is a
// migration, a sync rule and a second thing to keep in step with the rows it
// points at - for a fact that is one string. A map in code cannot be written by
// the person who diagnosed the defect, only by the person who next edits the
// binary, and it does not replicate: the node that has the row would still not
// have the pointer. A field on the artifact is the smallest thing that already
// survives a restart AND already replicates, because artifacts do - signed, HLC
// ordered, filtered per reader. Nothing here is new machinery; it is one key in
// the fields blob that every artifact already carries, exactly as room, category
// and supersedes are.
//
// WHO MAY ATTACH ONE. Whoever may write the row. Attaching is an edit of the
// row's own fields, so the existing write rule answers it and there is no second
// idea of authority to keep aligned with the first. Reading is the read rule,
// applied to the EXPLAINING row and not to the refusal: a refusal never cites a
// row the reader could not open, because a pointer to something unreadable is
// worse than no pointer - it says a diagnosis exists and refuses to show it, and
// it would leak what a project holds out of a door that answers everybody.
//
// ON THE WIRE it is one object, KnownIssue, under one key, `known_issue`. HTTP,
// MCP and the console render the same field off the same resolver, so there is
// one shape rather than three renderers that drift.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

// ExplainsField is where a row names the refusal it explains: a key in fields,
// beside room and supersedes, for the reason those are keys in fields.
//
// One code per row rather than a list. A list needs a containment operator to
// query and an index to stay cheap, and it buys a case that is usually two
// findings wearing one row: if a defect really explains two different doors
// saying no, the two explanations are worth writing separately, because the
// reader arrives at each of them from a different place and needs a different
// first sentence.
const ExplainsField = "explains"

// The refusal codes in use. They are the vocabulary a row is written against,
// which is why they live here beside the lookup that resolves them rather than
// beside the one door that happens to emit them: a code is a promise to whoever
// wrote `explains: merge.stale_gate` on a row, and a promise kept in one place
// is one somebody can read before making it.
//
// The name is the door and the case, dotted, so an unfamiliar one still says
// where it came from. They are STABLE: renaming one silently unhooks every row
// that cites it, which fails in the worst way this whole file exists to prevent
// - quietly, and only for the reader who needed it.
const (
	// RefusalMergeNotAnItem is an id that is not a merge queue item at all.
	RefusalMergeNotAnItem = "merge.not_a_merge_item"
	// RefusalMergeTipUnstated is admission asked without saying what the merge
	// would land on. A comparison against nothing always passes, so it is
	// refused rather than answered.
	RefusalMergeTipUnstated = "merge.tip_unstated"
	// RefusalMergeUngated is an item no gate has measured. There is no verdict
	// to be stale.
	RefusalMergeUngated = "merge.ungated"
	// RefusalMergeStaleGate is the one that costs the days: gated on a tip that
	// is not the tip it would land on.
	RefusalMergeStaleGate = "merge.stale_gate"
	// RefusalMergeTipDeployed is not a store refusal - it is the HTTP door's
	// fallback, where nobody stated a tip and the node judged against the commit
	// IT WAS BUILT FROM. Every verdict on that page is then a fact about when
	// somebody last deployed, which is why a refusal made under it is explained
	// by the deploy before it is explained by its own case.
	RefusalMergeTipDeployed = "merge.tip_deployed"
)

// CodedRefusal is a refusal that names ITSELF, not only its sentence.
//
// The sentence is for a person and it is already good - it names both tips and
// says what to re-gate against. The code is for the lookup, and it has to be a
// separate thing because prose gets reworded: a resolver that matched on the
// reason string would unhook every row the day somebody improved the wording,
// and improving the wording is a thing that happens here weekly.
//
// It is deliberately NOT part of DepRefusal. Whether a refusal is the caller's
// mistake (which is what DepRefusal answers, and what the doors map to 400) is a
// different question from whether we have written down why the rule exists. Most
// refusals will never carry a code, and the ones that do are the ones somebody
// has already had to explain twice.
type CodedRefusal interface {
	error
	RefusalCode() string
}

// RefusalCodeOf digs the code out of err, through wrapping, and answers "" when
// there is none - which is the ordinary case and must stay cheap and silent.
func RefusalCodeOf(err error) string {
	var coded CodedRefusal
	if errors.As(err, &coded) {
		return coded.RefusalCode()
	}
	return ""
}

// RefusalCode makes the queue's refusal the first coded one. The method is here
// rather than in mergequeue.go so that this whole mechanism costs that file one
// field and nothing else - it is the file the admission rule itself is being
// rewritten in, and a seam that forces two changes into one hot file is a seam
// that loses a merge.
func (e *ErrMergeNotAdmissible) RefusalCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// KnownIssue is the row that explains a refusal, as every surface renders it.
//
// Id and title because the title is what makes a reader decide to open it, and
// an id alone asks them to fetch before they can tell whether it is worth
// fetching. Ref because the id does not say where the row lives - project/type/id
// is the console's own route, built the way ReplacedByRef is built. Code because
// a reader looking at a page of refusals needs to know WHICH of them this row
// was attached to, and because a surface that groups them has no other key.
type KnownIssue struct {
	Code  string `json:"code"`
	ID    string `json:"id"`
	Title string `json:"title"`
	Ref   string `json:"ref,omitempty"`
}

// KnownIssues resolves refusal codes to the rows explaining them, for one page
// of refusals in one query.
//
// One query rather than one per refusal, for replacedBy's reason: a queue of
// thirty refused items is the normal case on a bad night, and a lookup that
// scales with the refusals is one that gets removed from the hot path later by
// somebody who then has to reintroduce it.
//
// Codes with no row come back absent rather than empty. There is no difference
// worth reporting between "nothing explains this" and "the thing that explains
// it is not yours to read", and there must not be one: the second answer would
// tell a stranger that a project holds a row about this refusal, which is the
// leak the filter is here to prevent.
//
// An OPEN row wins over a done one, and among equals the most recently updated.
// A done row still explains why the rule exists, so it is worth citing - but if
// somebody has reopened the question, the live row is the one that says what is
// being done about it now.
func (d *DB) KnownIssues(ctx context.Context, p *Principal, codes []string, scopeAll bool) (map[string]*KnownIssue, error) {
	seen := map[string]bool{}
	want := make([]string, 0, len(codes))
	for _, code := range codes {
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		want = append(want, code)
	}
	if len(want) == 0 {
		return nil, nil
	}

	a := &args{}
	codesArg := a.next(pq.Array(want))
	doneArg := a.next(DoneStatus)
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ar.fields->>'`+ExplainsField+`', ar.id, ar.title, ar.project, ar.type
		   FROM artifacts ar
		  WHERE ar.fields->>'`+ExplainsField+`' = ANY(`+codesArg+`)
		    AND coalesce(ar.tombstone, false) = false
		    AND `+filter+`
		  ORDER BY (ar.status = `+doneArg+`) ASC, ar.updated DESC, ar.id ASC`,
		a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: read the rows explaining these refusals: %w", err)
	}
	defer rows.Close()

	found := map[string]*KnownIssue{}
	for rows.Next() {
		var code, id, title, typ string
		var project sql.NullString
		if err := rows.Scan(&code, &id, &title, &project, &typ); err != nil {
			return nil, fmt.Errorf("store: read the rows explaining these refusals: %w", err)
		}
		// First wins, and the ordering above decided which that is. Later rows
		// for the same code are other people's answers to the same question;
		// citing all of them at a refusal would be a reading list rather than a
		// pointer.
		if found[code] != nil {
			continue
		}
		issue := &KnownIssue{Code: code, ID: id, Title: title}
		// A row personal to its author has no project, and a ref without one
		// names no route anybody can follow - so it gets none, and the id still
		// says what to ask for. Same reasoning as ReplacedByRef, same shape.
		if project.Valid && project.String != "" && typ != "" {
			issue.Ref = project.String + "/" + typ + "/" + id
		}
		found[code] = issue
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the rows explaining these refusals: %w", err)
	}
	if len(found) == 0 {
		return nil, nil
	}
	return found, nil
}

// PickKnownIssue is how a caller with more than one candidate code chooses: the
// first code that has a row wins.
//
// Order is the caller's judgement about WHICH QUESTION THE READER IS ACTUALLY
// ASKING, and it is not always the refusal's own case. A queue judged against a
// stale deploy refuses every item for its own honest reason, and every one of
// those reasons is a distraction from the fact that the node is four commits
// behind - so that door passes the deploy code first.
func PickKnownIssue(found map[string]*KnownIssue, codes ...string) *KnownIssue {
	for _, code := range codes {
		if issue := found[code]; issue != nil {
			return issue
		}
	}
	return nil
}
