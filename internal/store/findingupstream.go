package store

// WHERE A FINDING IS FILED, WHICH IS A FACT ABOUT SOMEBODY ELSE'S TRACKER, AND
// WHAT IT CITES, WHICH IS A DIFFERENT FACT AGAIN.
//
// A finding has two axes and they were being asked to share one column. The
// first is OUR lifecycle - open, triaged, in-progress, in-review, done - which
// lifecycle.go owns and which says how far this project has got with writing
// the thing up. The second is what happened to it SOMEWHERE ELSE: an issue
// exists in SereneDB's or RAGFlow's tracker, with a number, and their
// maintainers have or have not taken it. Neither answers the other. A finding
// can be done and unfiled - written up, nobody sent it, which is the state of 22
// of the 24 SereneDB findings - and a filed one can sit open upstream for a year
// while nothing happens here.
//
// Squeezed into `status` the pair collapses: "done" stops saying whether WE
// fixed it or THEY took it, and the issue number - the only part a reader can
// act on - has nowhere to live at all. That is the value-without-its-referent
// defect this project keeps closing, one field along.
//
// A REFERENCE IS NOT A FILING, AND THE CORPUS PROVES IT. The import's dry run
// over 40 real rows set filed wherever an issue number appeared, and reported 8
// filed where the corpus says 1. Seven RAGFlow findings carry a pull request
// number while their evidence still reads `source`: somebody sent a fix upstream
// for a defect nobody here ever reproduced. A finding can also cite an issue as
// context - "this reproduces under the conditions of #12109" - with nobody
// having filed anything. So the presence of a number says nothing about whether
// this was submitted, and nothing in this file ever infers one from the other:
// the filing is a WORD somebody states, and the citations are a LIST beside it.
//
// AND ONE UPSTREAM ARTIFACT COVERS SEVERAL FINDINGS. RAGFlow PR #16958 is
// findings 01, 04 and 05; #16959 is 02 and 03; #17236 is 11, 12 and 13. A single
// number on the row cannot say "these three went together", which is exactly the
// question asked the moment that PR is turned down. So references are many per
// finding, the same reference appears on every finding it covers, and "which
// findings are in #16958" is a containment query over one key rather than a
// join. A reference is a VALUE, not an entity: an entity would need identity, a
// migration and a dedup rule across trackers, and it would answer the same
// question no better.
//
// AN ISSUE AND A PULL REQUEST ARE DIFFERENT CLAIMS - we told them, and we sent
// them a fix - so a reference carries which it is. The corpus field they came
// out of held both at once ("#12109 / PR #16959"), sometimes a URL, sometimes a
// bare number, which is why the kind is a closed set here and refused at the
// write.
//
// WHERE IT LIVES: ON THE ROW, in fields, under the keys the corpus importers
// were given - upstream_tracker, upstream_id (a string, since not every tracker
// numbers with integers), upstream_url, upstream_state, filed_at, filed_by -
// plus upstream_kind and upstream_refs, which the dry run showed the corpus
// needs.
//
// A table of its own is a migration, a join on every list page and a second
// thing to replicate. An artifact relation needs an artifact at the far end, and
// their issue #123 is not one here - nothing signs it, nothing replicates it,
// and a relation could not carry the state word anyway. A key in fields is what
// findingrepro.go's manifest, knownissue.go's `explains` and todocategory.go's
// category all are, for the reason written down there: it already replicates,
// signed and HLC-ordered and filtered per reader, and it costs no new machinery.
//
// IT IS NOT artifacts.external AND NOT internal/forge. That pair is this node
// filing an artifact as an issue ITSELF: ExternalRef carries the sync's
// bookkeeping - Since, Seen, Pushed, the login the node's own comments arrive
// under - and forge.Select picks ONE client with ONE credential at startup.
// Three things break if an upstream filing is written there. The repo belongs to
// somebody else, so the sync loop would poll a tracker this node holds no
// credential for and log a failure every pass. The state vocabulary is not the
// same list: forge normalises everything to open|closed|merged and
// forge.Terminal maps both ways out onto done, so "they accepted it" and "they
// rejected it" would arrive here as one word - exactly the collapse this file
// exists to prevent, rebuilt in the layer below. And `reported` would start
// meaning two things, since it already means this node opened the issue.
//
// The seam is left open rather than nailed shut: if a node ever does file a
// finding upstream through its own forge client, that path calls
// SetFindingUpstream with the number FileIssue handed back. The filing is the
// FACT; the forge is one way of acquiring it, and today the usual way is a
// person who filed it by hand months ago and wrote the number in a markdown
// file.
//
// UNFILED IS THE ABSENCE OF THE FACT, not a value somebody must remember to
// set. A finding carrying none of these keys reads as unfiled, which is what
// FindingUpstreamOf answers and what makes the common case - 22 of 24 - free at
// import time and free for every row raised since findings existed.
//
// FILED TWICE IS REFUSED WHILE THE FIRST FILING STANDS. This is
// ErrAlreadyFiled's reasoning in forge.go: overwriting #123 with #456 leaves
// #123 live on their tracker with nothing anywhere pointing at it, and nobody
// finds it again. So a call naming a different filing than the one on the row is
// refused and told which one stands. It is ALLOWED once the standing one no
// longer does - rejected or withdrawn - because re-filing a rejected finding
// somewhere else is a real thing to do, and the number it replaces stays in the
// log and in the citation list either way.
//
// WITHDRAWN IS A WORD OF OUR OWN, beside their five. Withdrawing a filing fits
// none of theirs: back to unfiled would erase the number of an issue that exists
// on their tracker - inviting the duplicate filing this file is otherwise
// careful to prevent - and rejected would attribute our own retraction to their
// maintainers, who never looked at it. Importers never write it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// The keys an upstream filing rides in a finding's fields. The first six are the
// names the corpus importers were given and they are not this file's to rename:
// an importer writing keys the reader does not read is the split-brain the whole
// row exists to prevent.
const (
	// UpstreamTrackerField is whose tracker THE FILING is in - "serenedb",
	// "ragflow", or a host for one that has no short name here. It describes
	// the filing and never a citation: those are in upstream_refs, and putting
	// one here is what turned 1 filed row into 8.
	UpstreamTrackerField = "upstream_tracker"
	// UpstreamIDField is their number for the filing, AS A STRING. Not every
	// tracker numbers with integers, and an id that has been through a parse is
	// an id that can come back out different from the one they gave.
	UpstreamIDField = "upstream_id"
	// UpstreamURLField is the link, so a reader gets to it in one click rather
	// than reconstructing a URL from a tracker name.
	UpstreamURLField = "upstream_url"
	// UpstreamStateField is where the filing stands. It is the ONLY thing that
	// says whether this was sent upstream - see the head of this file.
	UpstreamStateField = "upstream_state"
	// UpstreamFiledAtField is when it was filed, which is frequently long
	// before this row existed - see SetFindingUpstream on why the caller may
	// state it.
	UpstreamFiledAtField = "filed_at"
	// UpstreamFiledByField is who filed it, for the same reason: the person who
	// sent it upstream is often not the principal recording that they did.
	UpstreamFiledByField = "filed_by"
	// UpstreamKindField is whether the filing is an issue or a pull request.
	// "We reported it" and "we sent a fix" are different claims, and the corpus
	// field these came out of held both in one string.
	UpstreamKindField = "upstream_kind"
	// UpstreamRefsField is everything upstream this finding TOUCHES - issues and
	// pull requests, many per finding, the same one repeated on every finding it
	// covers. It asserts nothing about whether anybody filed anything.
	UpstreamRefsField = "upstream_refs"
)

// The filing vocabulary. Five of these are THEIR states, named as theirs, one is
// ours, and one - referenced - is the answer to a question neither side asked:
// there are numbers here and nobody claims to have sent anything.
const (
	// UpstreamUnfiled is nobody sent it and it cites nothing. It is what an
	// unstated filing reads as, so a finding raised in the ordinary way is
	// unfiled without anybody having to say so.
	UpstreamUnfiled = "unfiled"
	// UpstreamReferenced is the case the dry run found: the finding names issues
	// or pull requests over there, and NOBODY IS CLAIMING WE FILED IT. Seven of
	// the sixteen RAGFlow findings are this. It is not a weaker "filed" and it
	// must never be read as one.
	UpstreamReferenced = "referenced"
	// UpstreamFiled is we sent it and nobody has ruled on it.
	UpstreamFiled = "filed"
	// UpstreamAccepted is their maintainers agreed it is a defect.
	UpstreamAccepted = "accepted"
	// UpstreamFixed is they landed something. It says nothing about our own
	// lifecycle, which is the entire point of this file.
	UpstreamFixed = "fixed"
	// UpstreamRejected is their judgement: not a defect, not one they will take.
	UpstreamRejected = "rejected"
	// UpstreamWithdrawn is OUR retraction - we pulled it back. Kept apart from
	// rejected because who decided is the fact worth keeping.
	UpstreamWithdrawn = "withdrawn"
)

// UpstreamStates is the whole vocabulary, in the order a filing travels through
// it, so a refusal listing them reads the way the story goes.
var UpstreamStates = []string{
	UpstreamUnfiled, UpstreamReferenced, UpstreamFiled, UpstreamAccepted,
	UpstreamFixed, UpstreamRejected, UpstreamWithdrawn,
}

// What an upstream reference IS. A closed set, refused at the write, because
// "we told them" and "we sent them a fix" are the two different claims the
// corpus field mixed together and a reader cannot recover the difference later.
const (
	UpstreamKindIssue = "issue"
	UpstreamKindPR    = "pr"
)

// UpstreamKinds is that set, for a refusal that has to name it.
var UpstreamKinds = []string{UpstreamKindIssue, UpstreamKindPR}

// EventFindingUpstream is what recording a filing is in the log. Minted - see
// mintedEventTypes in sync.go and mintedTypes in api.go - for the reason
// EventFindingRun is: the refusals that make the record worth reading are on the
// verb, and an entry a client could hand over would be a filing claimed against
// a finding the writer may not be able to see, with nothing on the row to match
// it.
const EventFindingUpstream = "finding.upstream"

// FindingUpstreamRoom is where an entry lands when the finding it is about names
// no room of its own - findingRunRoom's rule, and statusRoom's before that: an
// entry nobody can find in a room is an entry nobody reads.
const FindingUpstreamRoom = "findings"

// UpstreamRef is one thing over there this finding touches: whose tracker, an
// issue or a pull request, their number and the link.
//
// It says NOTHING about who put it there or whether this finding was filed. That
// is upstream_state's job and only upstream_state's - a reference read as a
// filing is the mistake that turned 1 into 8.
type UpstreamRef struct {
	Tracker string `json:"tracker"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	URL     string `json:"url,omitempty"`
}

// Same reports whether two references name the same thing over there. The URL is
// not part of it: the same issue is written as a bare number in one corpus line
// and as a full link in the next, and two rows for one issue is what dedup
// exists to stop.
func (r UpstreamRef) Same(other UpstreamRef) bool {
	return r.Tracker == other.Tracker && r.Kind == other.Kind && r.ID == other.ID
}

// String is how a reference is named in a sentence - "ragflow pr #16958" - so a
// refusal says which one without the reader going back for the row.
func (r UpstreamRef) String() string {
	switch {
	case r.Tracker == "" && r.ID == "":
		return "nothing"
	case r.ID == "":
		return r.Tracker
	case r.Kind == UpstreamKindPR:
		return strings.TrimSpace(r.Tracker + " pr #" + r.ID)
	}
	return strings.TrimSpace(r.Tracker + " #" + r.ID)
}

// UpstreamFiling is where a finding stands on somebody else's tracker and what
// it cites over there. It is what a caller states to SetFindingUpstream and what
// FindingUpstreamOf reads back off the row, in the keys named above, so the
// shape a reader parses and the shape an importer writes are one shape.
//
// State is NOT omitempty: an answer that left it out would leave a client
// deciding whether it means unfiled or means the node did not say, which is the
// two-words-for-one-state problem this file is entirely about.
type UpstreamFiling struct {
	Tracker string `json:"upstream_tracker,omitempty"`
	Kind    string `json:"upstream_kind,omitempty"`
	ID      string `json:"upstream_id,omitempty"`
	URL     string `json:"upstream_url,omitempty"`
	State   string `json:"upstream_state"`
	FiledAt string `json:"filed_at,omitempty"`
	FiledBy string `json:"filed_by,omitempty"`
	// Refs is everything this finding touches upstream, including the filing's
	// own reference when there is one. Nil in a call means "leave the citations
	// alone"; a stated list replaces them whole, findingrepro.go's manifest
	// rule - a citation list is a fact about what the finding cites NOW.
	Refs []UpstreamRef `json:"upstream_refs,omitempty"`
}

// Filed reports whether this filing STANDS - we sent it, it is live over there,
// and nobody has taken it back. Referenced is deliberately not in this set: a
// number somebody wrote down is not a submission. Rejected and withdrawn are
// filings that happened and no longer stand, which is why they are out too, and
// why they are the two states a re-file is allowed from.
func (f UpstreamFiling) Filed() bool {
	switch f.State {
	case UpstreamFiled, UpstreamAccepted, UpstreamFixed:
		return true
	}
	return false
}

// Ref is the reference the filing is OF, empty when nothing has been filed.
func (f UpstreamFiling) Ref() UpstreamRef {
	return UpstreamRef{Tracker: f.Tracker, Kind: f.Kind, ID: f.ID, URL: f.URL}
}

// upstreamRefusalError is what every refusal this verb makes ABOUT THE FILING IT
// WAS ASKED TO RECORD satisfies: the caller's mistake, and fixable by the
// caller. It is statusRefusalError's interface rather than a fourth one, so a
// refusal added here cannot be one HTTP maps to 400 and MCP reports as a broken
// node.
type upstreamRefusalError struct{ reason string }

func (e upstreamRefusalError) Error() string { return e.reason }
func (e upstreamRefusalError) depRefusal()   {}

func refuseUpstream(format string, a ...any) error {
	return upstreamRefusalError{reason: fmt.Sprintf(format, a...)}
}

// NormalizeUpstreamState validates the state a write asks for and returns it as
// the node stores it.
//
// Case and surrounding space are the caller's typing rather than a different
// state, so they are taken off - NormalizeTodoStatus's rule, for the reason
// written there. An EMPTY state is unfiled rather than a refusal, which is where
// this differs from the queue's: not filing something is the ordinary condition
// of a finding, so the absence of the fact is the fact.
//
// Anything outside the vocabulary is refused rather than stored. The refusal is
// at the WRITE, CheckIsolation's rule: a word no reader knows would sit on the
// row until somebody counting "what have we sent upstream" quietly counted it
// wrong.
func NormalizeUpstreamState(asked string) (string, error) {
	state := strings.ToLower(strings.TrimSpace(asked))
	if state == "" {
		return UpstreamUnfiled, nil
	}
	for _, known := range UpstreamStates {
		if state == known {
			return state, nil
		}
	}
	return "", refuseUpstream("%q is not a state a filing has: one of %s. %q is numbers "+
		"we cite and nobody sent, which is what most of them are - see "+
		"internal/store/findingupstream.go",
		asked, strings.Join(UpstreamStates, ", "), UpstreamReferenced)
}

// NormalizeUpstreamKind validates what a reference IS. Empty is refused rather
// than defaulted: guessing issue would record "we told them" about a row that
// carries a pull request, which is the claim the corpus lost and the reason this
// is a closed set.
func NormalizeUpstreamKind(asked string) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(asked))
	for _, known := range UpstreamKinds {
		if kind == known {
			return kind, nil
		}
	}
	return "", refuseUpstream("%q is not what a reference is: %s. Told-them and sent-a-fix "+
		"are different claims and neither can be recovered from a bare number later",
		asked, strings.Join(UpstreamKinds, " or "))
}

// FindingUpstreamOf is where a finding stands upstream, read off the row and
// nothing else.
//
// A row carrying none of the keys is UNFILED, and so is one whose fields do not
// parse - artifactField's rule: a row that says nothing about these keys is a
// row nobody has filed. That default is what makes the common case free, and it
// is why the 22 unfiled SereneDB findings need no second write at import time.
//
// A row that carries REFERENCES BUT NO STATE WORD reads as referenced, never as
// filed. Inferring a filing from the presence of a number is precisely the
// mistake the import's dry run made on 7 rows, and a reader that guesses it is
// the same mistake one layer along.
//
// The state comes back AS STORED rather than re-validated. The write door is the
// gate (NormalizeUpstreamState), and a word that arrived from a peer whose
// vocabulary is wider than this node's is a fact to show a reader, not one to
// silently rename.
func FindingUpstreamOf(a *Artifact) UpstreamFiling {
	filing := UpstreamFiling{State: UpstreamUnfiled}
	if a == nil || len(a.Fields) == 0 {
		return filing
	}
	var raw struct {
		Tracker string        `json:"upstream_tracker"`
		Kind    string        `json:"upstream_kind"`
		ID      string        `json:"upstream_id"`
		URL     string        `json:"upstream_url"`
		State   string        `json:"upstream_state"`
		FiledAt string        `json:"filed_at"`
		FiledBy string        `json:"filed_by"`
		Refs    []UpstreamRef `json:"upstream_refs"`
	}
	if err := json.Unmarshal(a.Fields, &raw); err != nil {
		return filing
	}
	filing.Tracker = strings.TrimSpace(raw.Tracker)
	filing.Kind = strings.TrimSpace(raw.Kind)
	filing.ID = strings.TrimSpace(raw.ID)
	filing.URL = strings.TrimSpace(raw.URL)
	filing.FiledAt = strings.TrimSpace(raw.FiledAt)
	filing.FiledBy = strings.TrimSpace(raw.FiledBy)
	filing.Refs = raw.Refs
	switch state := strings.ToLower(strings.TrimSpace(raw.State)); {
	case state != "":
		filing.State = state
	case len(raw.Refs) > 0 || filing.ID != "":
		filing.State = UpstreamReferenced
	}
	return filing
}

// upstreamDateFormats are what a filed_at may be written as. RFC3339 is what
// this node writes; the date alone is what a corpus filed by hand usually
// carries, because "2026-03-14" is what somebody typed in a markdown table and
// there is no hour to recover. Both are stored as RFC3339 in UTC, so a reader
// parses one format and a sort is a string sort.
var upstreamDateFormats = []string{time.RFC3339, "2006-01-02"}

// normalizeFiledAt takes what the caller stated and returns what the row
// carries. A date nothing can parse is REFUSED rather than stored: a filed_at
// that is free text is a column no console can sort and no importer can be
// checked against, which is a second value carrying a fact nobody can read.
func normalizeFiledAt(asked string) (string, error) {
	at := strings.TrimSpace(asked)
	if at == "" {
		return "", nil
	}
	for _, layout := range upstreamDateFormats {
		if t, err := time.Parse(layout, at); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", refuseUpstream("filed_at %q is not a time: state it as 2006-01-02 or as "+
		"a full RFC3339 timestamp, or leave it out and the node stamps now", asked)
}

// normalizeUpstreamRefs validates and dedups a citation list.
//
// A reference names a tracker, a kind and a number. A bare number is refused for
// the reason the whole file exists - a value that does not carry which fact it
// is about is not one - and the corpus is full of them ("#17236", "PR #16959"),
// which is the importer's problem to resolve at parse time and not a reader's
// forever.
//
// Duplicates are folded rather than refused, first mention winning, because one
// corpus line naturally writes the same PR twice in two spellings and a caller
// restating a list it just read should not have to diff it.
func normalizeUpstreamRefs(refs []UpstreamRef) ([]UpstreamRef, error) {
	out := make([]UpstreamRef, 0, len(refs))
	for _, ref := range refs {
		kind, err := NormalizeUpstreamKind(ref.Kind)
		if err != nil {
			return nil, err
		}
		clean := UpstreamRef{
			Tracker: strings.TrimSpace(ref.Tracker),
			Kind:    kind,
			ID:      strings.TrimSpace(ref.ID),
			URL:     strings.TrimSpace(ref.URL),
		}
		if clean.Tracker == "" || clean.ID == "" {
			return nil, refuseUpstream("an upstream reference names whose tracker it is in "+
				"and their number for it: %+v says neither, and a bare number is a value "+
				"nobody can follow", ref)
		}
		duplicate := false
		for i, seen := range out {
			if seen.Same(clean) {
				duplicate = true
				// A second mention with a link is worth keeping: the corpus
				// writes one reference as a bare number and the next as a URL.
				if out[i].URL == "" {
					out[i].URL = clean.URL
				}
				break
			}
		}
		if !duplicate {
			out = append(out, clean)
		}
	}
	return out, nil
}

// SetFindingUpstream records where a finding stands on somebody else's tracker
// and what it cites over there: the filing on the row and the entry in the log,
// in one write.
//
// AN UPDATE STATES WHAT CHANGES. Tracker, kind, id and url left empty are
// inherited from the filing already on the row, which is findingWrite's rule and
// is what makes the ordinary calls short: {state: accepted} advances the
// standing filing without restating its number, and {state: withdrawn} takes it
// back without erasing which issue it was. Refs left NIL leaves the citations
// alone; a stated list replaces them whole, which is the repro manifest's rule -
// what a finding cites is a fact stated fresh, not an append-only pile.
//
// THE CALLER MAY STATE filed_at AND filed_by. This is the one place a stamp is
// not taken from the node, and deliberately: these findings were filed by a
// person, by hand, months before this row existed, and the import rule for the
// corpus is that the original author and date ride as DATA rather than being
// overwritten by whoever ran the importer (01M098HJNZCV67J07BCX8DEVTE). Left
// out, they are the node's clock and this principal's seat - which is the right
// answer for a filing being recorded as it happens.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and a filing
//     nobody recorded is not one.
//   - a state outside the vocabulary, or a reference that is not an issue or a
//     pull request, or one with no tracker or no number.
//   - an id that does not name a finding this principal may READ - readFinding's
//     answer, the same one WriteFindingRepro asks first.
//   - a finding with no project, for RecordFindingRun's reason: a projectless
//     event is read back by its actor alone, so the entry behind the filing
//     would be invisible to everyone else who can read the finding.
//   - a filed state with no tracker or no number. A filing that cannot say WHICH
//     tracker and WHICH issue is a status word again, which is the whole defect
//     this file closes.
//   - a filing reference stated under unfiled or referenced. Neither says we
//     sent anything, so neither may name the thing we sent; citations go in refs.
//   - referenced with no references at all, and references under unfiled. The
//     word and the list have to agree or the row says two things.
//   - unfiled stated over a filing that exists. Unfiled means nobody ever sent
//     it; saying it about an issue that is live over there erases the number and
//     invites somebody to file it a second time. Withdrawn is the word. Over a
//     merely REFERENCED row it is allowed, because dropping a citation orphans
//     nothing over there.
//   - a different filing while the first one stands - see the head of this file,
//     and ErrAlreadyFiled in forge.go, whose reasoning this is.
//
// It does NOT refuse a restatement: saying a filing is still filed is somebody
// confirming it, the fold is latest-wins, and it costs a reader nothing -
// SetTodoStatus's rule for the other axis.
func (d *DB) SetFindingUpstream(
	ctx context.Context, p *Principal, findingID string, asked UpstreamFiling,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, EventFindingUpstream)
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter, and p.UserID rides the meta beside it so a
	// reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseUpstream("this token resolves to nobody, so it cannot say " +
			"where a finding stands upstream")
	}
	state, err := NormalizeUpstreamState(asked.State)
	if err != nil {
		return nil, nil, err
	}
	filedAt, err := normalizeFiledAt(asked.FiledAt)
	if err != nil {
		return nil, nil, err
	}
	statedRefs, err := normalizeUpstreamRefs(asked.Refs)
	if err != nil {
		return nil, nil, err
	}
	finding, err := d.readFinding(ctx, p, strings.TrimSpace(findingID))
	if err != nil {
		return nil, nil, err
	}
	if finding.Project == nil || *finding.Project == "" {
		return nil, nil, refuseUpstream("finding %s has no project and is its owner's alone, so "+
			"the entry behind a filing on it would be readable by whoever recorded it rather "+
			"than by whoever can read the finding - write it at scope=project or scope=shared "+
			"first", finding.ID)
	}

	stood := FindingUpstreamOf(finding)
	next := UpstreamFiling{
		State:   state,
		Tracker: strings.TrimSpace(asked.Tracker),
		Kind:    strings.TrimSpace(asked.Kind),
		ID:      strings.TrimSpace(asked.ID),
		URL:     strings.TrimSpace(asked.URL),
		FiledAt: filedAt,
		FiledBy: strings.TrimSpace(asked.FiledBy),
		Refs:    stood.Refs,
	}
	if asked.Refs != nil {
		next.Refs = statedRefs
	}

	switch {
	case state == UpstreamUnfiled || state == UpstreamReferenced:
		// Neither word claims we sent anything, so neither may name what we
		// sent. This is the rule that keeps a citation from becoming a filing by
		// sitting in the wrong key - the 8-for-1 defect, refused at the door.
		if next.Tracker != "" || next.ID != "" || next.Kind != "" || next.URL != "" {
			return nil, nil, refuseUpstream("finding %s: %q does not claim anybody filed it, so "+
				"it cannot name the issue we filed. Put the number in refs, where a citation "+
				"asserts nothing, or say %q if we did send it",
				finding.ID, state, UpstreamFiled)
		}
		if stood.Filed() || stood.State == UpstreamRejected || stood.State == UpstreamWithdrawn {
			return nil, nil, refuseUpstream("finding %s is filed as %s and calling it %q would "+
				"erase that number, after which somebody files it there a second time. Use %q "+
				"for a filing we took back, or %q for one they turned down",
				finding.ID, stood.Ref(), state, UpstreamWithdrawn, UpstreamRejected)
		}
		if state == UpstreamUnfiled && len(next.Refs) > 0 {
			return nil, nil, refuseUpstream("finding %s: %q means nobody sent it and it cites "+
				"nothing, but %d reference(s) are on it. Say %q, which is numbers we cite with "+
				"nobody claiming we filed them, or state refs as an empty list to drop them",
				finding.ID, UpstreamUnfiled, len(next.Refs), UpstreamReferenced)
		}
		if state == UpstreamReferenced && len(next.Refs) == 0 {
			return nil, nil, refuseUpstream("finding %s: %q with no references is %q written "+
				"the long way - state what it cites, or say %q",
				finding.ID, UpstreamReferenced, UpstreamUnfiled, UpstreamUnfiled)
		}
		next.FiledAt, next.FiledBy = "", ""
	default:
		if next.Tracker == "" {
			next.Tracker = stood.Tracker
		}
		if next.ID == "" {
			next.ID = stood.ID
		}
		if next.Kind == "" {
			next.Kind = stood.Kind
		}
		if next.URL == "" {
			next.URL = stood.URL
		}
		if next.Tracker == "" || next.ID == "" {
			return nil, nil, refuseUpstream("finding %s: a filing in state %q names which "+
				"tracker it is in and which number they gave it - without those it is a "+
				"status word, and the number is the only part of it a reader can act on",
				finding.ID, state)
		}
		if next.Kind, err = NormalizeUpstreamKind(next.Kind); err != nil {
			return nil, nil, fmt.Errorf("finding %s: %w", finding.ID, err)
		}
		// The second filing. A different one while the first still stands would
		// leave that issue live upstream with nothing pointing at it, so it is
		// refused here and allowed once the standing filing does not stand - the
		// log and the citation list keep both either way.
		if stood.Filed() && !stood.Ref().Same(next.Ref()) {
			return nil, nil, refuseUpstream("finding %s is already filed as %s and that filing "+
				"stands, so it cannot also be %s: the first would be live over there with "+
				"nothing here pointing at it. Record %q or %q on the first one and then file "+
				"it again",
				finding.ID, stood.Ref(), next.Ref(), UpstreamRejected, UpstreamWithdrawn)
		}
		if next.FiledAt == "" {
			// The filing that continues keeps the day it was made; a new one is
			// stamped now. A state move is not a re-filing and must not look
			// like one on the row.
			if stood.Ref().Same(next.Ref()) && stood.FiledAt != "" {
				next.FiledAt = stood.FiledAt
			} else {
				next.FiledAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
		if next.FiledBy == "" {
			if stood.Ref().Same(next.Ref()) && stood.FiledBy != "" {
				next.FiledBy = stood.FiledBy
			} else {
				next.FiledBy = actor
			}
		}
		// What we filed is also something this finding touches, so it is in the
		// citation list without the caller having to say it twice. That is what
		// makes "which findings are in #16958" one query over one key rather
		// than a union of two.
		next.Refs = withRef(next.Refs, next.Ref())
	}

	fields, err := ArtifactFields(finding)
	if err != nil {
		return nil, nil, err
	}
	setUpstreamFields(fields, next)
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: record finding %s upstream filing: %w", finding.ID, err)
	}

	entry, err := upstreamEntryEvent(finding, p, actor, actorKind, stood, next)
	if err != nil {
		return nil, nil, err
	}
	// One transaction, one clock reading, both rows or neither: a filing with no
	// entry behind it is a number nobody can trace to whoever recorded it, and an
	// entry with no filing behind it is a log that lies.
	if err := d.SetArtifactFields(ctx, finding, column, entry); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(finding.ID)
	return finding, entry, nil
}

// withRef puts a reference in the list if the list does not already name it,
// keeping the order the caller stated and taking the link when the list had none.
func withRef(refs []UpstreamRef, ref UpstreamRef) []UpstreamRef {
	for i, seen := range refs {
		if seen.Same(ref) {
			if refs[i].URL == "" {
				refs[i].URL = ref.URL
			}
			return refs
		}
	}
	return append(refs, ref)
}

// setUpstreamFields writes the filing into an artifact's fields map, DELETING
// the keys it has nothing to say about rather than writing empty strings or an
// empty list.
//
// An empty string and an absent key would be two spellings of "not filed", and
// FindingUpstreamOf would have to treat them alike forever. Deleting keeps one
// spelling: a finding nobody sent and nothing cites carries no upstream keys at
// all, which is also what every row written before this file looks like.
func setUpstreamFields(fields map[string]any, f UpstreamFiling) {
	for key, value := range map[string]string{
		UpstreamStateField:   f.State,
		UpstreamTrackerField: f.Tracker,
		UpstreamKindField:    f.Kind,
		UpstreamIDField:      f.ID,
		UpstreamURLField:     f.URL,
		UpstreamFiledAtField: f.FiledAt,
		UpstreamFiledByField: f.FiledBy,
	} {
		if value == "" {
			delete(fields, key)
			continue
		}
		fields[key] = value
	}
	if len(f.Refs) == 0 {
		delete(fields, UpstreamRefsField)
		return
	}
	fields[UpstreamRefsField] = f.Refs
}

// UpstreamEntry is one entry in the log behind a finding's filing: where it
// went, where it came from, WHICH ISSUE, who said so and when.
//
// It carries the previous reference as well as the new one, which is what makes
// a re-file readable: "rejected as serenedb #12, filed as serenedb #31" is two
// numbers and one story, and an entry that named only the current one would
// leave a reader unable to tell a re-file from a state move.
type UpstreamEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Finding string `json:"finding"`
	// State and From are not omitempty, for StatusEntry's reason: an entry that
	// left one out would leave a client deciding whether it means unfiled or
	// means the node did not say.
	State       string        `json:"state"`
	From        string        `json:"from"`
	Tracker     string        `json:"upstream_tracker,omitempty"`
	Kind        string        `json:"upstream_kind,omitempty"`
	UpstreamID  string        `json:"upstream_id,omitempty"`
	URL         string        `json:"upstream_url,omitempty"`
	FromTracker string        `json:"from_tracker,omitempty"`
	FromID      string        `json:"from_id,omitempty"`
	FiledAt     string        `json:"filed_at,omitempty"`
	FiledBy     string        `json:"filed_by,omitempty"`
	Refs        []UpstreamRef `json:"upstream_refs,omitempty"`
	Actor       string        `json:"actor"`
	ActorKind   string        `json:"actor_kind,omitempty"`
	ActorUser   string        `json:"actor_user,omitempty"`
	SeqHLC      int64         `json:"seq_hlc"`
	Node        string        `json:"node"`
	Created     string        `json:"created"`
}

// upstreamEntryEvent builds the entry a filing is. The meta is one JSON object
// rather than a map of strings because the citation list rides in it: the log is
// where "these three findings went in one PR" is readable after somebody
// restates the list.
func upstreamEntryEvent(
	finding *Artifact, p *Principal, actor, actorKind string, from, to UpstreamFiling,
) (*Event, error) {
	meta, err := json.Marshal(map[string]any{
		"state":              to.State,
		"from":               from.State,
		UpstreamTrackerField: to.Tracker,
		UpstreamKindField:    to.Kind,
		UpstreamIDField:      to.ID,
		UpstreamURLField:     to.URL,
		UpstreamRefsField:    to.Refs,
		"from_tracker":       from.Tracker,
		"from_id":            from.ID,
		UpstreamFiledAtField: to.FiledAt,
		UpstreamFiledByField: to.FiledBy,
		"actor_kind":         actorKind,
		"actor_user":         p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: upstream filing of %s: %w", finding.ID, err)
	}
	return &Event{
		Type:    EventFindingUpstream,
		Project: finding.Project,
		Room:    upstreamRoom(finding),
		Thread:  finding.ID,
		// The finding itself, which is what decides who reads the entry: the
		// people who can read the work are the people its filing is about.
		Artifact: finding.ID,
		Actor:    actor,
		Body:     upstreamBody(from, to),
		Meta:     meta,
	}, nil
}

// UpstreamEntryOf renders one event as the entry it is.
func UpstreamEntryOf(e *Event) UpstreamEntry {
	entry := UpstreamEntry{
		ID: e.ID, Type: e.Type, Finding: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta struct {
		State       string        `json:"state"`
		From        string        `json:"from"`
		Tracker     string        `json:"upstream_tracker"`
		Kind        string        `json:"upstream_kind"`
		UpstreamID  string        `json:"upstream_id"`
		URL         string        `json:"upstream_url"`
		Refs        []UpstreamRef `json:"upstream_refs"`
		FromTracker string        `json:"from_tracker"`
		FromID      string        `json:"from_id"`
		FiledAt     string        `json:"filed_at"`
		FiledBy     string        `json:"filed_by"`
		ActorKind   string        `json:"actor_kind"`
		ActorUser   string        `json:"actor_user"`
	}
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.State, entry.From = meta.State, meta.From
		entry.Tracker, entry.Kind, entry.UpstreamID = meta.Tracker, meta.Kind, meta.UpstreamID
		entry.URL, entry.Refs = meta.URL, meta.Refs
		entry.FromTracker, entry.FromID = meta.FromTracker, meta.FromID
		entry.FiledAt, entry.FiledBy = meta.FiledAt, meta.FiledBy
		entry.ActorKind, entry.ActorUser = meta.ActorKind, meta.ActorUser
	}
	return entry
}

// LatestFindingUpstream folds a finding's entries into the filing that stands:
// the last one wins, over the order the log is read in. nil when there are none,
// which is a finding nobody has filed THROUGH THIS VERB - it may still carry a
// filing written on a peer whose entries did not travel, which is
// FindingUpstreamOf's business and not this fold's.
//
// entries must be in log order, which is what findingUpstreamEvents returns.
func LatestFindingUpstream(entries []UpstreamEntry) *UpstreamFiling {
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &UpstreamFiling{
		State: last.State, Tracker: last.Tracker, Kind: last.Kind, ID: last.UpstreamID,
		URL: last.URL, FiledAt: last.FiledAt, FiledBy: last.FiledBy, Refs: last.Refs,
	}
}

// FindingUpstreamLog is every filing entry naming this finding that p may read,
// oldest first - so a reader sees a finding cited, filed, turned down and filed
// somewhere else, rather than only where it ended up. It is FindingRuns for the
// other fact a finding carries about the world outside this node, with the same
// permission story: the filter is in the WHERE clause and it is not a second
// rule.
func (d *DB) FindingUpstreamLog(
	ctx context.Context, p *Principal, findingID string,
) ([]UpstreamEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "finding.upstream.log")
	defer span.End()

	events, err := d.findingUpstreamEvents(ctx, p, []string{findingID})
	if err != nil {
		return nil, err
	}
	out := make([]UpstreamEntry, 0, len(events))
	for _, e := range events {
		out = append(out, UpstreamEntryOf(e))
	}
	return out, nil
}

// findingUpstreamEvents reads the entries naming any of findings, in log order,
// through the same event filter every other read of a log uses.
//
// There is no LIMIT, for depEvents' reason: the fold is over the WHOLE log for
// each finding, and a page that stopped early would fold a prefix - an answer
// that is not the filing that stands.
func (d *DB) findingUpstreamEvents(
	ctx context.Context, p *Principal, findings []string,
) ([]*Event, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "upstream events", func(a *args) string {
		idsArg := a.next(pq.Array(findings))
		typeArg := a.next(EventFindingUpstream)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// upstreamRoom is where an entry lands in the log: the room the finding was
// raised in, or the findings room when it names none - findingRunRoom's rule,
// and it exists for the reason that one does.
func upstreamRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return FindingUpstreamRoom
}

// upstreamBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
//
// It names both ends the way statusBody does, and it names the ISSUE at each
// end, because "filed as serenedb #12" and "rejected as serenedb #12, filed as
// serenedb #31" are different facts and only the pair says which happened. A
// referenced row says how many it cites, because that is the whole of what it
// claims.
func upstreamBody(from, to UpstreamFiling) string {
	return upstreamSide(from) + "->" + upstreamSide(to)
}

func upstreamSide(f UpstreamFiling) string {
	if ref := f.Ref(); ref.ID != "" {
		return f.State + " as " + ref.String()
	}
	if f.State == UpstreamReferenced {
		return fmt.Sprintf("%s (%d cited)", f.State, len(f.Refs))
	}
	return f.State
}
