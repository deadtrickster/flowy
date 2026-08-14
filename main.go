// Command flowy is the host-side Handoff Fabric node.
//
// Phase 0 is the skeleton and the schema spine: a server that answers /healthz
// against the database, and stubs for the surfaces later phases fill in.
package main

import (
	"fmt"
	"os"
)

const usage = `flowy - Handoff Fabric node

usage: flowy <command> [flags]

commands:
  serve    run the HTTP server (env: DATABASE_URL, FLOWY_ADDR, FLOWY_NODE)
  mcp      MCP surface for agents (not yet)
  fuse     FUSE mount of artifacts (not yet)
  sync     peer replication (not yet)
  version  print the version and exit
  help     print this message
`

// version is the node's build version.
const version = "0.1.0-phase0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch cmd := os.Args[1]; cmd {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy serve: %v\n", err)
			os.Exit(1)
		}
	case "mcp":
		fmt.Println("mcp: not yet")
	case "fuse":
		fmt.Println("fuse: not yet")
	case "sync":
		fmt.Println("sync: not yet")
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "flowy: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}
