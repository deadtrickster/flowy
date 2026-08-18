package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
)

// service is what the handlers need: the store they read findings out of and
// write verdicts into, the configuration that says which projects exist, the
// version resolver, and the run queue.
type service struct {
	db       *store.DB
	cfg      *Config
	resolver *repro.Resolver
	queue    runQueue
	started  time.Time
}

// fault is a refusal with a status on it - the shape every handler below
// returns instead of writing a response itself, so that what a refusal SAYS
// and what it is are decided in one place and can be tested without a socket.
type fault struct {
	code int
	msg  string
}

func (f fault) Error() string { return f.msg }

// refuse makes one.
func refuse(code int, msg string) error { return fault{code: code, msg: msg} }

// routes is the whole HTTP surface: five routes, and every one of them
// behind the same authentication.
//
// THIS IS NOT REGISTERED IN THE FLOWY SERVER, AND THAT IS THE POINT. Flowy
// proper is meant to run locked down - the deployment the Python service
// shipped drops every capability, mounts its filesystem read-only and is
// given no Docker socket at all. This process is the opposite: it needs a
// live Docker daemon and a source checkout on disk, because it is where the
// repro actually runs. One migration, two deployables, and this file is the
// boundary between them. See boundary_test.go, which fails if the two ever
// become one process.
func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /run", s.authed(s.handleRun))
	mux.Handle("GET /runs", s.authed(s.handleRuns))
	mux.Handle("GET /run/{id}/log", s.authed(s.handleRunLog))
	mux.Handle("GET /package", s.authed(s.handlePackage))
	mux.Handle("GET /version", s.authed(s.handleVersion))
	// /healthz is the one door with no token on it, and it answers with
	// nothing but liveness: a health check that needed a credential is a
	// health check that reports the node down when the credential is wrong.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return logRequests(mux)
}

// handler is a handler that may refuse. It is given the principal rather
// than fetching one, so that no handler here can be written that forgets to.
type handler func(w http.ResponseWriter, r *http.Request, p *store.Principal) error

// authed resolves the bearer token to a principal against the same Postgres
// the Flowy node uses, and refuses the request when it cannot.
//
// THE TOKEN IS THE CALLER'S OWN, never this process's. That is what makes
// the privilege boundary a real one: this binary holds Docker and a
// checkout, and it hands neither to anybody - what it hands back is what the
// caller's own principal could already read out of the store, plus the
// result of having executed something. A run enqueued through this door is
// recorded against the principal that asked for it, so the finding's run log
// names a person or an agent rather than a daemon.
//
// EVERY ROUTE IS BEHIND IT, INCLUDING THE TWO READ-ONLY ONES. The Python
// service left /api/package and /api/version open on the grounds that they
// only read; that reasoning does not survive the move. A package contains
// the finding's repro tree verbatim, and a finding here can be private -
// so serving one to an unauthenticated caller would publish exactly the
// artifact whose visibility the store is careful about.
func (s *service) authed(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeFault(w, r, refuse(http.StatusUnauthorized, "missing bearer token"))
			return
		}
		p, err := s.db.PrincipalForToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			writeFault(w, r, refuse(http.StatusUnauthorized, "unknown token"))
			return
		}
		if err != nil {
			writeFault(w, r, err)
			return
		}
		if p.UserID == "" && p.AgentID == "" {
			writeFault(w, r, refuse(http.StatusUnauthorized, "token resolves to no principal"))
			return
		}
		if err := h(w, r, p); err != nil {
			writeFault(w, r, err)
		}
	})
}

// bearerToken pulls the token out of the Authorization header. The same four
// lines as the node's own auth.go, and deliberately not shared with it: this
// binary does not import the server package, because importing it is how a
// second deployable quietly becomes the first one again.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// runRequest is POST /run's body. Both spellings are accepted because both
// are natural: a console reruns one finding, a sweep reruns a list.
type runRequest struct {
	Finding  string   `json:"finding"`
	Findings []string `json:"findings"`
	Version  string   `json:"version"`
}

// handleRun queues one run per finding named, and answers with what it
// queued AND what it refused.
//
// A PARTIAL ANSWER IS THE HONEST ONE. Handing over five findings where one
// has no repro tree should not fail the other four, and it must not silently
// drop the one either - a caller who asked for five runs and reads back four
// with no explanation has been told the wrong thing about their fifth
// finding.
func (s *service) handleRun(w http.ResponseWriter, r *http.Request, p *store.Principal) error {
	var req runRequest
	if err := decodeJSON(r, &req); err != nil {
		return refuse(http.StatusBadRequest, "bad request body: "+err.Error())
	}
	findings := req.Findings
	if req.Finding != "" {
		findings = append([]string{req.Finding}, findings...)
	}
	if len(findings) == 0 {
		return refuse(http.StatusBadRequest, "name a finding to run: {\"finding\": \"<id>\"}")
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = "latest"
	}
	if err := checkVersion(version); err != nil {
		return err
	}

	type queued struct {
		Run     string `json:"run"`
		Finding string `json:"finding"`
	}
	type refused struct {
		Finding string `json:"finding"`
		Error   string `json:"error"`
	}
	out := struct {
		Queued  []queued  `json:"queued"`
		Refused []refused `json:"refused,omitempty"`
		Version string    `json:"version"`
	}{Queued: []queued{}, Version: version}

	for _, id := range findings {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		// THE CALLER'S OWN REACH IS CHECKED HERE, BEFORE THE QUEUE IS TOLD
		// ANYTHING. The runner behind the port holds one principal of its
		// own - it is a daemon, and its reads happen minutes after the
		// request that caused them - so it cannot be the thing that decides
		// whether this caller was allowed to ask. If the door did not ask,
		// authenticating at this port would be a way to run, and to learn
		// the outcome of running, a finding you cannot read through Flowy;
		// the extra privilege this process holds is execution, and it must
		// not become a wider view of the store.
		//
		// The refusal is the store's own: out of reach and not there are one
		// answer.
		if !s.mayRead(r.Context(), p, id) {
			out.Refused = append(out.Refused, refused{
				Finding: id, Error: "no such finding: " + id})
			continue
		}
		runID, err := s.queue.Enqueue(r.Context(), p, id, version)
		if err != nil {
			if errors.Is(err, errQueueUnlinked) {
				return refuse(http.StatusServiceUnavailable, err.Error())
			}
			out.Refused = append(out.Refused, refused{Finding: id, Error: faultMessage(err)})
			continue
		}
		out.Queued = append(out.Queued, queued{Run: runID, Finding: id})
	}
	if len(out.Queued) == 0 {
		writeJSON(w, http.StatusBadRequest, out)
		return nil
	}
	writeJSON(w, http.StatusAccepted, out)
	return nil
}

// handleRuns lists the runs this process knows about, FILTERED TO THE ONES
// WHOSE FINDING THE CALLER MAY READ.
//
// The queue itself holds no permission filter - it is a list in memory, and
// a list in memory cannot answer "who may see this". So the filter is
// applied here, by asking the store the only question that decides it: can
// this principal read that finding. Without it, a run list would leak the
// ids, projects and verdicts of findings the caller cannot open, which is
// the same disclosure the store spends its filter preventing.
func (s *service) handleRuns(w http.ResponseWriter, r *http.Request, p *store.Principal) error {
	runs := s.queue.Runs()
	visible := make([]Run, 0, len(runs))
	// One answer per finding, not per run: a sweep queues many runs of the
	// same finding, and asking the store the same question twenty times to
	// get twenty identical answers is twenty queries.
	decided := map[string]bool{}
	for _, run := range runs {
		may, asked := decided[run.Finding]
		if !asked {
			may = s.mayRead(r.Context(), p, run.Finding)
			decided[run.Finding] = may
		}
		if may {
			visible = append(visible, run)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": visible, "linked": linked(s.queue)})
	return nil
}

// handleRunLog streams one run's log as plain text.
func (s *service) handleRunLog(w http.ResponseWriter, r *http.Request, p *store.Principal) error {
	id := r.PathValue("id")
	run, ok := s.queue.Run(id)
	if !ok {
		if !linked(s.queue) {
			return refuse(http.StatusServiceUnavailable, errQueueUnlinked.Error())
		}
		return refuse(http.StatusNotFound, "no such run: "+id)
	}
	// The same permission question the list asks, asked again rather than
	// assumed: a caller who guessed a run id must not read the log of a
	// finding they cannot open, and a log is the most revealing thing here.
	if !s.mayRead(r.Context(), p, run.Finding) {
		return refuse(http.StatusNotFound, "no such run: "+id)
	}
	f, err := s.openRunLog(run)
	if err != nil {
		return err
	}
	if f == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "(no log yet)")
		return nil
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
	return nil
}

// openRunLog opens a run's log file, or returns nil when it has not been
// written yet.
//
// It re-checks that the path is under LogDir even though the path came off
// this process's own record and not out of the request. The check costs
// nothing and it is the difference between "no handler can serve an
// arbitrary file" being true by construction and being true because of what
// some other file does today.
func (s *service) openRunLog(run Run) (*os.File, error) {
	if run.LogPath == "" {
		return nil, nil
	}
	root, err := filepath.Abs(s.cfg.LogDir)
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(run.LogPath)
	if err != nil {
		return nil, err
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return nil, fmt.Errorf("run %s: log path %s is outside %s", run.ID, path, root)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// handlePackage builds and streams the self-contained reproduction package
// for one finding at one version.
//
// IT NEVER SHIPS A BINARY. BuildPackage renders exactly the Dockerfile, the
// compose file, the README, the manifest and the repro tree - the artifact
// that goes to the project the finding is about. The binary only ever enters
// the staged copy a local run uses, which lives on this host and is not
// served by any route here.
func (s *service) handlePackage(w http.ResponseWriter, r *http.Request, p *store.Principal) error {
	finding := strings.TrimSpace(r.URL.Query().Get("finding"))
	if finding == "" {
		return refuse(http.StatusBadRequest, "name a finding: /package?finding=<id>")
	}
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if version == "" {
		version = "latest"
	}
	in, err := s.renderInput(r.Context(), p, finding, version)
	if err != nil {
		return err
	}
	res, err := repro.BuildPackage(s.packageDir(in.Finding.Project), in)
	if err != nil {
		return err
	}
	f, err := os.Open(res.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename="+quoteFilename(res.Name))
	// Never cache: the name is stable across a no-op rebuild by design, so a
	// cached copy would keep serving an old package under a name that is
	// still correct. The Python service learned this the same way.
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = io.Copy(w, f)
	return nil
}

// versionAnswer is what GET /version says.
//
// NOTE WHAT IS NOT IN IT: the path of the binary on this host. Version.Binary
// is a local filesystem path, and this process's filesystem is not the
// caller's business - what a caller needs to know is whether a binary for
// this commit is ALREADY there, because that is the difference between a run
// that starts now and a run that compiles a database first. So the fact is
// reported and the path is not.
type versionAnswer struct {
	Project     string `json:"project"`
	Requested   string `json:"requested"`
	SHA         string `json:"sha"`
	Image       string `json:"image"`
	BinaryReady bool   `json:"binary_ready"`
	Buildable   bool   `json:"buildable"`
	SourceBuild bool   `json:"source_build"`
	Note        string `json:"note"`
	// Runnable is whether this deployment can actually run the version it
	// just resolved, rather than only describe or package it.
	Runnable bool `json:"runnable"`
}

// handleVersion resolves a version string against one project without
// running anything - what a console asks before it offers a rerun.
func (s *service) handleVersion(w http.ResponseWriter, r *http.Request, _ *store.Principal) error {
	q := r.URL.Query()
	project := strings.TrimSpace(q.Get("project"))
	if project == "" && len(s.cfg.Projects) == 1 {
		project = s.cfg.ProjectNames()[0]
	}
	if project == "" {
		return refuse(http.StatusBadRequest, "name a project: /version?project=<name>&v=<version> - "+
			"this runner holds "+strings.Join(s.cfg.ProjectNames(), ", "))
	}
	cfg, ok := s.cfg.ProjectConfig(project)
	if !ok {
		return refuse(http.StatusNotFound, fmt.Sprintf(
			"this runner is not configured for project %q - it holds %s",
			project, strings.Join(s.cfg.ProjectNames(), ", ")))
	}
	version := strings.TrimSpace(q.Get("v"))
	if version == "" {
		version = strings.TrimSpace(q.Get("version"))
	}
	if version == "" {
		version = "latest"
	}
	if err := checkVersion(version); err != nil {
		return err
	}
	// Resolving "latest" fetches, and a release pulls an image: both are
	// slow enough that a caller who hangs up should stop the work, which is
	// what the request's own context does.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	v := s.resolver.Resolve(ctx, cfg, version)
	writeJSON(w, http.StatusOK, versionAnswer{
		Project:     project,
		Requested:   version,
		SHA:         v.SHA,
		Image:       v.Image,
		BinaryReady: v.Binary != "",
		Buildable:   v.Buildable,
		SourceBuild: v.SourceBuild,
		Note:        v.Note,
		Runnable:    linked(s.queue),
	})
	return nil
}

// handleHealthz says the process is up and whether it can reach the store.
func (s *service) handleHealthz(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"ok":       true,
		"version":  version,
		"uptime_s": int64(time.Since(s.started).Seconds()),
		"projects": len(s.cfg.Projects),
		"runnable": linked(s.queue),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		body["ok"] = false
		body["store"] = "unreachable"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// mayRead is the one permission question this binary asks on its own behalf:
// may this principal read that finding. It is answered by the store's own
// filtered read and never by anything held here.
func (s *service) mayRead(ctx context.Context, p *store.Principal, findingID string) bool {
	if findingID == "" {
		return false
	}
	art, err := s.db.ReadArtifact(ctx, p, findingID, false)
	return err == nil && art.Type == findingType
}

// packageDir is where a project's built packages land.
func (s *service) packageDir(project string) string {
	if p, ok := s.cfg.Projects[project]; ok && p.PackageDir != "" {
		return p.PackageDir
	}
	return filepath.Join(s.cfg.CacheDir, project, "packages")
}

// writeFault turns an error from a handler into a response.
//
// A store error that means "not here, or not yours" comes back as 404 and
// says nothing else: the store answers a read the caller may not make and a
// read of something that does not exist identically, on purpose, and a door
// that split them again would undo that.
func writeFault(w http.ResponseWriter, r *http.Request, err error) {
	var f fault
	if errors.As(err, &f) {
		writeJSON(w, f.code, map[string]string{"error": f.msg})
		return
	}
	var notFinding store.NotAFindingError
	if errors.As(err, &notFinding) || errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, store.ErrNoBytes) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	ref := errorRef()
	log.Printf("500 %s %s ref=%s: %v", r.Method, r.URL.Path, ref, err)
	writeJSON(w, http.StatusInternalServerError,
		map[string]string{"error": "internal error", "ref": ref})
}

// faultMessage is what one entry of a partial answer says went wrong. It
// carries the refusal's own words for a refusal, and nothing at all for an
// internal failure - the same split writeFault makes, because a per-finding
// error in a 202 body is as much a response as a 500 is.
func faultMessage(err error) string {
	var f fault
	if errors.As(err, &f) {
		return f.msg
	}
	var notFinding store.NotAFindingError
	if errors.As(err, &notFinding) || errors.Is(err, store.ErrNotFound) {
		return err.Error()
	}
	return "internal error"
}

// decodeJSON reads a request body into v, refusing anything it does not
// recognise rather than ignoring it - a misspelled "version" that is
// silently dropped is a run against the wrong commit.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// quoteFilename wraps a package name for Content-Disposition. The name comes
// out of the packager and is already slug-shaped, so the quoting is belt and
// braces against a header that could otherwise be split.
func quoteFilename(name string) string {
	name = strings.NewReplacer("\"", "", "\r", "", "\n", "", ";", "").Replace(name)
	return "\"" + name + "\""
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

// logRequests logs one line per request, the same shape the node's own log
// has, so two deployables of one system read as one log when they are
// collected together.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status,
			time.Since(start).Round(time.Microsecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
