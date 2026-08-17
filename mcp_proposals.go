package main

// The proposal tools. A proposal is an artifact of type 'proposal' living in a
// room, and a vote on it is an event - see internal/store/proposals.go, which
// is where the rules are and why they are those rules.
//
// The verbs mirror the memory and report tools, so an agent that has learned
// mem_* or report_* transfers with no brief: write, read, list, and the one
// verb that is not a noun. What is different is that there is no update-in-
// place of the record: a vote appends, and the only thing proposal_write does
// to a proposal that has votes on it is edit its text or close it.
//
// Closing rides proposal_write rather than a verb of its own, because closing
// is a write of the proposal and the bar is the same one: whoever may write it
// may close it. Naming an outcome is what closes it - a close with nothing
// recorded would say only that somebody stopped the thing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// maxProposalBody is the ceiling on a proposal body, in bytes. It is the
// report's ceiling for the report's reason: the body is what search reaches,
// so a corpus that outgrew the row would be a corpus search cannot see.
const maxProposalBody = 100_000

// maxVoteReason is the ceiling on why somebody voted the way they did. It is
// small deliberately: a reason is a line in a record, and an argument belongs
// in the room the proposal names, where people can answer it.
const maxVoteReason = 4_000

// proposalTools is the proposal surface, appended in allTools rather than
// written into the memory list, so each surface stays its own file - the rule
// the reports, worklog and observability tools follow.
var proposalTools = []tool{
	{
		Name: "proposal_write",
		Description: "Propose something to a room, or update or close a proposal by id. " +
			"A proposal is a decision waiting to be made: it records who assented, " +
			"who objected and when it closed, so that agreement lives in the store " +
			"rather than being reconstructed by reading the room back. Born open and " +
			"at scope=project. Naming an outcome closes it - nothing closes it for " +
			"you, and there is no quorum rule and no timer.",
		InputSchema: object(props{
			"title": str("One line, phrased as the thing being proposed."),
			"body":  str("What is being proposed and why, for somebody who was not in the room."),
			"room":  str("The chat room this belongs to, e.g. general. It puts the proposal in that room and narrows nothing else: who may read it is unchanged."),
			"scope": enum("Who may read it. Default project.", memScopes),
			"tags":  strArray("Free-form labels, searched with the title and the body."),
			"outcome": str("What was decided. Recording it closes the proposal - the vote " +
				"is in front of you, and this is what the room agreed the vote meant. " +
				"A closed proposal takes no more votes and cannot be closed again."),
			"id": str("Update or close the proposal with this id instead of creating one."),
		}, nil),
		call: proposalWrite,
	},
	{
		Name: "proposal_read",
		Description: "Read one proposal by id, with every vote on it in the order they " +
			"were cast and the tally of the latest vote per principal. A proposal you " +
			"may not read is reported exactly as one that does not exist.",
		InputSchema: object(props{"id": str("The proposal's id.")}, []string{"id"}),
		call:        proposalRead,
	},
	{
		Name: "vote",
		Description: "Vote on a proposal: for, against or abstain, with an optional reason. " +
			"Voting again records a new vote and keeps the old one - the latest counts " +
			"and the history stays readable, which is what makes \"who agreed to this, " +
			"and when\" answerable later. A proposal you cannot read, and one that has " +
			"closed, are both refused.",
		InputSchema: object(props{
			"proposal": str("The proposal's id."),
			"choice":   enum("for, against or abstain. Abstain is an answer: it says you have read this and are not standing in the way.", store.VoteChoices),
			"reason":   str("Why, in a line. Optional, and read by whoever asks about this decision months from now."),
		}, []string{"proposal", "choice"}),
		call: voteTool,
	},
	{
		Name: "proposal_list",
		Description: "List proposals you may read, newest first. Narrow to one room to " +
			"get that room's decisions, or to status=open for the ones still waiting " +
			"on somebody.",
		InputSchema: object(props{
			"room":   str("Only the proposals in this chat room."),
			"status": enum("Narrow to the open ones or the closed ones.", []string{store.ProposalOpen, store.ProposalClosed}),
			"scope":  enum("Narrow to one scope.", memScopes),
			"limit":  integer("Most proposals to return. Default 200."),
		}, nil),
		call: proposalList,
	},
}

// proposalWriteArgs is what proposal_write takes.
type proposalWriteArgs struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Room    string   `json:"room"`
	Scope   string   `json:"scope"`
	Tags    []string `json:"tags"`
	Outcome string   `json:"outcome"`
}

// proposalWrite creates a proposal, updates one, or closes it.
//
// The rules are reportWrite's rules verbatim, because they are the fabric's and
// not one surface's: an id that names something unreadable is refused rather
// than treated as a create, an artifact of another type is not turned into a
// proposal, and a principal writes in its own project or not at all.
//
// A close is answered first and on its own. It is the one write that a proposal
// can refuse outright, and a call that both edited the text and closed it would
// have to either write the text and then refuse - leaving a proposal saying
// something nobody voted on - or refuse before writing and leave the caller
// guessing which half happened.
func proposalWrite(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a proposalWriteArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot own a proposal")
	}
	if a.Outcome != "" && a.ID == "" {
		return nil, errors.New("an outcome closes a proposal, so it names one by id; " +
			"a proposal is born open, and one born closed is a decision nobody could vote on")
	}
	if a.Outcome != "" {
		// Closing is its own write. Taking an edit in the same call would mean
		// answering a refused close with the text written anyway - a proposal
		// saying something nobody voted on - and taking it after the close
		// would be an edit of a closed proposal that nobody asked for.
		if a.Title != "" || a.Body != "" || a.Room != "" || a.Scope != "" || a.Tags != nil {
			return nil, errors.New("closing a proposal records the outcome and nothing else: " +
				"edit the text first, then close it by id")
		}
		art, _, err := m.db.CloseProposal(ctx, p, a.ID, a.Outcome)
		if err != nil {
			return nil, proposalError(a.ID, err)
		}
		return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
	}

	// A proposal is read by the room it is proposed to, so it is born at the
	// project's scope rather than the personal default a memory item takes.
	scope, err := oneOf("scope", a.Scope, memScopes, "project")
	if err != nil {
		return nil, err
	}
	visibility := visibilityOf(scope)
	room, err := roomArg(a.Room)
	if err != nil {
		return nil, err
	}
	if len(a.Body) > maxProposalBody {
		return nil, fmt.Errorf("proposal body is %d bytes, over the %d ceiling - "+
			"propose the decision here and reference the detail as a report",
			len(a.Body), maxProposalBody)
	}

	art := &store.Artifact{
		ID:     a.ID,
		Type:   proposalType,
		Title:  strings.TrimSpace(a.Title),
		Body:   a.Body,
		Status: store.ProposalOpen,
		Tags:   a.Tags,
	}
	var fields map[string]any
	var home *string

	if a.ID != "" {
		old, err := m.db.ReadProposal(ctx, p, a.ID, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil, notAProposal(a.ID)
		}
		if err != nil {
			return nil, err
		}
		if old.OwnerUser != p.UserID {
			return nil, fmt.Errorf("proposal %s belongs to somebody else", a.ID)
		}
		if at, _ := store.ProposalClosure(old); at != "" {
			// A closed proposal is a record of what people voted on, so the
			// text it was voted on is not the owner's to rewrite afterwards.
			// The votes and the closure would still be there, saying they
			// agreed to something nobody can read any more.
			return nil, fmt.Errorf("proposal %s closed at %s and is a record now - "+
				"it says what was voted on, so edit it before it closes rather than after",
				a.ID, at)
		}
		// An update states what changes; the rest of the proposal stands.
		if art.Title == "" {
			art.Title = old.Title
		}
		if art.Body == "" {
			art.Body = old.Body
		}
		if a.Tags == nil {
			art.Tags = old.Tags
		}
		if a.Scope == "" {
			// An update that says nothing about scope keeps the one the
			// proposal has - the rule mem_write keeps, and the reason is the
			// same: a proposal shared across a grant would otherwise be pulled
			// back to its project by somebody fixing a typo in the title, with
			// nothing said about it.
			visibility = old.Visibility
		}
		// Including whether it is open. An edit is not a way to reopen a closed
		// proposal - the closure is in fields, carried forward below, and the
		// status the row already has is the one that goes back in.
		art.Status = old.Status
		art.Discovery, art.Severity, art.Related = old.Discovery, old.Severity, old.Related
		art.FilePath = old.FilePath
		if len(old.Fields) > 0 {
			if err := json.Unmarshal(old.Fields, &fields); err != nil {
				return nil, fmt.Errorf("proposal %s carries fields that do not parse: %w", a.ID, err)
			}
		}
		// Where a proposal lives is not something an update says.
		home = old.Project
	} else if art.Title == "" && strings.TrimSpace(art.Body) == "" {
		return nil, errors.New("a proposal needs a title or a body: what is being proposed")
	}

	fields = withRoom(fields, room, "")
	if len(fields) > 0 {
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		art.Fields = raw
	}

	art.OwnerUser = p.UserID
	art.Visibility = visibility
	if visibility == store.VisibilityPersonal {
		art.Project = nil
	} else {
		if p.Project == "" {
			return nil, fmt.Errorf("this token has no project, so it can only write scope=personal, not %s",
				scopeOf(visibility))
		}
		if home == nil || *home == "" {
			if a.ID != "" {
				return nil, fmt.Errorf("proposal %s has no project and is its owner's alone; "+
					"an update cannot move it into %s as %s - create it there instead",
					a.ID, p.Project, scopeOf(visibility))
			}
			here := p.Project
			home = &here
		}
		if *home != p.Project {
			return nil, fmt.Errorf("proposal %s lives in project %s, and this token writes in %s",
				art.ID, *home, p.Project)
		}
		art.Project = home
	}

	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if err := m.db.WriteMemory(ctx, art, &store.Event{
		Type:  "proposal.write",
		Room:  proposalRoomOr(room),
		Actor: actor,
		Body:  art.Title,
	}); err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
}

// proposalRead is the whole record: the proposal, every vote in the order they
// were cast, and the tally.
func proposalRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, errors.New("id is required")
	}

	art, err := m.db.ReadProposal(ctx, p, a.ID, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notAProposal(a.ID)
	}
	if err != nil {
		return nil, err
	}
	// The same answer the HTTP route gives, from the same function: an agent
	// and a console reading one proposal should not be reading two shapes.
	return viewProposal(ctx, m.db, p, art)
}

// voteTool records a vote. Every rule it keeps is in the store, so this surface
// and the HTTP one cannot drift into two ideas of who may vote on what.
func voteTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Proposal string `json:"proposal"`
		Choice   string `json:"choice"`
		Reason   string `json:"reason"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Proposal) == "" {
		return nil, errors.New("proposal is required: a vote is on something")
	}
	if len(a.Reason) > maxVoteReason {
		return nil, fmt.Errorf("the reason is %d bytes, over the %d ceiling - "+
			"a vote records why in a line, and the argument belongs in the room",
			len(a.Reason), maxVoteReason)
	}

	e, err := m.db.CastVote(ctx, p, strings.TrimSpace(a.Proposal), a.Choice, a.Reason)
	if err != nil {
		return nil, proposalError(a.Proposal, err)
	}
	// The tally as it stands after this vote, so the caller sees what it did
	// without a second call - and sees the two numbers that say a change of
	// mind appended rather than overwrote.
	votes, err := m.db.ProposalVotes(ctx, p, e.Artifact)
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{
		"vote": store.VoteOf(e), "tally": store.TallyOf(votes),
	}), nil
}

func proposalList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Room   string `json:"room"`
		Status string `json:"status"`
		Scope  string `json:"scope"`
		Limit  int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q := store.ArtifactQuery{Type: proposalType, Limit: a.Limit}
	var err error
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}
	if q.Status, err = oneOf("status", a.Status,
		[]string{store.ProposalOpen, store.ProposalClosed}, ""); err != nil {
		return nil, err
	}
	if a.Scope != "" {
		scope, err := oneOf("scope", a.Scope, memScopes, "")
		if err != nil {
			return nil, err
		}
		q.Visibility = visibilityOf(scope)
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}

// notAProposal is the only thing an unreadable id ever gets back, whether it is
// missing, out of reach, or something that is not a proposal at all.
func notAProposal(id string) error { return fmt.Errorf("no such proposal: %s", id) }

// proposalError turns a store refusal into the sentence an agent reads.
//
// A proposal the caller cannot read comes back from the store as ErrNotFound,
// which every read here answers as an id that is not there: voting is not a way
// to discover that something exists. The closed refusal is passed through
// whole, because the moment it names is the point of it.
func proposalError(id string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return notAProposal(id)
	}
	var closed store.ProposalClosedError
	if errors.As(err, &closed) {
		return errors.New(closed.Error())
	}
	return err
}

// proposalRoomOr is the room a proposal's own write lands in: the room it names,
// or the proposals room when it names none.
func proposalRoomOr(room string) string {
	if room != "" {
		return room
	}
	return store.ProposalRoom
}
