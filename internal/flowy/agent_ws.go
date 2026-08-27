package flowy

// One websocket per session, carrying several streams, and control outranks
// output.
//
// WHY A WEBSOCKET AND NOT THIS NODE'S SSE. internal/flowy/stream.go says the
// event stream is SSE because "the traffic is one-directional... a websocket
// would add a second protocol to carry nothing back", and that reasoning holds
// for envelopes about rows. It does not hold here, and the operator's argument
// is the one that decides it: a single socket per session carrying different
// streams MEANS WE CAN HAVE PRIORITIES. Split across SSE-down and POST-up there
// is no shared queue to prioritise in - a resize or an exit notice sits behind
// whatever output is already in flight, on a connection this node does not
// control the ordering of. Here there is one send loop and two queues, and
// control is drained first. Priority is not a property of the framing; it is a
// property of having one place where the ordering is decided, which is what
// this buys.
//
// THE TAG IS A BYTE AND THE PAYLOAD IS RAW. Frame zero-byte names the stream;
// everything after it is that stream's bytes, unencoded. Terminal output is
// arbitrary bytes and is the bulk of the traffic, so base64 here would cost a
// third of the bandwidth on the busy half to satisfy a text protocol we are not
// using. Control frames carry JSON after their tag, where the cost is nothing
// and the readability is worth it.
//
// WIDENING LATER. The operator was explicit that agents and multiplication come
// later. A tag byte leaves 250-odd values free, so a second terminal on the
// same socket is another tag rather than another connection - which is the
// whole reason to put streams on one socket now rather than when there are two
// of them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// The streams on the socket. A tag is one byte, first in every frame.
const (
	// agentStreamOut is the shell's output, server to client. The bulk, and the
	// low-priority queue.
	agentStreamOut = 0x01
	// agentStreamIn is keystrokes, client to server.
	agentStreamIn = 0x02
	// agentStreamControl is JSON both ways: hello, resize, exited. HIGH
	// priority - an exit notice that queued behind a megabyte of scrollback
	// would be read as a hung terminal.
	agentStreamControl = 0x03
)

// The largest single frame a client may send.
//
// A paste is why this is not tiny; a bound is why it is not absent. The input
// side writes straight into a terminal, and an unbounded frame is a way to make
// this node hold a lot of memory per socket.
const maxAgentFrame = 64 << 10

// How many output frames may be queued for one socket before the slow reader is
// dropped.
//
// A BOUND, AND THE DROP IS TOLD. A browser that cannot keep up must not be able
// to hold the shell still - pump() feeds every reader, so one stalled socket
// would stall the session. The reader is closed with a reason rather than
// silently starved, because a terminal that stops updating and says nothing is
// indistinguishable from one whose shell has hung.
const agentSendQueue = 256

// agentControl is what rides the control stream, in both directions.
//
// Every field is present in the JSON when it is meaningful and the zero value
// is never overloaded: `Type` says which of these this is, so a reader never
// has to infer the message from which fields happen to be set.
type agentControl struct {
	Type string `json:"type"`
	// hello
	ID      string `json:"id,omitempty"`
	Project string `json:"project,omitempty"`
	Started string `json:"started,omitempty"`
	// hello: how many bytes of scrollback this node no longer has. Present and
	// zero is "you have everything"; it is not omitempty for that reason.
	Dropped int `json:"dropped"`
	// resize, client to server
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	// exited
	Why string `json:"why,omitempty"`
}

// handleAgentSocket runs one session's socket.
//
// GET /api/agent/socket?project=NAME  (websocket)
//
// The session is created BY the socket rather than by a separate call, and that
// is deliberate: a run door that answered an id and left the caller to connect
// would have a window in which a VM is up and nobody is attached, which is the
// abandoned-VM failure with extra steps. Here the VM's lifetime is the socket's.
func (s *server) handleAgentSocket(w http.ResponseWriter, r *http.Request) {
	// firecodeBin and errNoFirecode are api_vm.go's, and this uses them rather
	// than looking the binary up again. That file already decided what "this
	// node cannot run VMs" means and says it in a sentence naming the fix; a
	// second answer to the same question here would be a second sentence to
	// keep in step, and the whole point of its 503-not-empty-list rule is that
	// there is ONE answer.
	binary, err := firecodeBin()
	if err != nil {
		// SAID BEFORE THE UPGRADE, as an ordinary HTTP refusal. A websocket
		// that accepts and then immediately closes tells a browser almost
		// nothing; a 503 with a sentence tells a person what is missing.
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoFirecode.Error()))
		return
	}

	// A NAME, NEVER A PATH - api_vm.go's rule, and it is a security decision
	// rather than a tidiness one: a caller who can name a directory can pack
	// any directory on this host into a VM that has the network. The name is
	// checked against what firecode itself advertises, so this node never
	// invents the set.
	// THE CALLER NAMES, THE NODE RESOLVES. api_vm.go's rule is that a project
	// is a NAME and never a path, because a caller who can name a directory can
	// pack any directory on this host into a VM that has the network. But
	// `firecode shell --project` takes a DIRECTORY, so something has to map one
	// to the other, and the only honest place is here, against the roster
	// firecode itself publishes. Asking the door that maps names to paths is
	// also this repo's rule about identifiers: resolve, never guess.
	project := r.URL.Query().Get("project")
	workdir, err := firecodeProjectPath(r.Context(), project)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The console is served by this same node, so the browser's origin is
		// this node's. Anything else is a page somebody else is serving trying
		// to open a shell here.
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		return
	}
	// A reason, so a closed panel can say why rather than just going blank.
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxAgentFrame + 1024)

	id := ulid.NewString()
	sess, err := s.agents.start(id, project, workdir, binary, agentSize{})
	if err != nil {
		sendAgentControl(r.Context(), conn, agentControl{
			Type: "exited",
			Why:  fmt.Sprintf("the VM could not be started: %v", err),
		})
		_ = conn.Close(websocket.StatusInternalError, "could not start")
		return
	}
	defer func() { _ = s.agents.stop(id, "the panel disconnected") }()

	ch, backlog, dropped := sess.attach()
	defer sess.detach(ch)

	ctx, stop := context.WithCancel(r.Context())
	defer stop()

	// THE SEND LOOP IS WHERE PRIORITY LIVES. Two queues, and the high one is
	// drained to empty before a single low frame goes out. A websocket is one
	// ordered byte stream, so priority cannot mean anything else: it is a
	// decision about what this node writes next, and this select is that
	// decision.
	high := make(chan []byte, 32)
	low := make(chan []byte, agentSendQueue)
	go func() {
		defer stop()
		for {
			// Offered first and on its own, so a control frame never waits
			// behind output that was queued before it.
			select {
			case <-ctx.Done():
				return
			case frame := <-high:
				if !writeAgentFrame(ctx, conn, frame) {
					return
				}
				continue
			default:
			}
			select {
			case <-ctx.Done():
				return
			case frame := <-high:
				if !writeAgentFrame(ctx, conn, frame) {
					return
				}
			case frame := <-low:
				if !writeAgentFrame(ctx, conn, frame) {
					return
				}
			}
		}
	}()

	v := viewAgent(sess)
	high <- agentControlFrame(agentControl{
		Type: "hello", ID: v.ID, Project: v.Project, Started: v.Started, Dropped: dropped,
	})
	if len(backlog) > 0 {
		low <- append([]byte{agentStreamOut}, backlog...)
	}

	// Output into the low queue.
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					// The shell ended. High priority: this is the frame that
					// tells the panel it is looking at a dead terminal.
					select {
					case high <- agentControlFrame(agentControl{
						Type: "exited", Why: viewAgent(sess).Why,
					}):
					case <-ctx.Done():
					}
					return
				}
				select {
				case low <- append([]byte{agentStreamOut}, chunk...):
				default:
					// THE SLOW READER IS TOLD, NOT STARVED. Dropping quietly
					// would leave a terminal that renders a corrupted screen
					// with no way to know why.
					select {
					case high <- agentControlFrame(agentControl{
						Type: "exited",
						Why:  "this panel fell too far behind to keep the terminal correct - reopen it",
					}):
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()

	// And the read loop: keystrokes and control, from the browser.
	for {
		typ, frame, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(frame) == 0 {
			continue
		}
		switch frame[0] {
		case agentStreamIn:
			if err := sess.write(frame[1:]); err != nil {
				return
			}
		case agentStreamControl:
			var c agentControl
			if err := json.Unmarshal(frame[1:], &c); err != nil {
				continue
			}
			if c.Type == "resize" {
				if err := sess.resize(agentSize{Rows: c.Rows, Cols: c.Cols}); err != nil {
					if errors.Is(err, ErrNoSession) {
						return
					}
				}
			}
		default:
			// AN UNKNOWN TAG IS REFUSED, NOT IGNORED. A stream this node does
			// not know is a client speaking a protocol this one does not, and
			// carrying on would be the callee dropping an argument.
			_ = conn.Close(websocket.StatusUnsupportedData,
				fmt.Sprintf("stream %d is not one this node speaks", frame[0]))
			return
		}
	}
}

func agentControlFrame(c agentControl) []byte {
	body, err := json.Marshal(c)
	if err != nil {
		return []byte{agentStreamControl, '{', '}'}
	}
	return append([]byte{agentStreamControl}, body...)
}

func sendAgentControl(ctx context.Context, conn *websocket.Conn, c agentControl) {
	_ = conn.Write(ctx, websocket.MessageBinary, agentControlFrame(c))
}

func writeAgentFrame(ctx context.Context, conn *websocket.Conn, frame []byte) bool {
	// A write deadline per frame, so one wedged socket cannot hold its
	// goroutines for the life of the node.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageBinary, frame) == nil
}

// agentView is what a caller is told about a session.
//
// Done and Why are always present, never omitempty: a client cannot tell "still
// running" from "the field was left out" if the running case has no field.
type agentView struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Started string `json:"started"`
	Done    bool   `json:"done"`
	Why     string `json:"why"`
}

func viewAgent(s *agentSession) agentView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return agentView{
		ID:      s.id,
		Project: s.project,
		Started: s.started.UTC().Format(time.RFC3339),
		Done:    s.done,
		Why:     s.why,
	}
}

// firecodeProjectPath turns a project NAME into the directory firecode says it
// is, refusing a name this host does not advertise.
//
// ASKED, NOT PATTERN-MATCHED. Whether a name is offered is a fact only firecode
// holds; a regex here would drift from the roster the moment somebody adds a
// project, and would be this node inventing an answer it was never given.
//
// An empty name is not an error and not a default guess: it means "wherever
// firecode would run anyway", and the empty path passed on to `shell` is
// exactly that.
func firecodeProjectPath(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	out, err := runFirecode(ctx, 15*time.Second, "projects", "--json")
	if err != nil {
		return "", errNoFirecode
	}
	var roster struct {
		Projects []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(out, &roster); err != nil {
		return "", fmt.Errorf("firecode's project roster could not be read: %w", err)
	}
	for _, p := range roster.Projects {
		if p.Name != name {
			continue
		}
		// EXISTS IS ASKED RATHER THAN ASSUMED. The roster carries the answer,
		// and a VM packed over a directory that is not there fails deep inside
		// firecode with a message a person reads as a bug in this panel.
		if !p.Exists {
			return "", fmt.Errorf("firecode knows %q but its directory is not on this host", name)
		}
		return p.Path, nil
	}
	return "", fmt.Errorf(
		"%q is not a project this host advertises - GET /api/vm/projects says which are", name)
}
