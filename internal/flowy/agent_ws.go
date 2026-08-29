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
	"fmt"
	"net/http"
	"strings"
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
	// Which terminal on this socket. One byte on the front of every data frame
	// and repeated here, so a control message and the bytes it is about name
	// the same thing.
	Slot byte `json:"slot"`
	// attach: which machine, and which session to adopt if one is named.
	Where   string `json:"where,omitempty"`
	Session string `json:"session,omitempty"`
	// attach: adopt the named session or fail, never start a new one.
	//
	// A PANEL COMING BACK IS NOT A PANEL ASKING FOR A SHELL. Reattaching on
	// mount is what stops a shell vanishing when somebody navigates away and
	// back; starting one on mount would boot a VM every time the page is
	// opened, which is a different and much worse thing. So the two are
	// different requests rather than the same request with a hopeful outcome.
	Adopt bool `json:"adopt,omitempty"`
	// attach: a byobu session on the host to join, by name.
	//
	// NAMED RATHER THAN DERIVED, so the panel can open the session somebody
	// picked out of the list - including one flowy never started. Empty means
	// the project's own, which is what a Run with nothing chosen asks for.
	Mux string `json:"mux,omitempty"`
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
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The console is served by this same node, so the browser's origin is
		// this node's. Anything else is a page somebody else is serving trying
		// to open a shell here.
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxAgentFrame + 1024)

	ctx, stop := context.WithCancel(r.Context())
	defer stop()

	high := make(chan []byte, 64)
	low := make(chan []byte, agentSendQueue)
	go func() {
		defer stop()
		pumpByPriority(ctx, high, low, func(frame []byte) bool {
			return writeAgentFrame(ctx, conn, frame)
		})
	}()

	// EVERY TERMINAL ON THIS SOCKET, by slot. A slot is one byte on the front
	// of every data frame, so output stays raw and costs one byte per frame
	// rather than a JSON envelope per chunk.
	//
	// The socket no longer owns a session: it ATTACHES to one. That is
	// OpenChamber's shape and the reason for it is the same - a connection is
	// how a browser is reaching this node, and a session is a shell on a
	// machine. Tying the two together is what made a dropped wire kill a guest.
	live := map[byte]*agentAttachment{}
	defer func() {
		for slot := range live {
			live[slot].detach()
		}
	}()

	for {
		typ, frame, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(frame) < 2 {
			continue
		}
		slot := frame[1]
		switch frame[0] {
		case agentStreamIn:
			if a := live[slot]; a != nil {
				if err := a.sess.write(frame[2:]); err != nil {
					sendAgentControl(ctx, high, agentControl{
						Type: "exited", Slot: slot, Why: "this shell is gone",
					})
				}
			}
		case agentStreamControl:
			var c agentControl
			if err := json.Unmarshal(frame[2:], &c); err != nil {
				continue
			}
			c.Slot = slot
			switch c.Type {
			case "attach":
				if live[slot] != nil {
					continue
				}
				a, why := s.attachAgent(ctx, r, c, high, low)
				if a == nil {
					sendAgentControl(ctx, high, agentControl{
						Type: "error", Slot: slot, Why: why,
					})
					continue
				}
				live[slot] = a
			case "detach":
				// DETACHING IS NOT STOPPING. The shell keeps running and the
				// VM stays up; this socket simply stops carrying it. Closing a
				// tab must not end somebody's build.
				if a := live[slot]; a != nil {
					a.detach()
					delete(live, slot)
				}
			case "resize":
				if a := live[slot]; a != nil {
					_ = a.sess.resize(agentSize{Rows: c.Rows, Cols: c.Cols})
				}
			case "stop":
				// The only message that ends a session, and it says so.
				if a := live[slot]; a != nil {
					_ = s.agents.stop(a.sess.id, "stopped from the panel")
					a.detach()
					delete(live, slot)
				}
			}
		default:
			_ = conn.Close(websocket.StatusUnsupportedData,
				fmt.Sprintf("stream %d is not one this node speaks", frame[0]))
			return
		}
	}
}

// agentAttachment is one terminal being carried by one socket.
type agentAttachment struct {
	sess *agentSession
	ch   chan []byte
	off  func()
}

func (a *agentAttachment) detach() {
	a.off()
	a.sess.detach(a.ch)
}

// attachAgent finds or starts the session an attach message names, and begins
// relaying it into this socket's queues under the message's slot.
//
// THE SNAPSHOT AND THE REGISTRATION ARE ONE STEP, which is what makes a
// terminal correct across a reattach. agentSession.attach takes the scrollback
// and adds the reader under a single lock, so a byte produced in between cannot
// be lost by arriving after the snapshot and before the channel existed - and
// the missing byte is nearly always the prompt. OpenChamber reaches the same
// property the other way, buffering into `pending` while `initializing` and
// replaying only what is newer than the snapshot; holding one lock is stronger
// and simpler, and this comment exists because the property is invisible in the
// code that depends on it.
func (s *server) attachAgent(
	ctx context.Context, r *http.Request, c agentControl, high, low chan []byte,
) (*agentAttachment, string) {
	// AN EXISTING SESSION IS ADOPTED. A stale id after a node restart is not an
	// error - it is simply a new session - because a browser remembering an id
	// across a deploy is the ordinary case.
	var sess *agentSession
	if id := strings.TrimSpace(c.Session); id != "" {
		if found, err := s.agents.get(id); err == nil {
			sess = found
		}
	}

	if sess == nil && c.Adopt {
		// Not an error worth logging: a remembered id after a node restart, or
		// after somebody stopped the shell, is the ordinary case. The panel
		// stays idle and offers Run.
		return nil, "that shell is no longer running"
	}

	if sess == nil {
		where := shellInGuest
		switch strings.TrimSpace(c.Where) {
		case "", string(shellInGuest):
		case string(shellOnHost):
			where = shellOnHost
		default:
			return nil, `where is "vm" or "host"`
		}

		binary := ""
		if where == shellInGuest {
			found, err := firecodeBin()
			if err != nil {
				return nil, errNoFirecode.Error()
			}
			binary = found
		}

		// A NAME, NEVER A PATH, resolved against the roster firecode itself
		// publishes - api_vm.go's rule, and a security decision rather than a
		// tidy one: a caller who can name a directory can pack any directory on
		// this host into a VM that has the network.
		workdir, err := firecodeProjectPath(r.Context(), c.Project)
		if err != nil {
			return nil, err.Error()
		}

		// A SESSION THE CALLER NAMED, WHEN IT NAMED ONE, and it is checked
		// rather than passed through. `mux` arrives from a browser, and it
		// becomes an argument to tmux: a name starting with a dash would be
		// read as a FLAG by new-session, which is how an argument vector still
		// gets you a command you did not write.
		mux := strings.TrimSpace(c.Mux)
		if strings.HasPrefix(mux, "-") {
			return nil, "a session name may not begin with a dash"
		}
		started, err := s.agents.startIn(newAgentID(), c.Project, workdir, binary, where, mux,
			agentSize{Rows: c.Rows, Cols: c.Cols})
		if err != nil {
			return nil, fmt.Sprintf("the shell could not be started: %v", err)
		}
		sess = started
	}

	ch, backlog, dropped := sess.attach()
	inner, off := context.WithCancel(ctx)
	out := &agentOutbox{slot: c.Slot}

	v := viewAgent(sess)
	sendAgentControl(ctx, high, agentControl{
		Type: "hello", Slot: c.Slot, ID: v.ID, Project: v.Project,
		Started: v.Started, Dropped: dropped, Where: string(sess.where),
	})
	if len(backlog) > 0 {
		out.add(inner, low, backlog)
	}

	go func() {
		for {
			select {
			case <-inner.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					sendAgentControl(ctx, high, agentControl{
						Type: "exited", Slot: c.Slot, Why: viewAgent(sess).Why,
					})
					return
				}
				out.add(inner, low, chunk)
			}
		}
	}()

	return &agentAttachment{sess: sess, ch: ch, off: off}, ""
}

// agentOutbox holds one terminal's bytes on their way to the socket.
//
// WHY IT OWNS A BUFFER INSTEAD OF DRAINING THE QUEUE. The first version, when
// the queue was full, drained `low` and merged what it found into one frame.
// That reads correctly and is a race: pumpByPriority is reading the same
// channel, so the two receivers take alternate items, and the merged frame goes
// to the BACK of a queue that already holds later ones - reordering a
// terminal's bytes. It survived a VM run and the drainer caught it.
//
// A producer may not reach into a queue somebody else is reading. So the bytes
// that will not fit are kept HERE, in the goroutine that made them, and offered
// again as one frame next time. Same bytes, same order, no shared mutation -
// and one goroutine per terminal means `pending` needs no lock.
type agentOutbox struct {
	slot    byte
	pending []byte
}

// add offers a chunk to the queue, keeping it for later if the queue is full.
func (o *agentOutbox) add(ctx context.Context, low chan []byte, chunk []byte) {
	o.pending = append(o.pending, chunk...)
	frame := make([]byte, 0, len(o.pending)+2)
	frame = append(frame, agentStreamOut, o.slot)
	frame = append(frame, o.pending...)
	select {
	case low <- frame:
		// Sent, so nothing is owed. A new slice rather than truncating the old
		// one: the frame just queued aliases it, and reusing the array would
		// rewrite bytes the send loop has not written yet.
		o.pending = nil
	case <-ctx.Done():
	default:
		// Still owed. It rides with the next chunk, which is what makes a burst
		// cost frames instead of the shell.
	}
}

func agentControlFrame(c agentControl) []byte {
	body, err := json.Marshal(c)
	if err != nil {
		return []byte{agentStreamControl, c.Slot, '{', '}'}
	}
	return append([]byte{agentStreamControl, c.Slot}, body...)
}

// sendAgentControl puts a control frame on the HIGH queue, so "control outranks
// output" holds for every one of them rather than only for those written before
// the queues existed.
func sendAgentControl(ctx context.Context, high chan []byte, c agentControl) {
	select {
	case high <- agentControlFrame(c):
	case <-ctx.Done():
	}
}

// newAgentID is the id a session is known by, on this node and in the browser
// that remembers it across a reload.
func newAgentID() string { return ulid.NewString() }

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

// pumpByPriority writes frames, and never lets a low one go first.
//
// THIS IS WHERE PRIORITY ACTUALLY LIVES, which is the whole of the operator's
// argument for one socket per session: "single websocket for a session with
// different streams... i.e. we can have priorities". A websocket is one ordered
// byte stream, so priority cannot be a property of the framing - it can only be
// a decision about what this node writes NEXT, and this is that decision.
//
// The high queue is offered on its own first, so a control frame never waits
// behind output that was queued before it. Only when high is empty does the
// second select consider low, and it still offers high alongside so a control
// frame arriving during the wait wins the race.
//
// IT IS A FUNCTION RATHER THAN INLINE SO THAT IT CAN BE ASSERTED. Written into
// the handler it was untestable without a VM, a browser and a websocket, which
// means the claim in the commit message would have been the only evidence for
// it. See TestControlOutranksOutput.
func pumpByPriority(ctx context.Context, high, low <-chan []byte, write func([]byte) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-high:
			if !write(frame) {
				return
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case frame := <-high:
			if !write(frame) {
				return
			}
		case frame := <-low:
			if !write(frame) {
				return
			}
		}
	}
}
