package flowy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// Announcements and the quiesce protocol.
//
// An announcement is how a node tells the people and the agents working on it
// that something is changing: a release is going out, a project is being
// archived, a store is about to be taken down. Three things make it worth
// having its own endpoints rather than being a note somebody writes in the
// room:
//
//   - it is scoped. node scope stays here, project scope reaches the project,
//     federation scope travels the fabric. The first of those is enforced at
//     the replication doors and not by convention - see store.ReplicableArtifactSQL.
//   - it is capability-gated. Only a system or monitor agent may post
//     federation scope, because that is the one message on this node that one
//     process says and every node then shows to everybody. A worker agent is
//     refused, and so is a person holding their own token: this is machine
//     traffic.
//   - it can hold something. A maintenance or breaking announcement may name a
//     resource and a mode, and then the change does not proceed until the
//     dependents holding that resource have drained or acknowledged it. That is
//     the quiesce protocol, and the enforcement is that resolving the
//     announcement - saying the change is done - is refused while the quiesce
//     is held.
//
// Everything else about an announcement is what it already is: an artifact,
// with the artifact's signature, the artifact's permission filter and the
// artifact's merge. A forged federation announcement fails on the merge for
// exactly the reason a forged bug does.

// announcementRequest is POST /api/announcements.
type announcementRequest struct {
	Scope    string `json:"scope"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Resource string `json:"resource"`
	Mode     string `json:"mode"`
}

// holdRequest is POST /api/quiesce/hold and POST /api/quiesce/release.
type holdRequest struct {
	Resource string `json:"resource"`
}

// handleCreateAnnouncement posts an announcement.
//
// POST /api/announcements
func (s *server) handleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req announcementRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	req.Scope = strings.TrimSpace(req.Scope)
	req.Severity = strings.TrimSpace(req.Severity)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Resource = strings.TrimSpace(req.Resource)

	if req.Scope == "" {
		req.Scope = store.ScopeProject
	}
	if req.Severity == "" {
		req.Severity = store.SeverityInfo
	}
	if !store.ScopeOK(req.Scope) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("scope must be node, project or federation"))
		return
	}
	if !store.SeverityOK(req.Severity) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("severity must be info, warning, maintenance or breaking"))
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("title is required"))
		return
	}
	// An announcement is owned by whoever posted it, and resolving it is the
	// owner's. A token that resolves to no person owns nothing, so there would
	// be nobody who could ever close the window it opened.
	if p.UserID == "" {
		writeJSON(w, http.StatusForbidden,
			errorBody("this token names no user, and an announcement has an owner"))
		return
	}

	// The capability. It is asked of the principal's agent kind and of nothing
	// else - not of the operator flag, because an operator posting by hand is a
	// person, and not of the project, because a federation announcement is not
	// a property of where it was written.
	if req.Scope == store.ScopeFederation && !store.MayAnnounceFederation(p.AgentKind) {
		writeJSON(w, http.StatusForbidden, errorBody(
			"a federation-scope announcement is posted by a system or monitor agent; "+
				"this token is "+describeKind(p)))
		return
	}

	// A quiesce is a property of a change, so only the two severities that are
	// a change may carry one. A notice that held a resource would be a way to
	// stop other people's work by describing it.
	switch {
	case req.Resource == "" && req.Mode != "":
		writeJSON(w, http.StatusBadRequest,
			errorBody("mode names how to quiesce a resource, so it needs a resource"))
		return
	case req.Resource != "" && !store.QuiesceSeverity(req.Severity):
		writeJSON(w, http.StatusBadRequest, errorBody(
			"only a maintenance or breaking announcement quiesces a resource"))
		return
	case req.Resource != "":
		if req.Mode == "" {
			req.Mode = store.ModeDrain
		}
		if !store.ModeOK(req.Mode) {
			writeJSON(w, http.StatusBadRequest,
				errorBody("mode must be drain, pause or ack-required"))
			return
		}
	}

	fields, err := store.AnnouncementFields{
		Scope: req.Scope, Resource: req.Resource, Mode: req.Mode,
	}.Encode()
	if err != nil {
		serverError(w, r, err)
		return
	}

	// An announcement lands in the principal's own project and is visible to
	// it, exactly like anything else the principal writes. A node-scope one is
	// no different: what makes it node-scope is that it does not replicate, not
	// that it is readable by people the rest of this node is not.
	var project *string
	visibility := store.VisibilityPersonal
	if p.Project != "" {
		home := p.Project
		project, visibility = &home, store.VisibilityShared
	}

	art := &store.Artifact{
		Type: store.AnnouncementType,
		// kind carries the scope too, so "every federation announcement" is a
		// column read rather than a jsonb one. It is a copy for querying and
		// nothing decides anything by it - store.AnnouncementScope reads
		// fields, and that is the one the doors ask. Both are inside the row's
		// signature, so a relay cannot move them apart.
		Kind:       req.Scope,
		Project:    project,
		OwnerUser:  p.UserID,
		Title:      req.Title,
		Body:       req.Body,
		Status:     store.AnnouncementActive,
		Severity:   req.Severity,
		Visibility: visibility,
		Fields:     fields,
	}
	event, err := s.announcementEvent(r, "posted", req.Scope, req.Severity, req.Resource, req.Mode)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.WriteAnnouncement(r.Context(), art, event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcement": art, "event": event})
}

// describeKind says what a token is, for the refusal above. A person holding
// their own credential is not an agent of any kind, and saying "worker" at them
// would be describing a thing they do not have.
func describeKind(p *store.Principal) string {
	if p.AgentID == "" {
		return "a person, not an agent"
	}
	kind := p.AgentKind
	if kind == "" {
		kind = store.AgentKindWorker
	}
	return "a " + kind + " agent"
}

// handleListAnnouncements is what the console banner reads: the active
// announcements this principal may see, worst first.
//
// GET /api/announcements[?status=resolved|all]
func (s *server) handleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	want := strings.TrimSpace(r.URL.Query().Get("status"))

	if want == "" || want == store.AnnouncementActive {
		list, err := s.db.ActiveAnnouncements(r.Context(), p, scopeAll(r, p))
		if err != nil {
			serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"announcements": announcementViews(p, list),
		})
		return
	}

	q := store.ArtifactQuery{Type: store.AnnouncementType, ScopeAll: scopeAll(r, p)}
	if want != "all" {
		q.Status = want
	}
	list, err := s.db.ListArtifacts(r.Context(), p, q)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": announcementViews(p, list)})
}

// readAnnouncement reads one announcement, answering the request itself when it
// is not there, not readable, or not an announcement at all.
func (s *server) readAnnouncement(
	w http.ResponseWriter, r *http.Request,
) (*store.Artifact, bool) {
	p := principalOf(r)
	art, err := s.db.ReadArtifact(r.Context(), p, r.PathValue("id"), scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such announcement"))
		return nil, false
	}
	if err != nil {
		serverError(w, r, err)
		return nil, false
	}
	if art.Type != store.AnnouncementType {
		writeJSON(w, http.StatusNotFound, errorBody("no such announcement"))
		return nil, false
	}
	return art, true
}

// handleQuiesce reports where an announcement's quiesce has got to.
//
// GET /api/announcement/{id}/quiesce
func (s *server) handleQuiesce(w http.ResponseWriter, r *http.Request) {
	art, ok := s.readAnnouncement(w, r)
	if !ok {
		return
	}
	q, err := s.db.QuiesceOf(r.Context(), art)
	if errors.Is(err, store.ErrNoQuiesce) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("announcement "+art.ID+" names no resource, so nothing is quiescing"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

// handleHoldResource records that this principal depends on a resource, so that
// a maintenance announcement naming it knows who it is waiting for.
//
// POST /api/quiesce/hold
func (s *server) handleHoldResource(w http.ResponseWriter, r *http.Request) {
	s.quiesceMark(w, r, store.EventQuiesceHold, "holding")
}

// handleReleaseResource is the other end: this principal has let the resource
// go. Under drain and pause that is the answer the announcement asked for;
// under ack-required it is not, and the ack still has to be written.
//
// POST /api/quiesce/release
func (s *server) handleReleaseResource(w http.ResponseWriter, r *http.Request) {
	s.quiesceMark(w, r, store.EventQuiesceRelease, "released")
}

// quiesceMark writes one of the two hold events.
func (s *server) quiesceMark(w http.ResponseWriter, r *http.Request, eventType, said string) {
	p := principalOf(r)

	var req holdRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	resource := strings.TrimSpace(req.Resource)
	if resource == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("resource is required"))
		return
	}

	actor, kind := chatActor(p)
	meta, err := json.Marshal(map[string]string{
		"resource": resource, "actor_kind": kind, "actor_user": p.UserID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	event := &store.Event{
		Type:    eventType,
		Project: homeProject(p),
		Room:    store.QuiesceRoom,
		Parents: []string{},
		Actor:   actor,
		Body:    said + " " + resource,
		Meta:    meta,
	}
	if err := s.db.AppendEvent(r.Context(), event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": resource, "event": event})
}

// handleAckAnnouncement is a dependent saying it has seen the announcement and
// is out of the way.
//
// POST /api/announcement/{id}/ack
func (s *server) handleAckAnnouncement(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, ok := s.readAnnouncement(w, r)
	if !ok {
		return
	}
	f := store.DecodeAnnouncementFields(art.Fields)
	if f.Resource == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("announcement "+art.ID+" names no resource, so there is nothing to ack"))
		return
	}

	actor, kind := chatActor(p)
	meta, err := json.Marshal(map[string]string{
		"resource": f.Resource, "mode": f.Mode, "actor_kind": kind, "actor_user": p.UserID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The ack lands in the announcement's project rather than the actor's, and
	// names the announcement: it is a fact about that row, and a quiesce read
	// from either side has to see the same set of acks.
	event := &store.Event{
		Type:     store.EventQuiesceAck,
		Project:  art.Project,
		Room:     store.QuiesceRoom,
		Parents:  []string{},
		Actor:    actor,
		Artifact: art.ID,
		Body:     "acked " + f.Resource,
		Meta:     meta,
	}
	if err := s.db.AppendEvent(r.Context(), event); err != nil {
		serverError(w, r, err)
		return
	}

	q, err := s.db.QuiesceOf(r.Context(), art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quiesce": q, "event": event})
}

// mayResolve says whether this principal may close an announcement's window.
//
// THE OWNER, because an announcement is somebody's statement and a stranger
// silencing it is the failure an owner rule prevents. That stays.
//
// AND THIS NODE'S OPERATOR, on a node-scope announcement, which is the half
// that was missing. Measured 2026-08-20: the land-guard bypass at 01:19 sat as
// an active severity=warning banner on every page for four hours, and the
// operator - whose node it is, and who was the one looking at it - got 403 from
// this door. The author was an agent, and an agent is not always running; a
// node-wide banner whose only key is held by a process that may be gone is a
// banner that stays up.
//
// It is not "anybody who can read it". For a shared announcement that is
// everybody, and then the owner rule means nothing. Scope is what makes the
// operator not a stranger here: a scope=node announcement is a statement ABOUT
// this node, on every reader's screen, and the operator is the person answerable
// for it. A project-scope one is still its author's.
func mayResolve(p *store.Principal, a *store.Artifact) bool {
	if p == nil {
		return false
	}
	if a.OwnerUser == p.UserID {
		return true
	}
	return p.Operator && store.IsLocalAnnouncement(a)
}

// resolveRefusal says which of the two ways in the caller has neither, rather
// than "not the owner" at an operator who is not trying to be one.
func resolveRefusal(a *store.Artifact) string {
	if store.IsLocalAnnouncement(a) {
		return "not the owner of " + a.ID + ", and not this node's operator"
	}
	return "not the owner of " + a.ID + " - only the owner resolves an announcement " +
		"that is not node-scope, whatever else they are on this node"
}

// announcementView is an announcement plus what THIS reader may do with it.
//
// may_resolve is answered by the node rather than worked out in the browser.
// The console drew no control at all before this - AnnouncementBanner's one
// button was ack, and it renders only for an announcement that names a
// resource, so a plain warning had no affordance of any kind. The fix is a
// button, and a button that appears and then 403s is worse than no button: the
// rule has two limbs and one of them is "is this token the operator of this
// node", which nothing in a browser can know.
//
// The artifact is embedded, so every field a reader had before is still where
// it was and this is one key more.
type announcementView struct {
	*store.Artifact
	MayResolve bool `json:"may_resolve"`
}

// announcementViews is the list as this reader may act on it.
func announcementViews(p *store.Principal, list []*store.Artifact) []announcementView {
	out := make([]announcementView, 0, len(list))
	for _, a := range list {
		out = append(out, announcementView{Artifact: a, MayResolve: mayResolve(p, a)})
	}
	return out
}

// handleResolveAnnouncement closes the window: the change is done, the banner
// clears.
//
// This is where the quiesce is enforced. An announcement that named a resource
// does not resolve while something is still holding it - the change has not
// finished, whatever the person driving it believes - and the answer says who
// it is waiting for. That refusal is the whole protocol: without it the mode
// and the acks would be a report rather than a gate.
//
// POST /api/announcement/{id}/resolve
func (s *server) handleResolveAnnouncement(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, ok := s.readAnnouncement(w, r)
	if !ok {
		return
	}
	if !mayResolve(p, art) {
		writeJSON(w, http.StatusForbidden, errorBody(resolveRefusal(art)))
		return
	}
	if art.Status == store.AnnouncementResolved {
		writeJSON(w, http.StatusOK, map[string]any{"announcement": art, "quiesce": nil})
		return
	}

	f := store.DecodeAnnouncementFields(art.Fields)
	var held *store.Quiesce
	if f.Resource != "" {
		q, err := s.db.QuiesceOf(r.Context(), art)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if q.State == store.QuiesceHeld {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "announcement " + art.ID + " still holds " + f.Resource +
					": waiting on " + strings.Join(q.Pending, ", "),
				"quiesce": q,
			})
			return
		}
		held = q
	}

	fields, err := store.ResolvedFields(art.Fields, time.Now())
	if err != nil {
		serverError(w, r, err)
		return
	}
	art.Status = store.AnnouncementResolved
	art.Fields = fields

	event, err := s.announcementEvent(r, "resolved", f.Scope, art.Severity, f.Resource, f.Mode)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.WriteAnnouncement(r.Context(), art, event); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("no such announcement"))
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"announcement": art, "quiesce": held, "event": event,
	})
}

// announcementEvent is the entry an announcement's own writes leave in the log.
func (s *server) announcementEvent(
	r *http.Request, did, scope, severity, resource, mode string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	meta, err := json.Marshal(map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"did":        did,
		"scope":      scope,
		"severity":   severity,
		"resource":   resource,
		"mode":       mode,
	})
	if err != nil {
		return nil, err
	}
	return &store.Event{
		Type:    store.EventAnnouncement,
		Room:    store.QuiesceRoom,
		Parents: []string{},
		Actor:   actor,
		Body:    did + " a " + severity + " announcement, " + scope + " scope",
		Meta:    meta,
	}, nil
}

// homeProject is the principal's project as an artifact's is: a pointer, and
// nil for a principal that has none.
func homeProject(p *store.Principal) *string {
	if p.Project == "" {
		return nil
	}
	home := p.Project
	return &home
}
