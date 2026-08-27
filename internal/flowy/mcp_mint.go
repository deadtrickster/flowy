package flowy

// The mint's MCP surface: one tool, operator-gated, over store.MintAgent.
//
// The gate is the point of this door's existing at all. An agent must not mint
// an agent - the flat-depth rule, and here it matters more than anywhere: the
// record of who joined this fabric would otherwise be whoever felt like
// joining it. So the tool answers only the operator's own token, and says so
// in the refusal rather than 404ing: a refused door that pretends not to exist
// teaches callers to hunt for one that does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deadtrickster/flowy/internal/store"
)

var mintTools = []tool{
	{
		Name: "agent_mint",
		Description: "Seat an agent on this node: user row, agent row, token and " +
			"signing key with its epoch, in one operation that refuses rather than " +
			"half-creating. The operator's own token only - an agent does not mint " +
			"an agent. Returns the ids and the token; the private key never leaves " +
			"the node.",
		InputSchema: object(props{
			"handle":  str("The name the agent speaks under. The roster shows it, mentions resolve to it."),
			"kind":    str("The runtime the agent runs on: claude, glm, opencode, ..."),
			"project": str("The agent's home project. Must already be declared in the registry."),
		}, []string{"handle", "kind", "project"}),
		call: mintTool,
	},
}

func mintTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	if m.operator == "" || p.UserID != m.operator {
		return nil, errors.New("minting an agent is the operator's: " +
			"this token is not the operator's own, and an agent does not mint an agent")
	}
	var a struct {
		Handle  string `json:"handle"`
		Kind    string `json:"kind"`
		Project string `json:"project"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	minted, err := m.db.MintAgent(ctx, store.MintSpec{
		Handle: a.Handle, Kind: a.Kind, Project: a.Project,
	})
	if err != nil {
		return nil, fmt.Errorf("mint %s: %w", a.Handle, err)
	}
	return map[string]any{
		"handle": a.Handle, "user": minted.User, "agent": minted.Agent,
		"token": minted.Token, "epoch": minted.Epoch,
	}, nil
}
