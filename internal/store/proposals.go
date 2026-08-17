package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// Proposals, and the votes on them.
//
// A proposal is an artifact of type 'proposal', for the reason an announcement
// is one and a report is one: one table, one permission filter, one signature,
// one merge. It is not a permission axis - a proposal is readable by exactly
// whoever could read the room's artifacts before it was written - and the room
// it belongs to rides fields under RoomField, the way a todo's does.
//
// A vote is an event, and that is the whole design rather than an
// implementation detail. The surface exists because agreement was being
// reconstructed by reading a chat log back: somebody proposes, others reply,
// and whether a thing was settled is inferred from prose two hours later. What
// makes the record honest is that changing your mind appends:
//
//   - a column would answer "what does everyone think now" and destroy "who
//     agreed to this, and when" - which is the question that gets asked, months
//     later, by somebody who was not here.
//   - the log is append-only already, it carries the actor the token resolves
//     to rather than one anybody can type, and it is permission-filtered by the
//     artifact it names - see EventFilterSQL's floor clause. So a vote inherits
//     the proposal's readers with nothing written to say so.
//   - the tally is therefore a reading of the log rather than a stored number:
//     the latest vote per principal counts, and every earlier one is still
//     there to be read.
//
// Closing is deliberately manual. There is no quorum rule and no timer: a rule
// that decided for people would be a governance system nobody agreed to, and
// the point of this is to record agreement rather than to manufacture it.
// Whoever may write the proposal - its owner, which is the bar every other
// artifact update here keeps - closes it with an outcome, and the closure is on
// the row and in the log.

// ProposalType is the artifact type.
const ProposalType = "proposal"

// The two states a proposal is in. They are the artifact's status, so a list
// narrows by the column every other list narrows by.
const (
	ProposalOpen   = "open"
	ProposalClosed = "closed"
)

// The choices. Abstain is one of them on purpose: "I have read this and I am
// not standing in the way" is a different answer from silence, and a record
// that cannot tell them apart makes an absent principal look like a party to
// the decision.
const (
	VoteFor     = "for"
	VoteAgainst = "against"
	VoteAbstain = "abstain"
)

// VoteChoices is the closed set, in the order a tally reads.
var VoteChoices = []string{VoteFor, VoteAgainst, VoteAbstain}

// VoteChoiceOK reports whether choice is one of them. It is a closed set for
// the reason every other one here is: a choice nothing checked is a value that
// is stored, signed and replicated as if this node had agreed to count it.
func VoteChoiceOK(choice string) bool {
	for _, c := range VoteChoices {
		if choice == c {
			return true
		}
	}
	return false
}

// The two entries a proposal leaves in the log. Both are minted types - see
// mintedEventTypes - so the only way to get one is to have done the thing. A
// vote a client could write by hand through POST /api/events would be a vote
// cast on a proposal that closed an hour ago, counted, with the refusal below
// walked straight past.
const (
	EventProposalVote  = "proposal.vote"
	EventProposalClose = "proposal.close"
)

// ProposalRoom is where the two entries land when the proposal names no room of
// its own, so that a vote is somewhere a reader can find it rather than in the
// roomless part of the log.
const ProposalRoom = "proposals"

// The keys a closure rides in the artifact's fields, beside RoomField. They are
// in fields rather than in columns for the reason as_of and supersedes are on a
// report: this is what the row is *about*, not who may see it.
const (
	OutcomeField  = "outcome"
	ClosedAtField = "closed_at"
)

// Vote is one recorded vote as a reader gets it back.
type Vote struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	Choice    string `json:"choice"`
	Reason    string `json:"reason,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// Tally is what the votes add up to.
//
// Voters and Votes are both here, and the pair is the point: one principal
// counts once however many times they changed their mind, and the number of
// entries behind that is a fact a reader can see rather than one the tally
// hides.
type Tally struct {
	For     int `json:"for"`
	Against int `json:"against"`
	Abstain int `json:"abstain"`
	Voters  int `json:"voters"`
	Votes   int `json:"votes"`
}

// ProposalClosedError is what a vote on a closed proposal gets, and it says
// when the proposal closed. A refusal that only says "closed" leaves the caller
// to go and find out whether it closed before or after they made up their mind,
// which is the argument this surface exists to stop having.
type ProposalClosedError struct {
	ID      string
	At      string
	Outcome string
}

func (e ProposalClosedError) Error() string {
	outcome := "no outcome was recorded"
	if e.Outcome != "" {
		outcome = "the outcome was: " + e.Outcome
	}
	return "proposal " + e.ID + " closed at " + e.At + " and " + outcome +
		"; a closed proposal takes no more votes"
}

// ProposalClosure reads a proposal's closure off its fields: the moment it
// closed and the outcome recorded with it, both empty while it is open.
//
// The status column says the same thing, and this is the value that decides.
// A row whose status somebody edited to 'closed' with no closed_at has no
// moment to name in the refusal, and a refusal that cannot say when is the one
// this surface must not produce - so the closure is the timestamped fact, and
// the column is what a list narrows by.
func ProposalClosure(a *Artifact) (at, outcome string) {
	if a == nil || len(a.Fields) == 0 {
		return "", ""
	}
	var fields map[string]any
	if err := json.Unmarshal(a.Fields, &fields); err != nil {
		return "", ""
	}
	at, _ = fields[ClosedAtField].(string)
	outcome, _ = fields[OutcomeField].(string)
	return at, outcome
}

// ProposalRoomOf is the room a proposal belongs to, or "" for one that names
// none. It is RoomField and nothing else - the same key a todo carries.
func ProposalRoomOf(a *Artifact) string {
	if a == nil || len(a.Fields) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(a.Fields, &fields); err != nil {
		return ""
	}
	room, _ := fields[RoomField].(string)
	return room
}

// voteActor is the actor a principal writes a vote as: the agent when the token
// names one, the person behind it otherwise.
//
// It is chatActor's rule, in the package that cannot import the server's. Each
// seat is its own voice: four agents and a person in a room are five parties to
// a decision, and folding an agent's vote into its user's would make one of
// them speak twice or not at all.
func voteActor(p *Principal) (actor, kind string) {
	if p == nil {
		return "", ""
	}
	if p.AgentID != "" {
		return p.AgentID, "agent"
	}
	return p.UserID, "user"
}

// ReadProposal is a permission-filtered read of one proposal. An id that names
// something that is not a proposal comes back as ErrNotFound, exactly like one
// that is not here: this surface has one namespace and is not a way to find out
// what else an id might be.
func (d *DB) ReadProposal(ctx context.Context, p *Principal, id string, scopeAll bool) (*Artifact, error) {
	art, err := d.ReadArtifact(ctx, p, id, scopeAll)
	if err != nil {
		return nil, err
	}
	if art.Type != ProposalType {
		return nil, ErrNotFound
	}
	return art, nil
}

// CastVote records one principal's vote on one proposal.
//
// It appends and never updates, which is the whole of what makes the history
// worth keeping: a principal who votes again has two entries in the log and one
// vote in the tally. There is no id to hand back in and nothing to overwrite.
//
// The three refusals, in the order they are asked:
//
//   - a choice that is not one of the three. A vote nothing can count is not a
//     vote, and defaulting it would put a word in somebody's mouth.
//   - a proposal the principal cannot read, which is refused as an id that is
//     not here - the answer a read of it would give. Voting is not a way to
//     find out that something exists, and a vote from somebody who cannot read
//     what they are voting on is not consent to anything.
//   - a proposal that has closed, naming the moment it closed.
//
// The event lands in the proposal's project rather than the voter's, and that
// is deliberate: a vote is about the proposal, and one filed in the voter's
// project would be invisible to the project holding the proposal - so the tally
// would come out differently for each reader, which is the one thing a record
// of who agreed cannot do.
func (d *DB) CastVote(ctx context.Context, p *Principal, id, choice, reason string) (*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "proposal.vote")
	defer span.End()

	if !VoteChoiceOK(choice) {
		return nil, fmt.Errorf("store: a vote is one of %s, not %q",
			strings.Join(VoteChoices, ", "), choice)
	}
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, errors.New("store: this token resolves to nobody, so it cannot vote")
	}

	art, err := d.ReadProposal(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if at, outcome := ProposalClosure(art); at != "" {
		return nil, ProposalClosedError{ID: art.ID, At: at, Outcome: outcome}
	}

	reason = strings.TrimSpace(reason)
	meta, err := json.Marshal(map[string]string{
		"choice": choice, "reason": reason,
		"actor_kind": actorKind, "actor_user": p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: vote on %s: %w", art.ID, err)
	}

	e := &Event{
		Type:     EventProposalVote,
		Project:  art.Project,
		Room:     proposalRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     voteBody(choice, reason),
		Meta:     meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	span.SetArtifact(art.ID)
	return e, nil
}

// CloseProposal closes a proposal with an outcome, and says so in the log.
//
// Manual, and the outcome is required. Nothing here counts the votes and
// decides: the tally is in front of whoever closes it, and what they write down
// is what the room agreed the tally meant. An outcome nobody stated would leave
// a closed proposal whose record says only that somebody stopped it.
//
// The bar is ownership, which is the bar every other update of an artifact
// keeps here - see upsertArtifact, whose ON CONFLICT clause would refuse it
// anyway. It is asked here so that the refusal says whose the proposal is
// rather than coming back as an id that is not there.
//
// The row and the entry go in together, under one clock reading, for the reason
// a memory write does: a closure with nothing in the log behind it replicates
// on its own, and nothing here ever comes back to finish half an operation.
func (d *DB) CloseProposal(
	ctx context.Context, p *Principal, id, outcome string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "proposal.close")
	defer span.End()

	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		return nil, nil, errors.New("store: closing a proposal records an outcome: " +
			"what was decided, in a line")
	}
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, errors.New("store: this token resolves to nobody, so it cannot close a proposal")
	}

	art, err := d.ReadProposal(ctx, p, id, false)
	if err != nil {
		return nil, nil, err
	}
	if at, was := ProposalClosure(art); at != "" {
		// It closed once. Writing a second outcome over the first would rewrite
		// the record this surface exists to keep.
		return nil, nil, ProposalClosedError{ID: art.ID, At: at, Outcome: was}
	}
	if p.UserID == "" || art.OwnerUser != p.UserID {
		return nil, nil, fmt.Errorf("store: proposal %s belongs to somebody else, "+
			"and whoever may write it is who may close it", art.ID)
	}

	fields := map[string]any{}
	if len(art.Fields) > 0 {
		if err := json.Unmarshal(art.Fields, &fields); err != nil {
			return nil, nil, fmt.Errorf("store: proposal %s carries fields that do not parse: %w",
				art.ID, err)
		}
	}
	fields[OutcomeField] = outcome
	fields[ClosedAtField] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: close %s: %w", art.ID, err)
	}
	art.Fields, art.Status = raw, ProposalClosed

	meta, err := json.Marshal(map[string]string{
		"outcome": outcome, "actor_kind": actorKind, "actor_user": p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: close %s: %w", art.ID, err)
	}
	e := &Event{
		Type:   EventProposalClose,
		Room:   proposalRoom(art),
		Thread: art.ID,
		Actor:  actor,
		Body:   "closed the proposal: " + outcome,
		Meta:   meta,
	}
	if err := d.WriteMemory(ctx, art, e); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, e, nil
}

// ProposalVotes is every vote on a proposal that p may read, in log order -
// oldest first, so a reader sees somebody change their mind rather than only
// the mind they ended up with.
//
// The permission filter is in the WHERE clause like every other read here, and
// it is not a second rule: EventFilterSQL's floor is the artifact filter on the
// row the event names, so the votes reach exactly the principals the proposal
// itself reaches.
func (d *DB) ProposalVotes(ctx context.Context, p *Principal, id string) ([]Vote, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "proposal.votes")
	defer span.End()

	events, err := readPage(ctx, d, "proposal votes", func(a *args) string {
		idArg := a.next(id)
		typeArg := a.next(EventProposalVote)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE e.artifact = ` + idArg + ` AND e.type = ` + typeArg + `
	             AND ` + filter + `
	           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
	if err != nil {
		return nil, err
	}
	votes := make([]Vote, 0, len(events))
	for _, e := range events {
		votes = append(votes, VoteOf(e))
	}
	return votes, nil
}

// VoteOf renders one event as the vote it is.
//
// A choice that is not one of the three reads back as it was stored rather than
// being dropped: the row is signed, so what it says is what its author said,
// and a reader is better served by seeing a value this build does not
// understand than by a gap in the record with nothing to say there was one.
// TallyOf counts what it recognises, which is the other half of that.
func VoteOf(e *Event) Vote {
	v := Vote{
		ID: e.ID, Actor: e.Actor, SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		v.Choice, v.Reason = meta["choice"], meta["reason"]
		v.ActorKind, v.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return v
}

// TallyOf counts the votes: the latest one per principal, and nothing else.
//
// votes are in log order, so a later entry from the same actor replaces an
// earlier one - which is what "changing your mind appends" means on the reading
// side. Votes is how many entries there were, so the two numbers together say
// what the log holds without a reader having to fetch it to find out.
func TallyOf(votes []Vote) Tally {
	latest := make(map[string]string, len(votes))
	for _, v := range votes {
		if v.Actor == "" {
			continue
		}
		latest[v.Actor] = v.Choice
	}

	t := Tally{Votes: len(votes)}
	for _, choice := range latest {
		switch choice {
		case VoteFor:
			t.For++
		case VoteAgainst:
			t.Against++
		case VoteAbstain:
			t.Abstain++
		default:
			// A choice this build does not know is a voter and not a count: it
			// is still one principal who answered, and guessing which column it
			// belongs in would be inventing their answer.
		}
		t.Voters++
	}
	return t
}

// proposalRoom is where a proposal's entries go in the log.
func proposalRoom(a *Artifact) string {
	if room := ProposalRoomOf(a); room != "" {
		return room
	}
	return ProposalRoom
}

// voteBody is what the vote reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
func voteBody(choice, reason string) string {
	body := "voted " + choice
	if reason != "" {
		body += ": " + reason
	}
	return body
}
