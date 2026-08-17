package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The proposal surface rides the same registration the memory and report tools
// do, and an agent that never reads the guide still has to be able to find it:
// the tools are listed, the write says what closes a proposal, and the vote
// says the three choices. The writes themselves are exercised against a real
// store by the gate and by the store's own tests.
func TestProposalSurfaceIsListedAndDocumented(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	listed := map[string]tool{}
	for _, tl := range resp.Result.(map[string]any)["tools"].([]tool) {
		listed[tl.Name] = tl
	}
	for _, want := range []string{"proposal_write", "proposal_read", "vote", "proposal_list"} {
		got, ok := listed[want]
		if !ok {
			t.Errorf("tools/list does not offer %s", want)
			continue
		}
		if _, has := got.InputSchema["properties"]; !has {
			t.Errorf("tool %s has no properties in its schema", want)
		}
	}

	// Closing is manual and it is a write, so the write's description has to
	// say what closes a proposal. An agent that cannot see that either never
	// closes anything or expects a quorum rule to do it - and there is no
	// quorum rule, deliberately.
	w, ok := listed["proposal_write"]
	if !ok {
		t.Fatal("proposal_write is not listed")
	}
	if !strings.Contains(w.Description, "outcome") {
		t.Errorf("proposal_write never says what closes a proposal: %q", w.Description)
	}
	props := w.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"outcome", "room", "id"} {
		if _, has := props[field]; !has {
			t.Errorf("proposal_write schema has no %s field", field)
		}
	}

	// The vote is the verb the whole thing exists for: it names something, it
	// is one of three choices, and both are required.
	v, ok := listed["vote"]
	if !ok {
		t.Fatal("vote is not listed")
	}
	required, _ := v.InputSchema["required"].([]string)
	if len(required) != 2 || required[0] != "proposal" || required[1] != "choice" {
		t.Errorf("vote requires %v, want the proposal and the choice", required)
	}
	choice, _ := v.InputSchema["properties"].(map[string]any)["choice"].(map[string]any)
	values, _ := choice["enum"].([]string)
	if len(values) != len(store.VoteChoices) {
		t.Fatalf("the choice enum is %v, want %v", values, store.VoteChoices)
	}
	for i, want := range store.VoteChoices {
		if values[i] != want {
			t.Errorf("the choice enum is %v, want %v", values, store.VoteChoices)
			break
		}
	}
	// And the detail, which is where the detail goes - instructions.md is
	// capped and is not the place for it.
	for _, want := range []string{"proposal_write", "vote", "abstain"} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide never mentions %q", want)
		}
	}
}

// What the proposal tools refuse before they ever reach the store. The server
// here has no database at all, so a check that got past them would panic
// instead of passing.
func TestProposalToolsRefuseWhatTheyCannotWrite(t *testing.T) {
	m := &mcpServer{node: "test"}
	ctx := context.Background()
	seat := &store.Principal{UserID: "ua", AgentID: "aa", Project: "pa"}

	// A proposal is owned by whoever wrote it, so a token that resolves to
	// nobody has no proposal to write.
	if _, err := proposalWrite(ctx, m, &store.Principal{Project: "pa"},
		json.RawMessage(`{"title":"something"}`)); err == nil {
		t.Error("a token with no user wrote a proposal")
	}
	if _, err := proposalWrite(ctx, m, seat, json.RawMessage(`{}`)); err == nil {
		t.Error("a proposal with nothing proposed in it was accepted")
	}

	// An outcome closes a proposal, so it names one. A proposal born closed is
	// a decision nobody could have voted on.
	_, err := proposalWrite(ctx, m, seat,
		json.RawMessage(`{"title":"decided already","outcome":"agreed"}`))
	if err == nil || !strings.Contains(err.Error(), "born open") {
		t.Errorf("a proposal created closed answered %v, want a refusal that says why", err)
	}

	// A room is one path segment, here as at every other surface that names one.
	if _, err := proposalWrite(ctx, m, seat,
		json.RawMessage(`{"title":"t","room":"two words"}`)); err == nil {
		t.Error("a room name that is not one was accepted")
	}

	// A vote is on something, and the argument for it belongs in the room.
	if _, err := voteTool(ctx, m, seat, json.RawMessage(`{"choice":"for"}`)); err == nil {
		t.Error("a vote on nothing was accepted")
	}
	long, err := json.Marshal(map[string]string{
		"proposal": "01HPROPOSAL", "choice": "for",
		"reason": strings.Repeat("x", maxVoteReason+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := voteTool(ctx, m, seat, long); err == nil {
		t.Error("a reason the size of an argument was accepted")
	}

	// And a status a list cannot narrow by is refused rather than ignored: a
	// filter that silently did nothing would answer with the closed proposals
	// mixed into the open ones and say nothing about it.
	if _, err := proposalList(ctx, m, seat, json.RawMessage(`{"status":"pending"}`)); err == nil {
		t.Error("a status that is not one was accepted")
	}
}

// A vote and a closure are minted by the verb that does the thing, so POST
// /api/events refuses them by hand and a push refuses them at the far end. The
// refusals that make the record worth reading - a voter who can read the
// proposal, and no vote after it closed - are on the verb, and an event a
// client could write itself would walk past both.
func TestVotesAreMintedByTheVerbAndNotWritableByHand(t *testing.T) {
	for _, minted := range []string{store.EventProposalVote, store.EventProposalClose} {
		if !mintedTypes[minted] {
			t.Errorf("POST /api/events would accept a %s written by hand", minted)
		}
		if !store.MintedEventType(minted) {
			t.Errorf("a pushed %s would be taken from a peer", minted)
		}
	}
}
