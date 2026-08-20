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
           (flowy inbox --as NAME [--deadline S] [--new] [--to-me] [--drop-reader];
           exit 0 something was said, 1 the deadline passed quietly, 2 broken)
  say      put one message in a room, the other half of inbox
           (flowy say [--room R] [--to NAME] [--thread ID] "text", or stdin;
           exit 0 the node took it, 2 it refused)
  worklog  the chronology: read what the last few seats did, append before you
           stop. The verb a spawned agent has, since it is given no MCP server
           (flowy worklog read [--limit N] | append "what changed" [--next N]
           [--as-of A] [--branch B] [--ref ID] [--subject WHO] [--run ID]
           [--verify S]; env: FLOWY_ADDR, FLOWY_TOKEN)
  queue    what is waiting to land, and who holds the target. Every lock line
           carries the moment it was read, because a lock reading is a claim
           about the past (flowy queue [lock] [--target T]; env: FLOWY_ADDR,
           FLOWY_TOKEN, FLOWY_AGENT)
  tui      the terminal client: rooms, inbox, memory, timeline, metrics and
           announcements over the HTTP API, keyboard-driven and tmux-friendly
           (flowy tui [--url URL] [--token T] [--agent NAME]; env: FLOWY_ADDR,
           FLOWY_TOKEN, FLOWY_AGENT, then ~/.config/flowy/token - which is the
           operator's own, so falling through to it warns)
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
  todo     the queue from a shell: file a row, note on one, take one, close one
           (flowy todo file --title T [--room R] [--category C] [--scope S]
           "body" | note --id ID "text" | claim --id ID [--expect WHO]
           | done --id ID)
  note     a memory from a shell, personal unless a scope says otherwise
           (flowy note write --title T [--scope S] "body")
  merge    file a branch for the queue to land. Filing only - gating and
           landing belong to the drainer (flowy merge open --branch B
           [--target T] [--title L] [--assignee A] "what changed")
  mint     seat an agent: user, token and signing key in one operation, from
           the operator's hands - the only door besides MCP, and no agent may
           knock (flowy mint --handle N --kind K --project P [--agent-kind W];
           prints the token on stdout, everything else to stderr)
  passwd   set a PERSON's console password, read from stdin so it stays out of
           the shell history and the process list. There is no signup door -
           this is how an account gets one (flowy passwd --handle N
           [--keep-sessions]; a change signs every browser out by default)
  principal
           whose word a row is: the principal keys this node signs with and
           checks against (flowy principal [list] | keygen --as P [--epoch N]
           | pin --as P --key K [--epoch N] | exposed)
  sign     sign a replication delta read on stdin (flowy sign [--seed HEX]
           [--as P --principal-seed HEX])
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
	case "say":
		// 2 rather than 1 for a refusal, to match inbox: there, 1 means the
		// deadline passed quietly and only 2 means broken. Nothing about
		// saying something has a quiet-and-fine outcome, so it never exits 1.
		if err := sayCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy say: %v\n", err)
			os.Exit(2)
		}
	case "worklog":
		// 2 rather than 1 for a refusal, to match say: an entry the node did not
		// take must not exit the way one it took does, or a script appending
		// before it stops records nothing and reports success.
		if err := worklogCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy worklog: %v\n", err)
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
	case "get":
		// One door's answer on stdout, through the client that already exists -
		// see get.go for the 414 hand-built curls that bought this verb.
		if err := getCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy get: %v\n", err)
			os.Exit(1)
		}
	case "queue":
		// `queue wait` has outcomes rather than only a result, so its codes are
		// mapped here beside inbox's for the same reason: a script must tell
		// "still waiting" from "the node blinked" from "the branch is red"
		// without parsing what was printed. See queuewait.go.
		if err := queueCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy queue: %v\n", err)
			switch {
			case errors.Is(err, errWaitedOut):
				os.Exit(1)
			case errors.Is(err, errRowIsRed):
				os.Exit(3)
			}
			os.Exit(2)
		}
	case "nag":
		if err := nagCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy nag: %v\n", err)
			os.Exit(2)
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
	case "mint":
		if err := mintCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy mint: %v\n", err)
			os.Exit(1)
		}
	case "passwd":
		if err := passwdCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy passwd: %v\n", err)
			os.Exit(1)
		}
	case "todo":
		if err := todoCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy todo: %v\n", err)
			os.Exit(1)
		}
	case "note":
		if err := noteCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy note: %v\n", err)
			os.Exit(1)
		}
	case "merge":
		if err := mergeCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy merge: %v\n", err)
			os.Exit(1)
		}
	case "principal":
		if err := principalCmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "flowy principal: %v\n", err)
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
