// Command flowy is the host-side Handoff Fabric node.
//
// Phase 0 is the skeleton and the schema spine, Phase 1 the typed API and the
// permission filter, Phase 2 the MCP surface: shared memory that every agent -
// Claude Code, GLM, opencode, Claude on the web - reads and writes over one
// store. Phase 3 is chat over the same event DAG and the console that reads it,
// embedded in this binary. Phase 4 is the agentic Jira layer on top: assignment
// as a share plus a task plus a thread, delegation to the assignee's agent, and
// an issue lifecycle whose every move is an event. Phase 5 is federation: two
// nodes, each with its own database, exchanging permission-filtered deltas and
// merging them last-writer-wins by hlc. Phase 6 is the forge bridge: a bug here
// is filed as an issue on GitHub or GitLab through that forge's own CLI, its
// state is read back, and the comments on it and the replies here are one
// conversation. Phase 6.5 signs what replicates: every row carries the ed25519
// signature of the node that wrote it, and a merge verifies that before it
// asks whether the principal handing it over was allowed to. Phase 7 is the
// file layer: `flowy fuse` mounts a principal's memory as files, so an agent
// writes memory where it already writes files and it lands in the store,
// indexed and searchable. It is opt-in, and everything above works whole
// without it.
package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = `flowy - Handoff Fabric node

usage: flowy <command> [flags]

commands:
  serve    run the HTTP server and the embedded console
           (env: DATABASE_URL, FLOWY_ADDR, FLOWY_NODE, FLOWY_OPERATOR,
           FLOWY_FORGE=gh|glab|mock)
  mcp      MCP server for agents: stdio by default, --http :PORT for a remote
           client (env: DATABASE_URL, FLOWY_TOKEN, FLOWY_NODE)
  inbox    block until somebody says something to you, then print it and exit
           (flowy inbox --as NAME [--deadline S] [--new] [--to-me];
           exit 0 something was said, 1 the deadline passed quietly, 2 broken)
  tui      the terminal client: rooms, inbox, memory, timeline, metrics and
           announcements over the HTTP API, keyboard-driven and tmux-friendly
           (flowy tui [--url URL] [--token T]; env: FLOWY_ADDR, FLOWY_TOKEN,
           then ~/.config/flowy/token)
  fuse     mount this principal's memory as files, so an agent writes memory
           where it already writes files
           (flowy fuse --mount <dir> [--token <t>]; --reconcile applies what an
           earlier mount queued and exits; env: DATABASE_URL, FLOWY_TOKEN,
           FLOWY_NODE)
  projects which project this token writes to, and the registry of what exists
           (flowy projects [list] | declare --project N [--origin R] [--fixture]
           | pin --project N --origin R; env: FLOWY_ADDR, FLOWY_TOKEN)
  sync     replicate with a peer: pull its delta, apply it, push ours
           (flowy sync --peer <url> --token <t>; env: DATABASE_URL, FLOWY_NODE,
           FLOWY_TOKEN)
  traces   collect one trace from this node and its peers, as one waterfall
           (flowy traces --trace <id> [--peer <url>,...] --token <t>;
           env: DATABASE_URL, FLOWY_NODE, FLOWY_TOKEN, FLOWY_OPERATOR)
  identity this node's signing key, and the peer keys it holds
           (flowy identity | list | pin --node N --key K | keygen --node N)
  sign     sign a replication delta read on stdin (flowy sign [--seed HEX])
  version  print the version and exit
  help     print this message
`

// release is the version of the code: the phase it belongs to, bumped by hand
// when a phase or a round of security work lands.
const release = "0.8.0"

// buildStamp names the build itself, and the build sets it:
//
//	go build -ldflags "-X main.buildStamp=$(git rev-parse --short HEAD)" .
//
// A version that is only the release is a version that cannot answer the
// question this kind of work asks - which build refused that row, which build
// is this peer running - because half a dozen distinct binaries report the same
// string. The stamp is what changes when the code does. A build with no flags,
// or `go run`, says "src", which is honest rather than a commit it is not.
var buildStamp = "src"

// version is what /healthz, GET /version, the MCP serverInfo, the sync handshake
// and `flowy version` report: the release and the build under it, joined by a
// plus - 0.7.3-fix14+3305508.
var version = versionOf(release, buildStamp)

// versionOf joins a release and a build stamp. It is a function so that the
// scheme is one line rather than a string literal repeated wherever a version
// is made, and so a test can ask whether two builds really do report two
// versions.
func versionOf(release, stamp string) string {
	if stamp == "" {
		return release
	}
	return release + "+" + stamp
}

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
		if err := mcpCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy mcp: %v\n", err)
			os.Exit(1)
		}
	case "inbox":
		// The one command whose exit code is its answer rather than a report of
		// whether it worked. A quiet deadline is not a failure - it is one of
		// the two things a waiter is for - so it is 1 and everything genuinely
		// broken is 2, and a restart loop can tell them apart without parsing
		// anything this prints.
		if err := inboxCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy inbox: %v\n", err)
			if errors.Is(err, errQuietDeadline) {
				os.Exit(1)
			}
			os.Exit(2)
		}
	case "tui":
		if err := tuiCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy tui: %v\n", err)
			os.Exit(1)
		}
	case "fuse":
		if err := fuseCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy fuse: %v\n", err)
			os.Exit(1)
		}
	case "projects":
		if err := projectsCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy projects: %v\n", err)
			os.Exit(1)
		}
	case "sync":
		if err := syncCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy sync: %v\n", err)
			os.Exit(1)
		}
	case "traces":
		if err := tracesCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy traces: %v\n", err)
			os.Exit(1)
		}
	case "identity":
		if err := identityCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy identity: %v\n", err)
			os.Exit(1)
		}
	case "sign":
		if err := signCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy sign: %v\n", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "flowy: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}
