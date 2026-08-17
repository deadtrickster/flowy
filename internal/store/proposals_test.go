package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// proposalIn writes an open proposal owned by p, in p's project. It is written
// through UpsertArtifact rather than through a verb because a proposal is an
// ordinary artifact - which is the claim - and what these tests are about is
// what happens to it afterwards.
func proposalIn(t *testing.T, ctx context.Context, db *DB, p *Principal, room, title string) *Artifact {
	t.Helper()

	fields, err := json.Marshal(map[string]any{RoomField: room})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	project := p.Project
	art := &Artifact{
		ID: ulid.NewString(), Type: ProposalType, Project: &project,
		OwnerUser: p.UserID, Title: title, Status: ProposalOpen,
		Visibility: VisibilityProjectOnly, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("write proposal: %v", err)
	}
	return art
}

// choices is what the log holds, in the order it holds it.
func choices(votes []Vote) []string {
	out := make([]string, 0, len(votes))
	for _, v := range votes {
		out = append(out, v.Actor+"="+v.Choice)
	}
	return out
}

// A vote is consent, and consent from somebody who cannot read what they are
// agreeing to is not consent to anything. So the read filter decides who may
// vote, and it decides it in the same words a read of the proposal would: an id
// that is out of reach is an id that is not there.
//
// The second half is the one that matters. A refusal that answers the caller
// and writes the row anyway would pass a test that only looked at the error, so
// this asks the owner - who can see everything about their own proposal - what
// the log actually holds.
func TestAVoteFromSomebodyWhoCannotReadTheProposalIsRefused(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pv")
	elsewhere := declaredProject(t, ctx, db, "px")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	art := proposalIn(t, ctx, db, owner, "build", "move the gate to the wired interface")

	if _, err := db.CastVote(ctx, stranger, art.ID, VoteFor, "sounds good to me"); err == nil {
		t.Fatal("a principal who cannot read the proposal voted on it")
	} else if err != ErrNotFound {
		t.Fatalf("the refusal was %v, want the answer a read of it would give", err)
	}

	// And nothing landed. The owner can read every vote on their own proposal,
	// so an empty log here is the whole log.
	votes, err := db.ProposalVotes(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("votes: %v", err)
	}
	if len(votes) != 0 {
		t.Fatalf("the refused vote is in the log anyway: %v", choices(votes))
	}
	if tally := TallyOf(votes); tally.Voters != 0 || tally.For != 0 {
		t.Fatalf("the tally counted a refused vote: %+v", tally)
	}

	// The other half of the same rule: the stranger cannot read the votes
	// either, because a vote is no more readable than the proposal it names.
	if _, err := db.CastVote(ctx, owner, art.ID, VoteFor, ""); err != nil {
		t.Fatalf("the owner could not vote on their own proposal: %v", err)
	}
	seen, err := db.ProposalVotes(ctx, stranger, art.ID)
	if err != nil {
		t.Fatalf("votes as the stranger: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("a principal who cannot read the proposal read %d of its votes", len(seen))
	}
}

// The discriminating case for the whole surface.
//
// An implementation that stored the vote on the artifact, or that updated the
// principal's row in place, passes every tally test ever written and destroys
// the thing this exists for: "who agreed to this, and when" is a question about
// the votes that are no longer current. A decision that was settled in one
// message and re-argued two hours later is exactly the case where the earlier
// vote is the evidence.
//
// So this asserts the log, not the total: both entries are there, in the order
// they were cast, with the old choice still readable - and only then that the
// tally follows the latest.
func TestChangingAVoteAppendsAndTheOldVoteIsStillThere(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := proposalIn(t, ctx, db, owner, "general", "one word for nobody, not two")

	first, err := db.CastVote(ctx, owner, art.ID, VoteFor, "reads better")
	if err != nil {
		t.Fatalf("first vote: %v", err)
	}
	second, err := db.CastVote(ctx, owner, art.ID, VoteAgainst, "the migration is not worth it")
	if err != nil {
		t.Fatalf("second vote: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("the second vote reused the first one's row, so the first is gone")
	}

	votes, err := db.ProposalVotes(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("votes: %v", err)
	}
	if len(votes) != 2 {
		t.Fatalf("the log holds %d votes, want both of them: %v", len(votes), choices(votes))
	}
	if votes[0].ID != first.ID || votes[0].Choice != VoteFor {
		t.Fatalf("the vote that was changed is not in the log as it was cast: %+v", votes[0])
	}
	if votes[0].Reason != "reads better" {
		t.Fatalf("the old vote lost why it was cast: %q", votes[0].Reason)
	}
	if votes[1].ID != second.ID || votes[1].Choice != VoteAgainst {
		t.Fatalf("the second vote read back as %+v", votes[1])
	}
	if votes[0].SeqHLC >= votes[1].SeqHLC {
		t.Fatalf("the votes are not in the order they were cast: %d then %d",
			votes[0].SeqHLC, votes[1].SeqHLC)
	}

	// And the tally follows the latest, counting the principal once.
	tally := TallyOf(votes)
	if tally.Against != 1 || tally.For != 0 {
		t.Fatalf("the tally is %+v, want the latest vote and not the first", tally)
	}
	if tally.Voters != 1 || tally.Votes != 2 {
		t.Fatalf("the tally is %+v, want one voter behind two entries", tally)
	}
}

// One principal, one vote, however many times they changed their mind - and the
// number of entries said beside it, so a reader can see that the two are
// different numbers without going and counting the log.
func TestTheTallyCountsOneVotePerPrincipalNotOnePerEvent(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := proposalIn(t, ctx, db, owner, "general", "ban the word from the owner column")

	// Three parties in one room: the person, their agent, and a second agent.
	// An agent is its own voice here - four agents and a person are five
	// parties to a decision - so the actor is the seat and not the user behind
	// it, and a build that folded an agent's vote into its user's would count
	// two of these as one.
	agent := &Principal{UserID: owner.UserID, AgentID: "a-" + ulid.NewString(), Project: project}
	other := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: project}

	cast := func(p *Principal, choice string) {
		t.Helper()
		if _, err := db.CastVote(ctx, p, art.ID, choice, ""); err != nil {
			t.Fatalf("vote %s: %v", choice, err)
		}
	}
	cast(owner, VoteFor)
	cast(agent, VoteAgainst)
	cast(agent, VoteFor)     // changed their mind
	cast(other, VoteAbstain) // and one who is not standing in the way
	cast(owner, VoteFor)     // and one who said the same thing twice

	votes, err := db.ProposalVotes(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("votes: %v", err)
	}
	tally := TallyOf(votes)
	if tally.For != 2 || tally.Against != 0 || tally.Abstain != 1 {
		t.Fatalf("the tally is %+v, want two for, none against and one abstention: %v",
			tally, choices(votes))
	}
	if tally.Voters != 3 {
		t.Fatalf("the tally counted %d voters, want the three principals", tally.Voters)
	}
	if tally.Votes != 5 {
		t.Fatalf("the tally says %d entries behind it, want the five that were cast", tally.Votes)
	}
}

// Closing is a line under the decision, and the line has to hold: a vote after
// it is refused, and the refusal says when it closed. Without the moment, a
// caller told only "closed" has to go and find out whether it closed before or
// after they made up their mind - which is the argument this surface exists to
// stop having.
func TestAClosedProposalRefusesVotesAndSaysWhenItClosed(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	late := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := proposalIn(t, ctx, db, owner, "general", "gate: ban a word from a column")

	if _, err := db.CastVote(ctx, owner, art.ID, VoteFor, "in time"); err != nil {
		t.Fatalf("the vote before the close: %v", err)
	}

	closed, _, err := db.CloseProposal(ctx, owner, art.ID, "agreed: the column, not the page")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Status != ProposalClosed {
		t.Fatalf("the proposal is %q after being closed", closed.Status)
	}
	at, outcome := ProposalClosure(closed)
	if at == "" {
		t.Fatal("the closure recorded no moment, so no refusal can name one")
	}
	if outcome != "agreed: the column, not the page" {
		t.Fatalf("the outcome read back as %q", outcome)
	}

	_, err = db.CastVote(ctx, late, art.ID, VoteAgainst, "I have thoughts")
	if err == nil {
		t.Fatal("a closed proposal took another vote")
	}
	if !strings.Contains(err.Error(), at) {
		t.Fatalf("the refusal is %q, and it does not say when it closed (%s)", err, at)
	}
	if !strings.Contains(err.Error(), outcome) {
		t.Fatalf("the refusal is %q, and it does not say what was decided", err)
	}

	// The refusal is a refusal: nothing was written, and what was agreed before
	// the close is unchanged.
	votes, err := db.ProposalVotes(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("votes: %v", err)
	}
	if len(votes) != 1 || votes[0].Choice != VoteFor {
		t.Fatalf("the late vote landed anyway: %v", choices(votes))
	}

	// And it closed once. A second outcome over the first would rewrite the
	// record rather than add to it.
	if _, _, err := db.CloseProposal(ctx, owner, art.ID, "actually, no"); err == nil {
		t.Fatal("a closed proposal was closed again, with a new outcome")
	} else if !strings.Contains(err.Error(), at) {
		t.Fatalf("the second close was refused with %q, which does not say when it closed", err)
	}
}

// Who may close: whoever may write the proposal, which is its owner - the bar
// every other update of an artifact keeps here. Nothing about being able to
// read a proposal, or to vote on it, says anything about being able to declare
// what it came to.
func TestOnlyWhoeverMayWriteTheProposalMayCloseIt(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	roommate := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := proposalIn(t, ctx, db, owner, "general", "three-state seeding for the status colours")

	// The roommate reads it and votes on it, which is the whole of what being
	// in the project buys.
	if _, err := db.CastVote(ctx, roommate, art.ID, VoteFor, ""); err != nil {
		t.Fatalf("a principal of the project could not vote: %v", err)
	}
	if _, _, err := db.CloseProposal(ctx, roommate, art.ID, "I say it passed"); err == nil {
		t.Fatal("somebody who does not own the proposal closed it")
	}
	again, err := db.ReadProposal(ctx, roommate, art.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if at, _ := ProposalClosure(again); at != "" || again.Status != ProposalOpen {
		t.Fatalf("the refused close closed it anyway: status %q, at %q", again.Status, at)
	}

	// A close with nothing recorded is not a close: the outcome is the record.
	if _, _, err := db.CloseProposal(ctx, owner, art.ID, "   "); err == nil {
		t.Fatal("a proposal was closed with no outcome")
	}
}

// A choice nothing can count is not a vote, and defaulting it would put a word
// in somebody's mouth.
func TestAVoteIsOneOfTheThreeChoices(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	art := proposalIn(t, ctx, db, owner, "general", "the shape of the vote")

	for _, bad := range []string{"", "yes", "FOR", "veto"} {
		if _, err := db.CastVote(ctx, owner, art.ID, bad, ""); err == nil {
			t.Fatalf("%q was accepted as a vote", bad)
		}
	}
	votes, err := db.ProposalVotes(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("votes: %v", err)
	}
	if len(votes) != 0 {
		t.Fatalf("a refused choice is in the log: %v", choices(votes))
	}
}
