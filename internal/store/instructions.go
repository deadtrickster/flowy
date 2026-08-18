package store

// Instructions as rows.
//
// The operator asked for this after one rule drifted for the fifth time in a
// day, and the diagnosis on 01M09RCSD7 is not that agents forget. It is that
// the rules live in files on disk, one copy per agent, and nothing reconciles
// them: nobody can read anybody else's, a rule changed at 06:00 never reaches a
// seat that started at 22:00, and there is no record of who decided a rule or
// when - so after a day a rule and a habit look the same.
//
// NOTHING NEW IS INVENTED HERE. An instruction is an artifact with a type, a
// scope in fields, and the supersede machinery every other row already has.
// That is the whole point: instructions stop being the one kind of knowledge in
// this system that is not a row.
//
// A TOP-LEVEL TYPE, not a memory kind. Five things already exist at both levels
// - todo, note, report, diagram - and the ruling on 01M0ANFYWY is that identity
// is one column. Adding the sixth would cement the ambiguity that ruling
// exists to remove.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// InstructionType is the artifact type.
const InstructionType = "instruction"

// ScopeSeat is one agent's own rules, beside ScopeNode and ScopeProject which
// announcements already use. The three are read together and never merged -
// see InstructionsFor.
const ScopeSeat = "seat"

// ScopeField is where the scope lives, the same key announcements use.
const ScopeField = "scope"

// SeatField names whose rules a seat-scoped instruction is. Empty on the other
// two scopes, and required on this one: an instruction scoped to a seat with no
// seat named applies to nobody and reads as applying to everybody.
const SeatField = "seat"

// instructionScopes is the closed set, in the order they compose.
//
// ORDER IS THE PRODUCT HERE. Node first, then project, then seat - widest to
// narrowest - because that is the order a reader should meet them in, and
// because the narrowest is the one most likely to be a local exception worth
// noticing.
var instructionScopes = []string{ScopeNode, ScopeProject, ScopeSeat}

// InstructionScopeOf reads an artifact's scope, or "" when it is not an
// instruction or carries no scope.
func InstructionScopeOf(a *Artifact) string {
	if a == nil || a.Type != InstructionType {
		return ""
	}
	fields, err := ArtifactFields(a)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fieldText(fields, ScopeField))
}

// InstructionSeatOf reads which seat a seat-scoped instruction belongs to.
func InstructionSeatOf(a *Artifact) string {
	if a == nil || a.Type != InstructionType {
		return ""
	}
	fields, err := ArtifactFields(a)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fieldText(fields, SeatField))
}

// CheckInstruction refuses a row that cannot be read back unambiguously.
//
// Called before the write rather than trusted at read time, for the reason
// checkQueueRow is: a row that means two things is easier to refuse than to
// interpret, and the interpretation would then live in every reader.
func CheckInstruction(a *Artifact) error {
	if a == nil || a.Type != InstructionType {
		return fmt.Errorf("store: not an instruction")
	}
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("store: an instruction needs a title - it is what a reader cites " +
			"when they say which rule bound them")
	}
	scope := InstructionScopeOf(a)
	known := false
	for _, s := range instructionScopes {
		if scope == s {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("store: %q is not a scope - an instruction is %s",
			scope, strings.Join(instructionScopes, ", "))
	}
	if scope == ScopeSeat && InstructionSeatOf(a) == "" {
		return fmt.Errorf("store: a seat-scoped instruction must name its seat, or it applies " +
			"to nobody and reads as applying to everybody")
	}
	if scope == ScopeProject && (a.Project == nil || strings.TrimSpace(*a.Project) == "") {
		return fmt.Errorf("store: a project-scoped instruction must be IN a project")
	}
	return nil
}

// WriteInstruction stores one, with the event that says who decided it.
//
// The event is not decoration. "Who decided this rule and when" is the question
// a file on disk cannot answer, and it is answered here by the same append-only
// log that answers it for every other row.
func (d *DB) WriteInstruction(ctx context.Context, a *Artifact, e *Event) error {
	a.Type = InstructionType
	if err := CheckInstruction(a); err != nil {
		return err
	}
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write instruction: %w", err)
	}
	d.fillAt(a, at)
	if e != nil {
		e.SeqHLC = at
		e.Artifact, e.Project = a.ID, a.Project
	}
	return d.inTx(ctx, "write instruction "+a.ID, func(tx *sql.Tx) error {
		if err := d.upsertArtifact(ctx, tx, a); err != nil {
			return err
		}
		if e == nil {
			return nil
		}
		return d.appendEvent(ctx, tx, e)
	})
}

// InstructionsFor is the ordered set that applies to this principal: node,
// then project, then their own seat.
//
// IT COMPOSES NOTHING, AND THAT IS THE DESIGN. The obvious version resolves
// conflicts - narrower scope wins - and that is precisely the failure being
// fixed: today the winner is whichever file loaded last and nobody can say
// which rule bound them. So this returns the rows, each carrying its scope, and
// leaves the reading to the reader. Two rules that contradict are visible as
// two rules instead of one silently discarded.
//
// Superseded rows are excluded: an edited instruction is a new row that
// supersedes the old, and the old one stays for the record rather than for the
// reader. ReplacedBy is what says so, and it is filled by the same read paths
// that fill it everywhere else.
func (d *DB) InstructionsFor(ctx context.Context, p *Principal) ([]*Artifact, error) {
	list, err := d.ListArtifacts(ctx, p, ArtifactQuery{Type: InstructionType})
	if err != nil {
		return nil, err
	}

	seat := ""
	if p != nil {
		seat, _ = voteActor(p)
	}
	rank := map[string]int{ScopeNode: 0, ScopeProject: 1, ScopeSeat: 2}

	keep := make([]*Artifact, 0, len(list))
	for _, a := range list {
		if strings.TrimSpace(a.ReplacedBy) != "" {
			continue
		}
		switch InstructionScopeOf(a) {
		case ScopeNode, ScopeProject:
			keep = append(keep, a)
		case ScopeSeat:
			// SOMEBODY ELSE'S SEAT RULES ARE NOT MINE. They are readable -
			// permission is a separate question and the filter above already
			// answered it - but they do not APPLY, and a list that mixes them
			// in is a list an agent would follow.
			if seat != "" && InstructionSeatOf(a) == seat {
				keep = append(keep, a)
			}
		}
	}
	sort.SliceStable(keep, func(i, j int) bool {
		ri, rj := rank[InstructionScopeOf(keep[i])], rank[InstructionScopeOf(keep[j])]
		if ri != rj {
			return ri < rj
		}
		// Oldest first within a scope, so a set read twice reads the same way
		// and a new rule appears at the end of its scope rather than in the
		// middle of rules somebody has already read.
		return keep[i].Created.Before(keep[j].Created)
	})
	return keep, nil
}

// EventInstruction is what a rule being written or changed looks like in the
// log. It is a distinct type rather than a note so "when did this rule change"
// is a query rather than a search.
const EventInstruction = "instruction"
