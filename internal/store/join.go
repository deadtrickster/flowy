package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// JoinKind is a request to exist here, from something that does not yet.
//
// It is a work kind so it lands in the queue the operator already reads. A join
// request is a decision waiting on a person, which is what the queue is for, and
// giving it its own surface would be a second place to look for the same kind of
// thing.
const JoinKind = "join"

// EventJoinRequest is what asking leaves in the log. The asking is recorded
// whether or not it is ever granted: a refused request is evidence somebody
// tried, and that is worth as much as an approval when a seat turns up later
// claiming it was told to.
const EventJoinRequest = "join.request"

// JoinRoom is where join requests are announced.
const JoinRoom = "general"

// ErrHandleTaken is a second request for a handle that already exists or is
// already pending. It names the first, because "taken" without saying by what
// sends the asker round the loop again with a new name they did not need.
type ErrHandleTaken struct {
	Handle string
	By     string
}

func (e *ErrHandleTaken) Error() string {
	return fmt.Sprintf("handle %q is already %s - pick another, or ask whoever holds it", e.Handle, e.By)
}

// JoinRequest is one ask, as written by something with no token.
type JoinRequest struct {
	Handle  string
	Kind    string
	Project string
	Reason  string
}

// RequestJoin records a request to exist. IT GRANTS NOTHING.
//
// This is the only door in the system that takes no principal, and it earns that
// by writing one kind of row and doing nothing else. The chicken-and-egg it
// solves is real: minting is the operator's, so an agent with no token cannot
// post to the room to ask for one, and today every seat exists because a human
// already knew it should. That works while a person starts every agent by hand
// and fails the moment anything else does.
//
// The request is INERT. It carries no permission, it cannot read, and until a
// human approves it the only thing that has happened is that a row exists saying
// somebody asked. That is the whole security argument: an open door that writes
// a request is not an open door that grants one.
func (d *DB) RequestJoin(ctx context.Context, req JoinRequest) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "join.request")
	defer span.End()

	handle := strings.TrimSpace(req.Handle)
	if handle == "" {
		return nil, nil, fmt.Errorf("store: a join request names the handle it is asking for")
	}
	if strings.ContainsAny(handle, " /\t\n") {
		return nil, nil, fmt.Errorf("store: %q is not a handle - no spaces or slashes, it has to address a seat", handle)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		// Asked for on purpose. A request with no reason gives the operator
		// nothing to decide on, and the decision is the entire point of the row.
		return nil, nil, fmt.Errorf("store: a join request says what it is for: send reason")
	}

	if taken, by, err := d.handleTaken(ctx, handle); err != nil {
		return nil, nil, err
	} else if taken {
		return nil, nil, &ErrHandleTaken{Handle: handle, By: by}
	}

	project := strings.TrimSpace(req.Project)
	if project == "" {
		return nil, nil, fmt.Errorf("store: a join request names the project it wants into: send project")
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "agent"
	}

	fields, err := json.Marshal(map[string]any{
		"join_handle":  handle,
		"join_kind":    kind,
		"join_project": project,
		"join_state":   "pending",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: join request for %s: %w", handle, err)
	}

	art := &Artifact{
		Type:   MemoryType,
		Kind:   JoinKind,
		Title:  "join request: " + handle,
		Body:   reason,
		Status: TodoStatus,
		// The request belongs to the project it wants into, so the operator of
		// that project is who sees it. A request nobody can see is a request
		// nobody can grant.
		Project:    &project,
		Visibility: VisibilityProject,
		Fields:     fields,
	}
	entry := &Event{
		Type:    EventJoinRequest,
		Project: &project,
		Room:    JoinRoom,
		Body:    fmt.Sprintf("%s asks to join %s as a %s", handle, project, kind),
		Meta:    fields,
		// No actor: nothing has an identity here yet, which is the whole point.
		// The row says somebody asked; who they turn out to be is decided by the
		// approval, not by the request.
		Actor: "",
	}
	// One write, and the row is what the operator acts on. The event is
	// unsigned by construction - there is no key to sign it with, because the
	// asker has no identity yet - so the ROW is the record and the entry is the
	// announcement.
	if err := d.UpsertArtifact(ctx, art); err != nil {
		return nil, nil, fmt.Errorf("store: write join request for %s: %w", handle, err)
	}
	entry.Artifact = art.ID
	if err := d.AppendEvent(ctx, entry); err != nil {
		return nil, nil, fmt.Errorf("store: announce join request for %s: %w", handle, err)
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// handleTaken reports whether a handle is already an agent or already pending,
// and says which. Both are refusals and they are different facts: an existing
// seat means ask its holder, a pending request means wait.
func (d *DB) handleTaken(ctx context.Context, handle string) (bool, string, error) {
	// A handle names a USER, not an agent row - a seat is a user with agents
	// under it, and users.handle is the unique one. Checking agents would have
	// been checking the wrong table for the thing that has to be unique.
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE handle = $1`, handle).Scan(&n)
	if err != nil {
		return false, "", fmt.Errorf("store: check handle %s: %w", handle, err)
	}
	if n > 0 {
		return true, "a seat here", nil
	}
	err = d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM artifacts
		  WHERE kind = $1 AND tombstone = false AND status <> $2
		    AND fields->>'join_handle' = $3`,
		JoinKind, DoneStatus, handle).Scan(&n)
	if err != nil {
		return false, "", fmt.Errorf("store: check pending join for %s: %w", handle, err)
	}
	if n > 0 {
		return true, "requested and waiting on the operator", nil
	}
	return false, "", nil
}

// JoinHandleOf reads the handle a join row is asking for.
func JoinHandleOf(a *Artifact) string { return artifactString(a, "join_handle") }

// JoinKindOf reads the seat kind a join row is asking for.
func JoinKindOf(a *Artifact) string { return artifactString(a, "join_kind") }

// JoinStateOf is pending, approved or refused.
func JoinStateOf(a *Artifact) string { return artifactString(a, "join_state") }

// joinPendingGuard is the compare-and-set: the row must still be the one that
// was read. `guard` is a SQL fragment appended to the WHERE, so it is written
// here from a constant rather than assembled from anything a caller sends.
const joinPendingGuard = "status = '" + TodoStatus + "'"

// ErrNotAJoinRequest is the answer for an id that is not one.
var ErrNotAJoinRequest = errors.New("store: that is not a join request")

// ApproveJoin grants a pending request: it mints the seat and records who said
// yes, in one act.
//
// OPERATOR ONLY, and the check is the caller's to make - this verb takes the
// decision as already made and does the two things that follow from it. That
// split is deliberate: who may approve is an authorisation question that belongs
// at the door, and what approval MEANS is this.
//
// The token comes back exactly once, in the return. It is not written to the row
// and not put in the log: a credential in an artifact is a credential in every
// replica of it, and the whole point of minting on approval is that the secret
// travels from the operator to the asker and stops.
func (d *DB) ApproveJoin(ctx context.Context, p *Principal, id string) (*Artifact, *Minted, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "join.approve")
	defer span.End()

	actor, _ := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot approve a join")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if art.Kind != JoinKind {
		return nil, nil, ErrNotAJoinRequest
	}
	if state := JoinStateOf(art); state != "pending" {
		// Approving twice would mint a second seat for one request and hand out
		// a second token, which is the shape a replay attack wants. The state is
		// the guard and it says what happened instead.
		return nil, nil, fmt.Errorf("store: join request %s is already %s", art.ID, state)
	}

	handle := JoinHandleOf(art)
	project := ""
	if art.Project != nil {
		project = *art.Project
	}
	minted, err := d.MintAgent(ctx, MintSpec{
		Handle:  handle,
		Kind:    "agent",
		Project: project,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: mint %s on approval: %w", handle, err)
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	fields["join_state"] = "approved"
	fields["join_user"] = minted.User
	fields["join_agent"] = minted.Agent
	fields["join_approved_by"] = actor
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: approve join %s: %w", art.ID, err)
	}

	meta, _ := json.Marshal(map[string]string{
		"join_handle": handle,
		"join_user":   minted.User,
		"join_agent":  minted.Agent,
		"actor_user":  p.UserID,
	})
	entry := &Event{
		Type:     EventJoinRequest,
		Project:  art.Project,
		Room:     JoinRoom,
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     fmt.Sprintf("%s approved: %s exists now", handle, handle),
		Meta:     meta,
	}
	// GUARDED ON THE STATUS IT CAME IN WITH. The state check above is a
	// courtesy that reads before it writes; this is the one that holds when two
	// operators approve the same request in the same second. Exactly one wins,
	// and the loser is told rather than minting a second seat and handing out a
	// second token.
	if err := d.SetArtifactFieldsAndStatusIf(ctx, art, column, DoneStatus, joinPendingGuard, entry); err != nil {
		return nil, nil, err
	}
	art.Status = DoneStatus
	span.SetArtifact(art.ID)
	return art, minted, nil
}

// RefuseJoin says no, with a reason, and closes the request.
//
// A refusal is worth as much as an approval and for the same reason every
// refusal here is: an asker told no can stop, and an asker told nothing retries
// forever. The reason is required for that - "refused" alone leaves them to
// guess whether to try again with a different handle.
func (d *DB) RefuseJoin(ctx context.Context, p *Principal, id, reason string) (*Artifact, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("store: this token resolves to nobody, so it cannot refuse a join")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("store: a refusal says why, so the asker knows whether to ask again")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if art.Kind != JoinKind {
		return nil, ErrNotAJoinRequest
	}
	if state := JoinStateOf(art); state != "pending" {
		return nil, fmt.Errorf("store: join request %s is already %s", art.ID, state)
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, err
	}
	fields["join_state"] = "refused"
	fields["join_refused_by"] = actor
	fields["join_refused_because"] = reason
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: refuse join %s: %w", art.ID, err)
	}
	entry := &Event{
		Type: EventJoinRequest, Project: art.Project, Room: JoinRoom,
		Thread: art.ID, Artifact: art.ID, Actor: actor,
		Body: fmt.Sprintf("%s refused: %s", JoinHandleOf(art), reason),
	}
	if err := d.SetArtifactFieldsAndStatusIf(ctx, art, column, DoneStatus, joinPendingGuard, entry); err != nil {
		return nil, err
	}
	art.Status = DoneStatus
	return art, nil
}
