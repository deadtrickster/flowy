package store

// A REPUDIATION IS A ROW, AND THE SUBJECT IS THE ONE WHO SIGNS IT.
//
// The operator filed this as "security: repudiation" (01M06630EZ) and it sat
// blocked on the relay experiment. That experiment reported (01M088WZ7K1A) and
// its findings decide the shape:
//
//   - provenance does NOT launder at a hop: both signatures cross intact.
//   - authorship does not TRAVEL - it is recomputed at each door, so a node
//     holding no key for the author says attributed about a row the author's
//     own node calls authored.
//   - A REFUSAL DOES NOT PROPAGATE AT ALL. C accepted, from B, a row A had
//     already refused terminally. Unpinning at A repaired nothing downstream: a
//     pin governs what a node TAKES and says nothing about what its peers
//     already took.
//
// So per-node cleanup was never going to work, and one node's refusal must not
// become another node's verdict either - refused_authorship is deliberately
// this node's own finding, and second-hand judgement arriving as a row is the
// thing that design declines.
//
// WHAT TRAVELS IS FIRST-HAND EVIDENCE. The subject signs "rows attributed to me
// in this window are not mine", with their own key, and every node holding that
// key checks it themselves. A node without the key cannot check it and shows it
// as attributed - the same rule the whole authorship path already follows, and
// the one that claims least.
//
// A WINDOW, NOT A LIST OF ROWS. This is the point flowy-claude and I converged
// on from opposite ends: a subject can only name the rows they have SEEN, and a
// stolen key wrote the ones they never did. A list of digests repudiates the
// known rows and leaves exactly the dangerous ones standing. So the object is
// (subject, from, to) - a clock range - and a digest is how a reader says WHICH
// row it is looking at, not what the claim is about.
//
// IT MARKS, IT DOES NOT DELETE, for two reasons. A row shown with "its author
// disowns this" carries strictly more information than a hole. And a verb that
// removed rows would be a censorship verb, whose first question is who else may
// call it.
//
// IT DOES NOT TOUCH THE AUTHORSHIP COLUMN. That column records whether a
// signature verified here, and that stays true of a stolen key: it really did
// sign. Repudiation is a different fact about the same row, so it is a query
// and not a rewrite - the ruling on 01M0ANFYWY, applied.
//
// WHAT IT CANNOT DO, said out loud so nobody reads it as more: it is a claim,
// not a guarantee. A peer that never syncs again keeps the old reading forever,
// and a node holding the private key could have written a repudiation too. It
// changes what a careful reader can know. It does not reach back into what has
// already been read.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RepudiationType is the artifact type. A top-level type rather than a memory
// kind, for the reason instructions are: identity is one column.
const RepudiationType = "repudiation"

const (
	// SubjectField is the principal whose authorship is being disowned - the
	// same id the authorship check reads as the author, which is OwnerUser on
	// an artifact and Actor on an event.
	SubjectField = "subject"
	// FromField and ToField bound the window, as packed clock readings. The
	// window is CLOSED at both ends: a subject saying "from here to here"
	// means both those readings too, and an off-by-one at the edge of a
	// compromise is the one row nobody would look at twice.
	FromField = "from"
	ToField   = "to"
	// SpeakerField says WHOSE claim this is, and it is not decoration - see
	// the two speakers below.
	SpeakerField = "speaker"
)

const (
	// SpeakerSubject - the principal themselves, saying "that was not me". The
	// strong form, and the only one that is first-hand.
	SpeakerSubject = "subject"
	// SpeakerOperator - the operator saying it FOR a principal who cannot,
	// because the key is gone rather than rotated. A weaker claim and a
	// different one: it is the word of whoever holds this node, which is
	// exactly the authority the principal key was introduced to move away
	// from. Kept separate for that reason - merging the two would let the
	// operator speak as anybody again, under a verb that sounds procedural.
	SpeakerOperator = "operator"
)

var repudiationSpeakers = []string{SpeakerSubject, SpeakerOperator}

// RepudiationSubjectOf reads whose authorship a repudiation disowns.
func RepudiationSubjectOf(a *Artifact) string { return repudiationField(a, SubjectField) }

// RepudiationSpeakerOf reads which of the two claims this row is.
func RepudiationSpeakerOf(a *Artifact) string { return repudiationField(a, SpeakerField) }

// RepudiationWindowOf reads the closed clock range a repudiation covers.
func RepudiationWindowOf(a *Artifact) (from, to int64) {
	if a == nil || a.Type != RepudiationType {
		return 0, 0
	}
	fields, err := ArtifactFields(a)
	if err != nil {
		return 0, 0
	}
	return fieldInt(fields, FromField), fieldInt(fields, ToField)
}

func repudiationField(a *Artifact, key string) string {
	if a == nil || a.Type != RepudiationType {
		return ""
	}
	fields, err := ArtifactFields(a)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fieldText(fields, key))
}

// maxExactFloat is 2^53: the largest integer a float64 represents exactly.
//
// A PACKED CLOCK READING IS THIRTEEN TIMES PAST IT. 117119446652354561 becomes
// 117119446652354560 the moment it passes through a float - it loses exactly
// one, which is the smallest window this store can express and precisely where
// a boundary sits.
const maxExactFloat = int64(1) << 53

// fieldInt reads a number a field may hold, as text or as a number, and
// REFUSES a number that cannot have survived the trip.
//
// encoding/json decodes every number into float64 unless it is told otherwise,
// so a reading written as a JSON number comes back rounded to an even
// neighbour. Measured on 2026-08-19 by flowy-claude, on the arm that matters:
// a repudiation window of [before+1, epoch] disowned the message at `before`,
// because before+1 and before are the same float64. Every positive assertion
// passed - the value is close enough for anything except the boundary, which is
// the whole subject of a window.
//
// So a float that is too large to be exact is not silently truncated. It reads
// as absent, which makes CheckRepudiation refuse the row, which makes
// Repudiations drop it, which means it disowns NOTHING. A repudiation whose
// window cannot be read must not disown approximately.
//
// Writers should store a reading as a STRING for the same reason; this is the
// half that protects readers from every writer that has not.
func fieldInt(fields map[string]any, key string) int64 {
	switch v := fields[key].(type) {
	case float64:
		n := int64(v)
		if n >= maxExactFloat || n <= -maxExactFloat {
			return 0
		}
		return n
	case int64:
		return v
	case json.Number:
		// What a decoder using UseNumber hands back: the digits as written,
		// with nothing lost.
		if n, err := v.Int64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// CheckRepudiation refuses a row that cannot be read back unambiguously.
func CheckRepudiation(a *Artifact) error {
	if a == nil || a.Type != RepudiationType {
		return fmt.Errorf("store: not a repudiation")
	}
	if RepudiationSubjectOf(a) == "" {
		return fmt.Errorf("store: a repudiation names the principal whose rows it disowns")
	}
	speaker := RepudiationSpeakerOf(a)
	known := false
	for _, s := range repudiationSpeakers {
		if speaker == s {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("store: %q is not a speaker - a repudiation is spoken by %s",
			speaker, strings.Join(repudiationSpeakers, " or "))
	}
	from, to := RepudiationWindowOf(a)
	if from <= 0 || to <= 0 {
		return fmt.Errorf("store: a repudiation covers a window - both ends are required, " +
			"because a claim with an open end disowns rows nobody has written yet")
	}
	if to < from {
		return fmt.Errorf("store: the window ends (%d) before it begins (%d)", to, from)
	}
	return nil
}

// WriteRepudiation stores one, having checked that the caller is entitled to
// make the claim they are making.
//
// THE SPEAKER IS NOT TAKEN ON THE ROW'S WORD. A row saying speaker=subject is a
// claim about who is speaking, and this is the door where that is decided: the
// caller must BE the subject for the strong form, and must be the operator for
// the weak one. Reading the field and believing it would make the distinction
// the two speakers exist for a field anybody can type.
func (d *DB) WriteRepudiation(ctx context.Context, p *Principal, a *Artifact, e *Event) error {
	a.Type = RepudiationType
	if err := CheckRepudiation(a); err != nil {
		return err
	}
	actor, _ := voteActor(p)
	if actor == "" {
		return fmt.Errorf("store: this token resolves to nobody, so it cannot repudiate anything")
	}
	subject := RepudiationSubjectOf(a)
	switch RepudiationSpeakerOf(a) {
	case SpeakerSubject:
		if actor != subject && (p == nil || p.UserID != subject) {
			return fmt.Errorf("store: %s cannot say that %s did not write something - "+
				"a first-hand repudiation is spoken by its subject", actor, subject)
		}
	case SpeakerOperator:
		if p == nil || !p.Operator {
			return fmt.Errorf("store: only the operator speaks for a principal who cannot " +
				"speak for themselves")
		}
	}
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write repudiation: %w", err)
	}
	d.fillAt(a, at)
	if e != nil {
		e.SeqHLC = at
		e.Artifact, e.Project = a.ID, a.Project
	}
	return d.inTx(ctx, "write repudiation "+a.ID, func(tx *sql.Tx) error {
		if err := d.upsertArtifact(ctx, tx, a); err != nil {
			return err
		}
		if e == nil {
			return nil
		}
		return d.appendEvent(ctx, tx, e)
	})
}

// Repudiations are the live ones this principal may READ, superseded and
// unreadable rows excluded. It is the list surface: what a person sees when
// they ask what this node holds.
func (d *DB) Repudiations(ctx context.Context, p *Principal) ([]*Artifact, error) {
	list, err := d.ListArtifacts(ctx, p, ArtifactQuery{Type: RepudiationType})
	if err != nil {
		return nil, err
	}
	return liveRepudiations(list), nil
}

// LiveRepudiations is every live repudiation this node holds, with NO
// permission filter, for the node's own use in marking rows.
//
// WHY THIS IS NOT A LEAK, which is the only interesting question about it.
//
// A repudiation does not reveal a row. It annotates rows the caller can ALREADY
// read, with a claim its own subject signed and published about their own
// authorship. The most it can tell you is that a principal whose row you are
// looking at has disowned a window - which is exactly what they wrote it to
// say. Nothing about the repudiation's own project, body or reason is handed
// over by the mark; those come from reading the row itself, which stays behind
// the ordinary filter (see Repudiations above).
//
// WHY THE FILTERED VERSION WAS WRONG HERE. A repudiation is a fact about a
// PRINCIPAL, and principals write in more than one project. Artifact reach is
// project-scoped, so the filtered read answered "the repudiations in your
// project" - which meant a subject had to file one per project and would leave
// rows in any project they forgot reading as authentic. `flowy principal
// repudiate` needed --project for that reason, and the requirement was the
// defect rather than the design.
//
// This is the same shape as the authorship check itself: principalKeyOf reads
// keys with no permission filter, because "whose word is this row" is not a
// question about who is asking. Marking is the same question one step on.
func (d *DB) LiveRepudiations(ctx context.Context) ([]*Artifact, error) {
	list, err := d.allArtifactsOfType(ctx, RepudiationType)
	if err != nil {
		return nil, err
	}
	return liveRepudiations(list), nil
}

// allArtifactsOfType reads every live row of one type on this node, with no
// permission filter and no limit.
//
// UNEXPORTED AND ONE CALLER, deliberately. Every read in this store goes
// through ArtifactFilterSQL, and a function that skips it is a hole waiting for
// a second caller who has not read why the first one is safe - see
// LiveRepudiations for that argument. If a second use ever appears, the
// question to ask again is whether ITS answer reveals a row or annotates one.
func (d *DB) allArtifactsOfType(ctx context.Context, typ string) ([]*Artifact, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+artifactColumns+`
		   FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false
		    AND ar.type = $1`, typ)
	if err != nil {
		return nil, fmt.Errorf("store: read every %s: %w", typ, err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: read every %s: %w", typ, err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read every %s: %w", typ, err)
	}
	// NO replacedBy PASS. It is a permission-filtered read of its own, and this
	// one deliberately has no principal to filter by. A superseded repudiation
	// is dropped by the caller on ReplacedBy being set, which the row carries
	// only when that pass ran - so this list keeps a superseded row, and the
	// effect is that a repudiation stays in force until the row REPLACING it
	// also covers what it covered. That is the safe direction: a supersede that
	// narrows a window takes effect when a filtered reader sees it, and until
	// then the wider claim stands.
	return out, nil
}

// liveRepudiations drops the ones that must not be applied to anybody.
func liveRepudiations(list []*Artifact) []*Artifact {
	keep := make([]*Artifact, 0, len(list))
	for _, a := range list {
		if strings.TrimSpace(a.ReplacedBy) != "" {
			continue
		}
		if CheckRepudiation(a) != nil {
			// A row that cannot be read back unambiguously is not applied to
			// anybody's authorship. It arrived from somewhere - a peer, an
			// older writer - and the honest answer to "what does this disown"
			// is nothing.
			continue
		}
		keep = append(keep, a)
	}
	return keep
}

// Repudiated reports whether a repudiation covers this author at this reading,
// and which row said so.
//
// It takes the list rather than reading it, because a caller marking a page of
// rows reads the repudiations ONCE and asks this per row. A version that
// queried per row would put a database round trip inside a render loop - and
// would also let two rows on one page be judged against different states.
func Repudiated(reps []*Artifact, author string, at int64) *Artifact {
	author = strings.TrimSpace(author)
	if author == "" || at <= 0 {
		return nil
	}
	for _, r := range reps {
		if RepudiationSubjectOf(r) != author {
			continue
		}
		from, to := RepudiationWindowOf(r)
		if at >= from && at <= to {
			return r
		}
	}
	return nil
}
