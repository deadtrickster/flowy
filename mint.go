package main

// `flowy mint`: an agent seat as one operation, from the operator's hands.
//
// The worklog set the door pattern - one implementation, several callers - and
// this is the same shape for identity: store.MintAgent is the whole operation,
// this CLI is the operator's door onto it, and the MCP tool is the other.
// There is deliberately no HTTP route: an agent must not mint an agent, and a
// route would be a door anything with a token could knock on.
//
// The token is the one secret this prints, on stdout alone, so the operator
// can file it with a redirect; everything else a person reads goes to stderr.

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/deadtrickster/flowy/internal/store"
)

func mintCmd(args []string) error {
	fs := flag.NewFlagSet("mint", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name this node stamps onto every row")
	handle := fs.String("handle", "", "the name the agent speaks under - the roster shows it, mentions resolve to it")
	kind := fs.String("kind", "", "the runtime the agent runs on: claude, glm, opencode, ...")
	project := fs.String("project", "", "the agent's home project, which must already be declared")
	agentKind := fs.String("agent-kind", string(store.AgentKindWorker),
		"what the agent is for: worker, reviewer, system, monitor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *handle == "" || *project == "" {
		return fmt.Errorf("mint needs --handle and --project; --kind names the runtime")
	}

	db, err := store.Open(context.Background(), *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	minted, err := db.MintAgent(context.Background(), store.MintSpec{
		Handle: *handle, Kind: *kind, Project: *project, AgentKind: *agentKind,
	})
	if err != nil {
		return err
	}
	// A person reads who joined and where their word starts; the shell reads
	// the token. Redirect stdout to a 0600 file and the seat is filed.
	fmt.Fprintf(os.Stderr, "minted %s\n  user %s\n  agent %s\n  epoch %d\n",
		*handle, minted.User, minted.Agent, minted.Epoch)
	fmt.Println(minted.Token)
	return nil
}
