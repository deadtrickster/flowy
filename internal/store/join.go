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

// ErrNotAJoinRequest is the answer for an id that is not one.
var ErrNotAJoinRequest = errors.New("store: that is not a join request")
