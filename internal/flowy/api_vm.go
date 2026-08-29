package flowy

// THE VMs, FROM THE CONSOLE'S SIDE.
//
//	GET  /api/vm/projects        what may be spawned over
//	GET  /api/vm/list            what is running
//	POST /api/vm/spawn           {project, prompt}
//	GET  /api/vm/{name}/log      what it has printed
//	POST /api/vm/{name}/say      {text} - another turn
//	POST /api/vm/{name}/down     stop it, work copied back out
//
// The operator's ask, 01M0G0KT52: "I want to be able to spawn agent right from
// flow - inside fc VM".
//
// NO RUNNER, AND THE TOPOLOGY IS WHY. My first design copied repro_proxy.go and
// put a second service behind a configured address. That was reasoning by
// analogy: the repro split exists because cmd/handoff-runner needs a Docker
// daemon, a source checkout and enough disk to build a database, and the node
// is meant to have none of them. Firecode is not that. It is already on this
// box - `ss` says this process listens on an address of the same host that
// holds the binary - so every verb here is one exec away.
//
// The difference matters because of what the repro panel became. It is built,
// correct, and does nothing, because the node was started without -repro and
// somebody has to know that. The operator's answer when told: "i dont care
// about any runners". A feature whose last mile is a flag the person who wanted
// it has never heard of is not delivered, and the console shows a panel that
// looks finished and is inert - the dead-button failure arriving through
// deployment rather than through code.
//
// So there is nothing to configure. The node asks whether `firecode` is on its
// PATH, at the moment it is asked. Presence is a question a process can answer
// about itself; an address is a question only a human can answer.
//
// WHEN IT IS ABSENT the answer is 503 with a sentence that names the binary,
// and it is deliberately NOT an empty list. "No VMs are running" and "this node
// cannot run VMs" are different facts, and a console that renders both as an
// empty page tells the operator the second one is the first. That collapse has
// produced five silent wrong answers in this project already.
//
// OPERATOR ONLY. Spawning a VM is starting a process on the host with a copy of
// a project tree in it, which is the widest thing this node can be asked to do.
// It goes through the same wrapper mint and the grant door use, rather than a
// check inside each handler, because a set of routes that all need one gate is
// a set where one of them eventually does not have it.
//
// NO SHELL, ANYWHERE. Every call is exec.CommandContext with an argument
// vector. A prompt is a sentence an operator typed and it reaches the guest as
// one argument, never as a word in a command line - a project name that happens
// to contain a backtick is a project name, not a command.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// firecodeBin is looked up per request rather than cached at startup.
//
// The node outlives an install. A cached "not found" from boot would answer
// long after somebody put the binary there, and it would answer confidently -
// which is the shape of every stale signal here: a reading from the past
// presented as a fact about now.
func firecodeBin() (string, error) {
	return exec.LookPath("firecode")
}

// runFirecode is the only way this file starts a process.
//
// The timeout is on the CONTEXT rather than on a timer the caller has to
// remember, so a wedged host cannot hold a request handler open forever. It is
// generous because these are VM operations and a stingy one would report a
// working spawn as a failure.
func runFirecode(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	bin, err := firecodeBin()
	if err != nil {
		return nil, errNoFirecode
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	return out, nil
}

// fctopBin resolves the dashboard the agents pane mirrors.
//
// A SEPARATE TOOL AND A SEPARATE ABSENCE. A host can have firecode and not
// fctop, so "I cannot run VMs" and "I cannot tell you how they are" are two
// different answers and neither may be reported as the other.
func fctopBin() (string, error) {
	return exec.LookPath("fctop")
}

// runFctop asks fctop for one frame.
//
// The timeout is short next to runFirecode's because fctop probes the guests IN
// PARALLEL - measured 2.3s for two VMs on the live fleet, fully probed. That is
// the reason this door can exist at all: /api/vm/list is deliberately unprobed
// because `firecode ps` costs 25s per guest in series, and that arithmetic does
// not apply here.
func runFctop(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	bin, err := fctopBin()
	if err != nil {
		return nil, errNoFctop
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	return out, nil
}

var errNoFctop = errors.New(
	"this node has no fctop on its PATH, so it cannot say how the VMs are. " +
		"That is not the same as there being none: the list is still on " +
		"/api/vm/list, without the readings")

var errNoFirecode = errors.New(
	"this node has no firecode on its PATH, so it cannot run VMs. " +
		"That is not the same as having none running: install firecode on " +
		"the machine serving this node, or open the VMs page on one that has it")

// writeFirecodeFailure keeps the two failures apart at the wire level.
//
// 503 means the capability is missing, 502 means the tool was there and did not
// answer. A caller that gets one status for both cannot tell "nothing to show"
// from "nothing works", which is precisely the distinction this door exists to
// preserve.
func writeFirecodeFailure(w http.ResponseWriter, err error, out []byte) {
	if errors.Is(err, errNoFctop) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoFctop.Error()))
		return
	}
	if errors.Is(err, errNoFirecode) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoFirecode.Error()))
		return
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	writeJSON(w, http.StatusBadGateway, errorBody("firecode refused: "+detail))
}

// passThroughJSON forwards what firecode already answered in JSON.
//
// Re-parsing into a struct here would mean two definitions of the same shape,
// and the one in this file would silently drop any field firecode learned to
// report. It is validated as JSON rather than trusted, so a tool that printed a
// warning onto stdout cannot become a body the console fails to parse with no
// explanation.
func passThroughJSON(w http.ResponseWriter, out []byte) {
	var probe any
	if err := json.Unmarshal(out, &probe); err != nil {
		writeJSON(w, http.StatusBadGateway,
			errorBody("firecode did not answer json: "+strings.TrimSpace(string(out))))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (s *server) handleVMProjects(w http.ResponseWriter, r *http.Request) {
	out, err := runFirecode(r.Context(), 15*time.Second, "projects", "--json")
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	passThroughJSON(w, out)
}

func (s *server) handleVMList(w http.ResponseWriter, r *http.Request) {
	// --json rather than `ps`, because `ps` probes every guest over vsock with
	// a 25s timeout each: ten VMs would be four minutes for one page refresh.
	// The roster it returns says "probed": false rather than implying it asked.
	out, err := runFirecode(r.Context(), 20*time.Second, "ps", "--json")
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	passThroughJSON(w, out)
}

// handleVMTop is the agents pane's table: every VM with how it is, and how much
// of that to believe.
//
// PASSED THROUGH, like its siblings, and for a stronger reason here. fctop's
// whole subject is the STATUS column - OK, ASKING, STALE 42s, SLOW, NO ANSWER,
// TIMEOUT, ERROR, GONE - and its own README says why: "A dashboard that keeps
// drawing the last number it saw, in the same colour it drew a fresh one, is
// worse than no dashboard." Re-deriving that here would mean a second opinion
// about staleness, and the two would drift.
func (s *server) handleVMTop(w http.ResponseWriter, r *http.Request) {
	out, err := runFctop(r.Context(), 45*time.Second, "--once", "--format", "json")
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	passThroughJSON(w, out)
}

type vmSpawnRequest struct {
	// Project is a NAME from /api/vm/projects, never a path. See the head
	// comment: a caller that can name a directory can pack any directory into
	// a VM that has the network.
	Project string `json:"project"`
	// Prompt is the agent's first turn. Optional: without one the VM comes up
	// and waits, which is `firecode up`; with one the agent runs unattended and
	// the VM powers off when it finishes.
	Prompt string `json:"prompt"`
}

func (s *server) handleVMSpawn(w http.ResponseWriter, r *http.Request) {
	var req vmSpawnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody(`say which project to spawn over: {"project": "flowy"}`))
		return
	}

	// The name is resolved against firecode's own registry before anything is
	// started, so an unknown project is refused HERE with the list that would
	// have worked - rather than becoming a VM booting over a directory nobody
	// meant. The registry is asked rather than read: two programs parsing one
	// config file disagree the day its shape changes.
	dir, known, err := s.resolveVMProject(r.Context(), name)
	if err != nil {
		writeFirecodeFailure(w, err, nil)
		return
	}
	if dir == "" {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"no project named "+name+" on this host. Registered: "+strings.Join(known, ", ")))
		return
	}

	args := []string{"claude", "--workdir", dir}
	if p := strings.TrimSpace(req.Prompt); p != "" {
		args = append(args, p)
	}

	// STARTED, NOT AWAITED. An agent run is minutes to hours; holding the
	// request open for it would tie the answer to the browser staying on the
	// page. The door's promise is "it started", and `GET /api/vm/list` is how
	// anybody learns what happened next - which is also why the response says
	// which project it started over rather than a bare ok.
	bin, err := firecodeBin()
	if err != nil {
		writeFirecodeFailure(w, errNoFirecode, nil)
		return
	}
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("could not start firecode: "+err.Error()))
		return
	}
	// Reaped in the background so the node does not accumulate zombies. The
	// result is deliberately dropped: this handler already answered, and the
	// run's own outcome belongs to `list` and `log`.
	go func() { _ = cmd.Wait() }()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"project": name, "workdir": dir, "started": true,
		"prompt_given": strings.TrimSpace(req.Prompt) != "",
	})
}

// resolveVMProject turns a name into a directory, and returns the names that
// would have worked when it cannot.
//
// The refusal carries the list because a door that says only "unknown project"
// makes the caller go and find the other door. It also reports a registered
// project whose directory has gone as unknown-with-a-reason rather than
// silently: firecode marks those `exists: false`.
func (s *server) resolveVMProject(ctx context.Context, name string) (string, []string, error) {
	out, err := runFirecode(ctx, 15*time.Second, "projects", "--json")
	if err != nil {
		return "", nil, err
	}
	var reg struct {
		Projects []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(out, &reg); err != nil {
		return "", nil, errors.New("firecode did not answer json")
	}
	known := make([]string, 0, len(reg.Projects))
	for _, p := range reg.Projects {
		if p.Exists {
			known = append(known, p.Name)
		}
		if p.Name == name && p.Exists {
			return p.Path, known, nil
		}
	}
	return "", known, nil
}

func (s *server) handleVMLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	out, err := runFirecode(r.Context(), 20*time.Second, "logs", name)
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	// Text, not JSON: this is console output and the console renders it as
	// such. Wrapping it in a field would make every reader unwrap it.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

type vmSayRequest struct {
	Text string `json:"text"`
}

func (s *server) handleVMSay(w http.ResponseWriter, r *http.Request) {
	var req vmSayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody(`say what: {"text": "..."}`))
		return
	}
	name := r.PathValue("name")
	out, err := runFirecode(r.Context(), 30*time.Second, "say", name, req.Text)
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vm": name, "said": true})
}

func (s *server) handleVMDown(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Longer than the others on purpose: `down` copies the work back out
	// before it returns, and cutting it short would be losing the run's
	// output to a timeout chosen for a list call.
	out, err := runFirecode(r.Context(), 3*time.Minute, "down", "--project", name)
	if err != nil {
		writeFirecodeFailure(w, err, out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vm": name, "stopped": true})
}

// handleShellSessions is the management half: what sessions this host holds and
// what is in them.
//
// EVERY SESSION, NOT ONLY FLOWY'S. The operator's own projectile/* sessions are
// the point of this - the panel attaches to the ones their editor uses - so a
// list filtered to what this node started would answer the wrong question. Each
// row says whether it follows the convention rather than hiding the rest.
//
// operatorOnly, like every /api/vm door: it names what is running on the
// machine serving this console, and the shells inside it are that machine's.
func (s *server) handleShellSessions(w http.ResponseWriter, r *http.Request) {
	list, err := listByobuSessions(r.Context())
	if err != nil {
		if errors.Is(err, errNoByobu) {
			// 503 AND A REASON, never 200 with an empty list. "no multiplexer
			// here" and "no sessions" are different facts and the second is a
			// perfectly good state - a caller that cannot tell them apart will
			// offer an attach button on a host that can never honour it.
			writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoByobu.Error()))
			return
		}
		writeJSON(w, http.StatusBadGateway, errorBody("tmux refused: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

type shellWindowRequest struct {
	// Session is a name from /api/shell/sessions, never a name typed here.
	Session string `json:"session"`
	// Where says what the window runs: "host" is a login shell, "vm" is a
	// firecode shell over Project. The same two words the panel already uses,
	// because they are the same choice - it has just stopped being a choice
	// about what KIND of session to make.
	Where string `json:"where"`
	// Project is a NAME from /api/vm/projects when Where is "vm". Resolved to a
	// directory here, for api_vm.go's standing reason: a caller that can name a
	// directory can pack any directory on this host into a VM with a network.
	Project string `json:"project"`
}

// handleShellWindow opens a window in a session that already exists.
//
// THE VM/HOST SELECTOR ENDS UP HERE. It used to decide what kind of session to
// start; under "all is byobu" there is one session per project and this decides
// what a new WINDOW in it runs. Both are reachable over ssh from the same
// `byobu attach`, and both switch with F3 and F4, which is where this began.
func (s *server) handleShellWindow(w http.ResponseWriter, r *http.Request) {
	var req shellWindowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}

	var command []string
	switch strings.TrimSpace(req.Where) {
	case "", string(shellOnHost):
		// Nothing: tmux opens the default shell, which is the person's own.
	case string(shellInGuest):
		bin, err := firecodeBin()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoFirecode.Error()))
			return
		}
		workdir, err := firecodeProjectPath(r.Context(), req.Project)
		if err != nil {
			writeFirecodeFailure(w, err, nil)
			return
		}
		// --no-tmux, and NOW it is unambiguous why: the window IS the wrapping,
		// so firecode wrapping itself again would put a second multiplexer
		// inside one this node already manages.
		command = []string{bin, "shell", "--no-tmux"}
		if workdir != "" {
			command = append(command, "--project", workdir)
		}
	default:
		writeJSON(w, http.StatusBadRequest, errorBody(`where is "vm" or "host"`))
		return
	}

	if err := openByobuWindow(r.Context(), req.Session, command); err != nil {
		if errors.Is(err, errNoByobu) {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoByobu.Error()))
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": req.Session})
}

type shellKillRequest struct {
	Session string `json:"session"`
	// Windows is how many the caller saw when it read the list. The guard, not
	// a detail: a list read ten seconds ago is not the state of the machine now,
	// and this is the only thing standing between a click and somebody's build.
	// Absent is refused rather than treated as zero - see killByobuSession.
	Windows *int `json:"windows"`
}

// handleShellKill ends a session and everything in it.
//
// THE MOST DESTRUCTIVE DOOR ON THIS NODE, and the only one that ends work
// belonging to a person rather than to flowy. projectile/* is where their
// editor lives; a session may hold a build, an agent, an ssh they walked away
// from. So the caller states what it believes it is ending and is refused if
// the session has changed since it looked.
func (s *server) handleShellKill(w http.ResponseWriter, r *http.Request) {
	var req shellKillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	// ABSENT IS NOT ZERO. A body with no `windows` is a caller that did not
	// read the session, and a zero-window session does not exist - so treating
	// the two the same would let an unchecked kill through as a valid claim.
	expect := -1
	if req.Windows != nil {
		expect = *req.Windows
	}
	if err := killByobuSession(r.Context(), req.Session, expect); err != nil {
		if errors.Is(err, errNoByobu) {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoByobu.Error()))
			return
		}
		// 409, not 400: the request was well formed and the world disagreed
		// with it. A caller that reads it again may well succeed, which is
		// exactly what a conflict means and what a bad request does not.
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ended": req.Session})
}
