package main

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestMintedTypesAgreeWithTheStore holds the two halves of one rule together.
//
// POST /api/events refuses the types this node's own handlers mint, and a
// pushed delta has to refuse the same ones - a replicated status event nobody
// moved is the same forgery arriving by another door. The two lists live in
// two packages because the store cannot import the server, so this is what
// stops them drifting apart.
func TestMintedTypesAgreeWithTheStore(t *testing.T) {
	for kind := range mintedTypes {
		if !store.MintedEventType(kind) {
			t.Errorf("%s is minted by this node's handlers, and replication would take it", kind)
		}
	}
	for _, kind := range []string{statusEventType, taskEventType, forgeEventType} {
		if !mintedTypes[kind] {
			t.Errorf("%s is written by a handler and is not on the endpoint's list", kind)
		}
	}
	// chat is not minted: it carries no authority beyond what saying something
	// already gives the same principal, on either side.
	if store.MintedEventType(chatEventType) || mintedTypes[chatEventType] {
		t.Error("chat is not a minted type; refusing it would stop conversations replicating")
	}
}
