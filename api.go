package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// maxBody caps a request body. Artifacts hold transcripts, so it is generous,
// but it is not unbounded.
const maxBody = 8 << 20

// artifactRequest is the create/update body. Project is raw JSON rather than a
// *string because absent and null mean different things here: absent means "my
// home project", null means "no project at all", which is what personal is.
type artifactRequest struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind"`
	Project    json.RawMessage `json:"project"`
	OwnerUser  string          `json:"owner_user"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Discovery  string          `json:"discovery"`
	Status     string          `json:"status"`
	Severity   string          `json:"severity"`
	Tags       []string        `json:"tags"`
	UserTags   []string        `json:"user_tags"`
	Related    []string        `json:"related"`
	Visibility string          `json:"visibility"`
	FilePath   string          `json:"file_path"`
	Fields     json.RawMessage `json:"fields"`
}

// decodeJSON reads a capped, strict JSON body. Unknown fields are an error:
// silently dropping a misspelled `visibilty` is how an artifact ends up
// readable by a project that was never meant to see it.
func decodeJSON(r *http.Request, into any) error {
	return decodeJSONLimit(r, into, maxBody)
}

// decodeJSONLimit is decodeJSON with a different cap, for the one endpoint that
// takes a page of rows rather than a single one.
func decodeJSONLimit(r *http.Request, into any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// handleCreateArtifact creates an artifact, or replaces one the principal owns.
//
// POST /api/artifacts
func (s *server) handleCreateArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req artifactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Type == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("type is required"))
		return
	}

	project, err := req.project(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	// An artifact is owned by whoever wrote it. The field is accepted so a
	// client can be explicit, not so it can put somebody else's name on a row -
	// that would let one user mint another user's personal artifacts.
	owner := req.OwnerUser
	if owner == "" {
		owner = p.UserID
	}
	if owner != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("owner_user must be the calling principal"))
		return
	}

	// An update is a replacement of a row that is already there, so it has to
	// clear the same bar twice: the principal must be able to see the artifact
	// at all, and must own it. Unreadable is 404 rather than 403, the same as a
	// plain read, so a probe cannot learn an id exists by trying to write it.
	if req.ID != "" {
		existing, err := s.db.ReadArtifact(r.Context(), p, req.ID, false)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Not there, or not ours to see. Either way this is a create with
			// a caller-chosen id, which is allowed - ids are minted anywhere.
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		case existing.OwnerUser != p.UserID:
			writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+req.ID))
			return
		default:
			// Fields the caller left out keep the value they had, so an update
			// does not have to restate the whole artifact.
			req.fillFrom(existing)
			if project, err = req.project(p); err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
				return
			}
		}
	}

	// You write where you are. A principal is a (user, agent, project) triple
	// and the project half of it is the only project it may put a row into:
	// writing into another one would produce an artifact its own author cannot
	// read back, because reads go by project and not by authorship. The same
	// person working in another project is another principal, with another
	// token.
	if project != nil && *project != p.Project {
		writeJSON(w, http.StatusForbidden,
			errorBody("cannot write into project "+*project+" as a principal of "+p.Project))
		return
	}

	art := &store.Artifact{
		ID:         req.ID,
		Type:       req.Type,
		Kind:       req.Kind,
		Project:    project,
		OwnerUser:  owner,
		Title:      req.Title,
		Body:       req.Body,
		Discovery:  req.Discovery,
		Status:     req.Status,
		Severity:   req.Severity,
		Tags:       req.Tags,
		UserTags:   req.UserTags,
		Related:    req.Related,
		Visibility: req.Visibility,
		FilePath:   req.FilePath,
		Fields:     req.Fields,
	}
	if err := s.db.UpsertArtifact(r.Context(), art); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, art)
}

// project resolves the three states of the project field: absent means the
// principal's home project, null means none, a string means that project.
func (req *artifactRequest) project(p *store.Principal) (*string, error) {
	raw := strings.TrimSpace(string(req.Project))
	switch {
	case raw == "":
		if req.Visibility == "personal" || p.Project == "" {
			return nil, nil
		}
		home := p.Project
		return &home, nil
	case raw == "null":
		return nil, nil
	}
	var name string
	if err := json.Unmarshal(req.Project, &name); err != nil {
		return nil, errors.New("project must be a string or null")
	}
	if name == "" {
		return nil, nil
	}
	return &name, nil
}

// fillFrom carries forward the parts of an existing artifact the update left
// unset.
func (req *artifactRequest) fillFrom(old *store.Artifact) {
	setIfEmpty := func(dst *string, old string) {
		if *dst == "" {
			*dst = old
		}
	}
	setIfEmpty(&req.Type, old.Type)
	setIfEmpty(&req.Kind, old.Kind)
	setIfEmpty(&req.Title, old.Title)
	setIfEmpty(&req.Body, old.Body)
	setIfEmpty(&req.Discovery, old.Discovery)
	setIfEmpty(&req.Status, old.Status)
	setIfEmpty(&req.Severity, old.Severity)
	setIfEmpty(&req.Visibility, old.Visibility)
	setIfEmpty(&req.FilePath, old.FilePath)
	if req.Tags == nil {
		req.Tags = old.Tags
	}
	if req.UserTags == nil {
		req.UserTags = old.UserTags
	}
	if req.Related == nil {
		req.Related = old.Related
	}
	if len(req.Fields) == 0 {
		req.Fields = old.Fields
	}
	if len(req.Project) == 0 && old.Project != nil {
		req.Project, _ = json.Marshal(*old.Project)
	}
}

// handleListArtifacts lists what the principal may read.
//
// GET /api/artifacts?type=&project=&status=
func (s *server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	list, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type:     q.Get("type"),
		Kind:     q.Get("kind"),
		Project:  q.Get("project"),
		Status:   q.Get("status"),
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": list})
}

// handleGetArtifact returns one artifact, or 404 when it is missing or out of
// reach - the caller cannot tell which, which is the point.
//
// GET /api/artifact/{id}
func (s *server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, err := s.db.ReadArtifact(r.Context(), p, r.PathValue("id"), scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, art)
}

// handleDeleteArtifact tombstones an artifact: the row stays, marked, with a
// fresh clock reading, so the delete can replicate as a fact rather than as an
// absence.
//
// POST /api/artifact/{id}/delete
func (s *server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")

	art, err := s.db.ReadArtifact(r.Context(), p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	if art.OwnerUser != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+id))
		return
	}

	art, err = s.db.TombstoneArtifact(r.Context(), p, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, art)
}

// handleSearch ranks the artifacts the principal may read against a free text
// query. The match covers title, body, discovery and tags, so a word an agent
// only ever wrote in the discovery still finds the artifact.
//
// GET /api/search?q=&type=&project=
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	query := q.Get("q")

	hits, err := s.db.SearchArtifacts(r.Context(), p, store.ArtifactQuery{
		Query:    query,
		Type:     q.Get("type"),
		Kind:     q.Get("kind"),
		Project:  q.Get("project"),
		Status:   q.Get("status"),
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "artifacts": hits})
}

// eventRequest is the append body. There is no project field: an event lands in
// the principal's home project, because that is the only project it is entitled
// to write into.
type eventRequest struct {
	Type     string          `json:"type"`
	Room     string          `json:"room"`
	Thread   string          `json:"thread"`
	Parents  []string        `json:"parents"`
	Actor    string          `json:"actor"`
	Artifact string          `json:"artifact"`
	Body     string          `json:"body"`
	Meta     json.RawMessage `json:"meta"`
}

// handleAppendEvent appends to the log. The log is append-only: there is no
// update and no delete, and the DAG is carried in parents rather than in the
// order rows happen to land.
//
// POST /api/events
func (s *server) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req eventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Type == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("type is required"))
		return
	}

	actor := req.Actor
	if actor == "" {
		// An agent acting on its own behalf is the actor; otherwise the user is.
		if actor = p.AgentID; actor == "" {
			actor = p.UserID
		}
	}
	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}
	if req.Parents == nil {
		req.Parents = []string{}
	}

	e := &store.Event{
		Type:     req.Type,
		Project:  project,
		Room:     req.Room,
		Thread:   req.Thread,
		Parents:  req.Parents,
		Actor:    actor,
		Artifact: req.Artifact,
		Body:     req.Body,
		Meta:     req.Meta,
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// handleListEvents reads the log in order, filtered to what the principal may
// see.
//
// GET /api/events?thread=&since=
func (s *server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	since, err := strconv.ParseInt(orZero(q.Get("since")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("since must be a packed hlc integer"))
		return
	}

	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Thread:   q.Get("thread"),
		Room:     q.Get("room"),
		Type:     q.Get("type"),
		Since:    since,
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list})
}

// grantRequest is the body of a grant. Leave artifact empty for a project-wide
// grant, or name one artifact - with the user it is shared to - for a share.
type grantRequest struct {
	FromProject string `json:"from_project"`
	ToProject   string `json:"to_project"`
	Subject     string `json:"subject"`
	Artifact    string `json:"artifact"`
	Cap         string `json:"cap"`
}

// handleCreateGrant issues a capability.
//
// You can only give away what is yours: a project-wide grant has to be written
// by a principal of the project being opened up (to_project), and a share has
// to be written by the artifact's owner. Personal artifacts cannot be shared at
// all - the floor holds in the store regardless, so this is only here to fail
// loudly rather than quietly.
//
// POST /api/grants
func (s *server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req grantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}

	if req.Artifact != "" {
		art, err := s.db.ReadArtifact(r.Context(), p, req.Artifact, false)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		if art.OwnerUser != p.UserID {
			writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+req.Artifact))
			return
		}
		if art.Visibility == "personal" || art.Project == nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("a personal artifact cannot be shared; give it a project first"))
			return
		}
		if req.Subject == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("subject is required for a share"))
			return
		}
		if req.ToProject == "" {
			req.ToProject = *art.Project
		}
	} else {
		if req.FromProject == "" || req.ToProject == "" {
			writeJSON(w, http.StatusBadRequest,
				errorBody("from_project and to_project are required for a project grant"))
			return
		}
		if req.ToProject != p.Project {
			writeJSON(w, http.StatusForbidden,
				errorBody("only a principal of "+req.ToProject+" can open it up"))
			return
		}
	}

	g := &store.Grant{
		FromProject: req.FromProject,
		ToProject:   req.ToProject,
		Subject:     req.Subject,
		Artifact:    req.Artifact,
		Cap:         req.Cap,
		GrantedBy:   p.UserID,
	}
	if err := s.db.InsertGrant(r.Context(), g); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleWhoami echoes the principal a token resolved to, which is the quickest
// way to find out why a read came back empty.
//
// GET /api/whoami
func (s *server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalOf(r))
}

// intParam parses an optional positive integer parameter, treating anything
// unparseable as absent.
func intParam(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
