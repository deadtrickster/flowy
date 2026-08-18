package store

// HOW SURE WE ARE, AND ON WHAT COMMIT. This is the third axis a finding stands
// on, and it is the one that makes this a tracker rather than a list.
//
// findingupstream.go's head comment separates OUR lifecycle from what somebody
// else's tracker says. This file separates a third fact from both of them:
//
//	OUR LIFECYCLE     open, triaged, in-progress, done...   how far WE got
//	UPSTREAM FILING   unfiled, referenced, filed...         what THEIR tracker says
//	EVIDENCE          source, reproduced, verified/refuted on <sha>   HOW SURE
//
// None of the three answers either of the others, and the corpus proves it in
// both directions. Seven RAGFlow findings cite a pull request while their
// evidence still reads `source` - somebody sent a fix upstream for a defect
// nobody here ever ran. Twenty-two SereneDB findings are written up and unfiled.
// Squeeze evidence into status and "done" stops saying whether anybody ever
// watched the thing happen, which is the only question a person filing it
// upstream is actually asking.
//
// VERIFIED IS A WORD PLUS A COMMIT, AND THAT IS THE WHOLE OF IT.
// REPORTABLE-FINDINGS.md states the discipline this corpus was built around:
// nothing goes upstream until its reproduction has been run against a build of
// the most recent origin/main HEAD, WITH THAT SHA ON THE ITEM - because a report
// whose repro was run against the released image gets closed as already-fixed,
// and the sha is the only part of the claim their maintainers can check. So
// `verified` with no commit is REFUSED here rather than stored: it would be a
// stronger word than `reproduced` carrying strictly less information, and the
// list an operator works from before filing - "reproduced, but not against
// current main" - would silently lose rows to it.
//
// A RUN THAT FOUND NOTHING IS EVIDENCE, AND IT IS NOT `source`. `refuted` is
// the same act as `verified` with the opposite answer: the reproduction was run
// against a named commit and the defect did not appear. It is the strongest
// thing anybody can say about a finding and the one that STOPS a filing, so
// flattening it into "no evidence" would hide exactly the rows nobody may send
// upstream. Two of the twenty-four SereneDB reproductions are this, and both say
// so in three places - `polarity: absent`, a retitled report, and a summary
// beginning "REFUTED".
//
// A COMMIT UNDER `source` IS REFUSED TOO, for the mirror of the reason
// findingupstream.go refuses a filing's number under `referenced`: source means
// nobody ran anything, so there is no run for a commit to be the commit OF. It
// is allowed under `reproduced`, where it is the commit the run happened on and
// not yet a claim that the commit was current main - which is exactly the gap
// between the two words.
//
// AN UNSTATED EVIDENCE IS "NOBODY HAS SAID", AND IT IS NOT `source`. This is the
// one place this file differs from findingupstream.go's absence rule, and it is
// deliberate. Unfiled is a true fact about an untouched finding - nobody sent
// it. Source is a CLAIM somebody made - I read the code and I believe this is
// wrong - and defaulting to it would have the node assert that claim on behalf
// of whoever never made it. So the field stays absent, FindingEvidenceOf answers
// with an empty state, the console renders "not stated" (web/src/lib/findings.ts
// says the same thing on its side), and an empty state is refused at this door
// rather than being a way to say nothing.
//
// WHERE IT LIVES: ON THE ROW, in fields, under evidence_state, verified_on,
// verified_at and last_run - the keys the console already reads. A table of its
// own is a migration, a join on every list page and a second thing to replicate;
// fields already replicates, signed and HLC-ordered and filtered per reader.
// That is findingrepro.go's manifest rule and findingupstream.go's, and this is
// the third user of it rather than a third mechanism.
//
// THE RUN LOG IS NOT THIS. findingruns.go keeps every verdict a repro tree ever
// produced, append-only, so red-then-green across reruns is readable. This axis
// is the CURRENT claim standing on the row, folded down to one word and one
// commit, which is what a list page filters on. `last_run` is the join between
// them: the run whose log backs the claim, so a reader gets from the badge to
// the evidence in one hop.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// The keys the evidence axis rides in a finding's fields. They are the names
// web/src/lib/findings.ts already reads and they are not this file's to rename -
// a writer using keys the reader does not read is the split-brain both of the
// other axes were careful to avoid.
const (
	// EvidenceStateField is how strong the claim is. ABSENT means nobody has
	// said, which is not the same as source - see the head of this file.
	EvidenceStateField = "evidence_state"
	// VerifiedOnField is the commit the run happened on, as the caller wrote it.
	// Not parsed and not validated as a sha: the corpus runs against tags
	// ("v0.26.4") as often as against a hex commit, and an identifier that has
	// been through a parse is one that can come back different from the one
	// somebody ran.
	VerifiedOnField = "verified_on"
	// VerifiedAtField is when that run happened. Stated by the caller when a
	// claim is being imported from a file somebody wrote months ago, stamped by
	// the node otherwise - SetFindingUpstream's filed_at rule, for its reason.
	VerifiedAtField = "verified_at"
	// LastRunField is the run whose log backs the claim, so a reader gets from
	// the word to the evidence in one hop instead of scanning the run log.
	LastRunField = "last_run"
)

// The evidence vocabulary. THREE OF THESE ARE A LADDER AND THE FOURTH IS NOT,
// which is the shape of the thing rather than an untidiness in it: source,
// reproduced and verified get stronger in that order, and `refuted` is the
// NEGATIVE RESULT of the same act that produces `verified` - somebody ran the
// reproduction against a named commit and the defect did not appear.
//
// WHY REFUTED IS A WORD AND NOT A FLAG BESIDE VERIFIED. A boolean would make
// "how many have we verified" count the refutations as verifications unless
// every caller remembered to exclude them - which is findingupstream.go's
// one-filing-counted-as-eight, rebuilt on this axis. The count has to be right
// by construction, so the refutation gets its own word and falls out of the
// verified bucket without anybody remembering anything.
//
// It is also the corpus's OWN word, not one invented here: serenedb-0018's
// RESULT.md reads `status: refuted`, `polarity: absent`, and its summary says
// "REFUTED: the audit's exponential-parse inference was wrong ... Do not file."
// A vocabulary that had no word for that would have had to record the strongest
// evidence in the corpus as `source`, which is the flattening this file exists
// to prevent.
const (
	// EvidenceSource is somebody read the code and believes this is wrong. It is
	// a claim, which is why it is never defaulted to.
	EvidenceSource = "source"
	// EvidenceReproduced is somebody ran it and watched it happen. The commit
	// may ride along and is not required: plenty of the corpus was reproduced
	// before anybody wrote down what it was reproduced against, and refusing
	// that would push those rows back down to source, which is a lie in the
	// other direction.
	EvidenceReproduced = "reproduced"
	// EvidenceVerified is it was run against a NAMED COMMIT, which is recorded
	// beside it. The commit is not decoration on this word, it is the content of
	// it, so this file refuses the word without one.
	EvidenceVerified = "verified"
	// EvidenceRefuted is it was run against a named commit and DID NOT
	// reproduce. Not an absence of evidence - it is the strongest kind, and it
	// is the one that stops a filing: a report of a defect that does not appear
	// on the commit their maintainers will check is worse than no report.
	EvidenceRefuted = "refuted"
)

// EvidenceStates is the whole vocabulary, the ladder in order and the negative
// result last, so a refusal listing them reads the way the words relate.
var EvidenceStates = []string{
	EvidenceSource, EvidenceReproduced, EvidenceVerified, EvidenceRefuted,
}

// EvidenceNeedsCommit reports whether a word is one that names the commit it was
// measured on. Both outcomes of a run against a named commit do: the sha is what
// makes either of them checkable by somebody else, and a refutation with no
// commit is the more dangerous of the two - "it does not reproduce" with nothing
// saying WHERE closes a real defect.
func EvidenceNeedsCommit(state string) bool {
	return state == EvidenceVerified || state == EvidenceRefuted
}

// evidenceSpellings are the other ways the one word `verified` gets written in a
// brief or a handoff, mapped to what the node stores.
//
// "verified-on-a-commit" is how the axis is NAMED in prose, precisely because
// the commit is the content of the level - and a person who has just read that
// sentence types it. Refusing it would be this door insisting on a spelling
// while knowing exactly which state was meant, and the cost of getting it wrong
// is a backfill that silently leaves rows unset. One stored word, several
// accepted ones: the vocabulary is still closed, the row still reads `verified`,
// and nothing downstream learns a second spelling.

var evidenceSpellings = map[string]string{
	"verified-on-a-commit": EvidenceVerified,
	"verified-on-commit":   EvidenceVerified,
	"verified_on_a_commit": EvidenceVerified,
	// The corpus writes a refutation as a polarity beside a status
	// (`polarity: absent`) and as prose in a title ("not reproduced on main"),
	// so an importer reaching for either phrase means this one word.
	"not-reproduced": EvidenceRefuted,
	"not_reproduced": EvidenceRefuted,
	"absent":         EvidenceRefuted,
}

// EventFindingEvidence is what recording an evidence claim is in the log.
// Minted - see mintedEventTypes in sync.go and mintedTypes in api.go - for
// EventFindingUpstream's reason: every refusal that makes the claim a fact
// rather than a word is on the verb, and an entry a client could hand over
// would be "verified" with no commit, claimed against a finding the writer may
// not be able to read, with nothing on the row to match it.
const EventFindingEvidence = "finding.evidence"

// FindingEvidenceRoom is where an entry lands when the finding it is about names
// no room of its own - findingRunRoom's rule, and upstreamRoom's: an entry
// nobody can find in a room is an entry nobody reads.
const FindingEvidenceRoom = "findings"

// Evidence is how strong a finding's claim is and what it rests on: what a
// caller states to SetFindingEvidence and what FindingEvidenceOf reads back off
// the row, in the keys named above, so the shape a reader parses and the shape a
// writer writes are one shape.
//
// State IS omitempty here, where UpstreamFiling.State is not, and for the reason
// the head of this file gives: an empty state is a real and common answer -
// nobody has said - rather than a node that declined to answer.
type Evidence struct {
	State      string `json:"evidence_state,omitempty"`
	VerifiedOn string `json:"verified_on,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	LastRun    string `json:"last_run,omitempty"`
}

// Stated reports whether anybody has said how strong this finding's evidence is.
// The question every surface asks first, and the one a bare `== ""` at each call
// site gets subtly wrong the day a fourth word is added.
func (e Evidence) Stated() bool { return e.State != "" }

// Ran reports whether somebody actually ran the reproduction, as opposed to
// having read the code. Every word but source means a run happened - INCLUDING
// refuted, which is a run that produced the opposite answer and is not a weaker
// kind of having-not-run.
func (e Evidence) Ran() bool {
	return e.State != "" && e.State != EvidenceSource
}

// Reproduces reports whether the defect ACTUALLY APPEARED the last time anybody
// looked. This is the question a filing asks, and it is not "is the evidence
// strong": refuted is strong evidence that there is nothing to file.
//
// source answers false, and that is right for this question rather than a
// rounding of it - nobody has made it happen, so nobody can say it happens.
func (e Evidence) Reproduces() bool {
	return e.State == EvidenceReproduced || e.State == EvidenceVerified
}

// evidenceRefusalError is what every refusal this verb makes ABOUT THE CLAIM IT
// WAS ASKED TO RECORD satisfies: the caller's mistake, fixable by the caller. It
// is DepRefusal's interface rather than a fourth one, so a refusal added here
// cannot be one HTTP maps to 500 and MCP reports as a broken node.
type evidenceRefusalError struct{ reason string }

func (e evidenceRefusalError) Error() string { return e.reason }
func (e evidenceRefusalError) depRefusal()   {}

func refuseEvidence(format string, a ...any) error {
	return evidenceRefusalError{reason: fmt.Sprintf(format, a...)}
}

// NormalizeEvidenceState validates the word a write asks for and returns it as
// the node stores it.
//
// Case and surrounding space are the caller's typing rather than a different
// state, so they come off - NormalizeUpstreamState's rule. EMPTY IS REFUSED,
// which is where this differs from that one: not having filed something is the
// ordinary condition of a finding and reads as a fact, but "nobody has said how
// sure we are" is not a claim anybody can make, and a verb that accepted it
// would be a way of writing an assertion that asserts nothing.
func NormalizeEvidenceState(asked string) (string, error) {
	state := strings.ToLower(strings.TrimSpace(asked))
	if spelling, ok := evidenceSpellings[state]; ok {
		state = spelling
	}
	if state == "" {
		return "", refuseEvidence("an evidence claim says how strong it is: one of %s. "+
			"An unstated evidence is \"nobody has said\", which is what a finding carrying "+
			"none of these keys already reads as - it is not something to write",
			strings.Join(EvidenceStates, ", "))
	}
	for _, known := range EvidenceStates {
		if state == known {
			return state, nil
		}
	}
	return "", refuseEvidence("%q is not one of the words evidence comes in: %s. %q and %q are "+
		"each a word AND a commit, and %q is the run that found nothing rather than the "+
		"absence of one - see internal/store/findingevidence.go",
		asked, strings.Join(EvidenceStates, ", "), EvidenceVerified, EvidenceRefuted,
		EvidenceRefuted)
}

// FindingEvidenceOf is how strong a finding's evidence is, read off the row and
// nothing else.
//
// A row carrying none of the keys - every finding raised before this file
// existed, and 40 of 40 in the corpus at the moment it landed - answers with an
// EMPTY state, and that is the honest answer rather than a missing one. A row
// whose fields do not parse answers the same way, artifactField's rule.
//
// The state comes back AS STORED rather than re-validated. The write door is the
// gate, and a word that arrived from a peer whose vocabulary is wider than this
// node's is a fact to show a reader, not one to silently rename - which is also
// what the console does with an unfamiliar word rather than folding it away.
func FindingEvidenceOf(a *Artifact) Evidence {
	var evidence Evidence
	if a == nil || len(a.Fields) == 0 {
		return evidence
	}
	var raw struct {
		State      string `json:"evidence_state"`
		VerifiedOn string `json:"verified_on"`
		VerifiedAt string `json:"verified_at"`
		LastRun    string `json:"last_run"`
	}
	if err := json.Unmarshal(a.Fields, &raw); err != nil {
		return evidence
	}
	evidence.State = strings.ToLower(strings.TrimSpace(raw.State))
	evidence.VerifiedOn = strings.TrimSpace(raw.VerifiedOn)
	evidence.VerifiedAt = strings.TrimSpace(raw.VerifiedAt)
	evidence.LastRun = strings.TrimSpace(raw.LastRun)
	return evidence
}

// normalizeVerifiedAt takes what the caller stated and returns what the row
// carries, over upstreamDateFormats - the same two shapes filed_at accepts, and
// deliberately the same list rather than a second one: a corpus written by hand
// carries "2026-08-07" and this node writes RFC3339, and two lists would drift
// into one door accepting what the other refuses.
//
// Free text is REFUSED rather than stored, normalizeFiledAt's reason: a
// timestamp nothing can parse is a column no console can sort and no importer
// can be checked against.
func normalizeVerifiedAt(asked string) (string, error) {
	at := strings.TrimSpace(asked)
	if at == "" {
		return "", nil
	}
	for _, layout := range upstreamDateFormats {
		if t, err := time.Parse(layout, at); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", refuseEvidence("verified_at %q is not a time: state it as 2006-01-02 or as a "+
		"full RFC3339 timestamp, or leave it out and the node stamps now", asked)
}

// SetFindingEvidence records how strong a finding's evidence is and what it
// rests on: the claim on the row and the entry in the log, in one write.
//
// AN UPDATE STATES WHAT CHANGES. verified_on, verified_at and last_run left
// empty are inherited from the claim already on the row - SetFindingUpstream's
// rule - so {state: verified} over a row that already names the commit its repro
// ran on is a promotion rather than a restatement, and the commit is not lost by
// being left out.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and a claim
//     nobody made is not one.
//   - an empty state, or a word outside the vocabulary. See
//     NormalizeEvidenceState on why empty is not a way of saying nothing.
//   - a verified_at that is not a time.
//   - an id that does not name a finding this principal may READ - readFinding's
//     answer, the same one WriteFindingRepro and RecordFindingRun ask first.
//   - a finding with no project, for RecordFindingRun's reason: a projectless
//     event is read back by its actor alone, so the entry behind the claim would
//     be invisible to everyone else who can read the finding.
//   - a commit, a time or a run under `source`. Source is nobody ran it, so
//     there is no run for any of the three to be about.
//   - VERIFIED WITH NO COMMIT, on the call or already on the row. This is the
//     refusal the whole axis exists for: the sha is the content of the word, and
//     without it `verified` is `reproduced` spelled more confidently.
//
// It does NOT refuse a restatement, and it does not refuse going back DOWN the
// vocabulary. A repro that stops reproducing is a real thing to record, and the
// entry in the log keeps the commit the old claim rested on even though the row
// stops carrying it - which is why the log is written in the same transaction
// rather than being optional.
func (d *DB) SetFindingEvidence(
	ctx context.Context, p *Principal, findingID string, asked Evidence,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, EventFindingEvidence)
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter, and p.UserID rides the meta beside it so a
	// reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseEvidence("this token resolves to nobody, so it cannot say how " +
			"strong the evidence for a finding is")
	}
	state, err := NormalizeEvidenceState(asked.State)
	if err != nil {
		return nil, nil, err
	}
	verifiedAt, err := normalizeVerifiedAt(asked.VerifiedAt)
	if err != nil {
		return nil, nil, err
	}
	finding, err := d.readFinding(ctx, p, strings.TrimSpace(findingID))
	if err != nil {
		return nil, nil, err
	}
	if finding.Project == nil || *finding.Project == "" {
		return nil, nil, refuseEvidence("finding %s has no project and is its owner's alone, so "+
			"the entry behind an evidence claim on it would be readable by whoever recorded it "+
			"rather than by whoever can read the finding - write it at scope=project or "+
			"scope=shared first", finding.ID)
	}

	stood := FindingEvidenceOf(finding)
	next := Evidence{
		State:      state,
		VerifiedOn: strings.TrimSpace(asked.VerifiedOn),
		VerifiedAt: verifiedAt,
		LastRun:    strings.TrimSpace(asked.LastRun),
	}

	if state == EvidenceSource {
		// Nobody ran it, so nothing may name a run. This is the mirror of
		// SetFindingUpstream refusing a filing's number under `referenced`: a
		// value in a key that contradicts the word beside it is a row that says
		// two things, and a reader can only ever pick the wrong one.
		if next.VerifiedOn != "" || next.VerifiedAt != "" || next.LastRun != "" {
			return nil, nil, refuseEvidence("finding %s: %q is nobody ran it, so it cannot name "+
				"the commit a run happened on. Say %q for a run with no commit recorded, or %q "+
				"with the commit it ran against",
				finding.ID, EvidenceSource, EvidenceReproduced, EvidenceVerified)
		}
		// The keys go, and the entry keeps what they said. A claim walked back
		// to source is a claim about the code, and leaving a commit on the row
		// under it would leave the old run looking like it still counts.
		next.VerifiedOn, next.VerifiedAt, next.LastRun = "", "", ""
	} else {
		if next.VerifiedOn == "" {
			next.VerifiedOn = stood.VerifiedOn
		}
		if next.LastRun == "" {
			next.LastRun = stood.LastRun
		}
		if EvidenceNeedsCommit(state) && next.VerifiedOn == "" {
			return nil, nil, refuseEvidence("finding %s: %q names the commit the reproduction was "+
				"run against - without it %q is %q written more confidently, and %q is "+
				"\"it does not happen\" with nothing saying where, which is how a real defect "+
				"gets closed. State verified_on, or say %q",
				finding.ID, state, EvidenceVerified, EvidenceReproduced, EvidenceRefuted,
				EvidenceReproduced)
		}
		switch {
		case next.VerifiedOn == "":
			// Reproduced with no commit recorded: there is nothing for a time to
			// be the time OF. A stated one is REFUSED rather than dropped -
			// silently discarding a value the caller wrote is how a writer comes
			// to believe a fact is recorded when it is not.
			if next.VerifiedAt != "" {
				return nil, nil, refuseEvidence("finding %s: verified_at is when the run "+
					"named by verified_on happened, and this claim names no commit. State "+
					"verified_on beside it, or leave the time out",
					finding.ID)
			}
		case next.VerifiedAt == "":
			// The claim that continues keeps the day its run was made; a run
			// against a different commit is stamped now. A word moving from
			// reproduced to verified over the same commit is not a new run and
			// must not look like one on the row.
			if next.VerifiedOn == stood.VerifiedOn && stood.VerifiedAt != "" {
				next.VerifiedAt = stood.VerifiedAt
			} else {
				next.VerifiedAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
	}

	fields, err := ArtifactFields(finding)
	if err != nil {
		return nil, nil, err
	}
	setEvidenceFields(fields, next)
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: record finding %s evidence: %w", finding.ID, err)
	}

	entry, err := evidenceEntryEvent(finding, p, actor, actorKind, stood, next)
	if err != nil {
		return nil, nil, err
	}
	// One transaction, one clock reading, both rows or neither: a claim with no
	// entry behind it is a word nobody can trace to whoever made it, and an entry
	// with no claim behind it is a log that lies.
	if err := d.SetArtifactFields(ctx, finding, column, entry); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(finding.ID)
	return finding, entry, nil
}

// setEvidenceFields writes the claim into an artifact's fields map, DELETING the
// keys it has nothing to say about rather than writing empty strings.
//
// setUpstreamFields' rule and its reason: an empty string and an absent key
// would be two spellings of "nobody has said", and every reader would have to
// treat them alike forever. Deleting keeps one spelling, which is also what
// every row written before this file looks like.
func setEvidenceFields(fields map[string]any, e Evidence) {
	for key, value := range map[string]string{
		EvidenceStateField: e.State,
		VerifiedOnField:    e.VerifiedOn,
		VerifiedAtField:    e.VerifiedAt,
		LastRunField:       e.LastRun,
	} {
		if value == "" {
			delete(fields, key)
			continue
		}
		fields[key] = value
	}
}

// EvidenceEntry is one entry in the log behind a finding's evidence: where the
// claim went, where it came from, WHICH COMMIT, who said so and when.
//
// It carries the previous commit as well as the new one, which is what makes a
// re-verification readable: "verified on 67adbe04, verified on 1fa4374" is two
// commits and one story, and an entry naming only the current one would leave a
// reader unable to tell a re-run from a word that merely moved.
type EvidenceEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Finding string `json:"finding"`
	// State and From are not omitempty, for StatusEntry's reason: an entry that
	// left one out would leave a client deciding whether it means "not stated"
	// or means the node did not say.
	State      string `json:"state"`
	From       string `json:"from"`
	VerifiedOn string `json:"verified_on,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	LastRun    string `json:"last_run,omitempty"`
	FromOn     string `json:"from_verified_on,omitempty"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind,omitempty"`
	ActorUser  string `json:"actor_user,omitempty"`
	SeqHLC     int64  `json:"seq_hlc"`
	Node       string `json:"node"`
	Created    string `json:"created"`
}

// evidenceEntryEvent builds the entry a claim is.
func evidenceEntryEvent(
	finding *Artifact, p *Principal, actor, actorKind string, from, to Evidence,
) (*Event, error) {
	meta, err := json.Marshal(map[string]any{
		"state":            to.State,
		"from":             from.State,
		VerifiedOnField:    to.VerifiedOn,
		VerifiedAtField:    to.VerifiedAt,
		LastRunField:       to.LastRun,
		"from_verified_on": from.VerifiedOn,
		"actor_kind":       actorKind,
		"actor_user":       p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: evidence of %s: %w", finding.ID, err)
	}
	return &Event{
		Type:    EventFindingEvidence,
		Project: finding.Project,
		Room:    evidenceRoom(finding),
		Thread:  finding.ID,
		// The finding itself, which is what decides who reads the entry: the
		// people who can read the work are the people its evidence is about.
		Artifact: finding.ID,
		Actor:    actor,
		Body:     evidenceBody(from, to),
		Meta:     meta,
	}, nil
}

// EvidenceEntryOf renders one event as the entry it is.
func EvidenceEntryOf(e *Event) EvidenceEntry {
	entry := EvidenceEntry{
		ID: e.ID, Type: e.Type, Finding: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta struct {
		State      string `json:"state"`
		From       string `json:"from"`
		VerifiedOn string `json:"verified_on"`
		VerifiedAt string `json:"verified_at"`
		LastRun    string `json:"last_run"`
		FromOn     string `json:"from_verified_on"`
		ActorKind  string `json:"actor_kind"`
		ActorUser  string `json:"actor_user"`
	}
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.State, entry.From = meta.State, meta.From
		entry.VerifiedOn, entry.VerifiedAt = meta.VerifiedOn, meta.VerifiedAt
		entry.LastRun, entry.FromOn = meta.LastRun, meta.FromOn
		entry.ActorKind, entry.ActorUser = meta.ActorKind, meta.ActorUser
	}
	return entry
}

// FindingEvidenceLog is every evidence entry naming this finding that p may
// read, oldest first - so a reader sees read-the-code, then ran-it, then
// ran-it-on-this-commit, rather than only where it ended up. It is
// FindingUpstreamLog for the third axis, with the same permission story: the
// filter is in the WHERE clause and it is not a second rule.
func (d *DB) FindingEvidenceLog(
	ctx context.Context, p *Principal, findingID string,
) ([]EvidenceEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "finding.evidence.log")
	defer span.End()

	events, err := d.findingEvidenceEvents(ctx, p, []string{findingID})
	if err != nil {
		return nil, err
	}
	out := make([]EvidenceEntry, 0, len(events))
	for _, e := range events {
		out = append(out, EvidenceEntryOf(e))
	}
	return out, nil
}

// findingEvidenceEvents reads the entries naming any of findings, in log order,
// through the same event filter every other read of a log uses.
//
// There is no LIMIT, for depEvents' reason: a finding's evidence log is a
// handful of entries, and a page that stopped early would show a history that is
// not the history.
func (d *DB) findingEvidenceEvents(
	ctx context.Context, p *Principal, findings []string,
) ([]*Event, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "evidence events", func(a *args) string {
		idsArg := a.next(pq.Array(findings))
		typeArg := a.next(EventFindingEvidence)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// evidenceRoom is where an entry lands in the log: the room the finding was
// raised in, or the findings room when it names none - findingRunRoom's rule.
func evidenceRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return FindingEvidenceRoom
}

// evidenceBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
//
// It names the COMMIT at each end, upstreamBody's rule, because "verified on
// 67adbe04" and "verified on 67adbe04, verified on 1fa4374" are different facts
// and only the pair says which happened. A finding nobody had said anything
// about reads as "not stated" rather than as an empty half.
func evidenceBody(from, to Evidence) string {
	return evidenceSide(from) + "->" + evidenceSide(to)
}

func evidenceSide(e Evidence) string {
	if !e.Stated() {
		return "not stated"
	}
	if e.VerifiedOn != "" {
		return e.State + " on " + e.VerifiedOn
	}
	return e.State
}
