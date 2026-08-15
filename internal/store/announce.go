package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// Announcements, and the quiesce protocol that hangs off them.
//
// An announcement is an artifact of type 'announcement'. It is not a table of
// its own for the same reason a memory item is not: one table means one
// permission filter, one signature, one canonical encoding and one merge, and
// every property the fabric already promises about an artifact is then a
// property of an announcement without a line of code saying so. In particular a
// federation announcement is unforgeable because *every* artifact is - the
// merge verifies the signature of the node named on the row before it looks at
// anything else, and the signature covers the type, the severity, the status
// and the fields blob these three constants live in.
//
// What makes an artifact an announcement rather than a note is:
//
//	type       - 'announcement'
//	severity   - info|warning|maintenance|breaking
//	status     - 'active' until it is 'resolved'; that pair is the window
//	fields     - {"scope": ..., "resource": ..., "mode": ..., "resolved_at": ...}
//
// Scope is how far the announcement is meant to travel and it is enforced at
// the one place travelling happens: a node-scope announcement is refused on
// both replication doors, the pull that offers rows and the push that takes
// them. Project and federation scope replicate under the permission rules every
// other artifact replicates under - an announcement does not get a way round
// the filter, so a federation announcement reaches the peers that can read the
// project it was posted in. Scope says how far it is *meant* to go; the filter
// still says who may see it, and those are different questions.

// AnnouncementType is the artifact type.
const AnnouncementType = "announcement"

// The scopes.
const (
	ScopeNode       = "node"
	ScopeProject    = "project"
	ScopeFederation = "federation"
)

// The severities. maintenance and breaking are the two that may carry a
// quiesce: they are the ones that say something is about to change under you.
const (
	SeverityInfo        = "info"
	SeverityWarning     = "warning"
	SeverityMaintenance = "maintenance"
	SeverityBreaking    = "breaking"
)

// The quiesce modes: what an announcement asks of the dependents holding the
// resource it names.
//
//	ModeDrain - finish what you are doing and let go. A release clears you.
//	ModePause - stop now and let go. A release clears you.
//	ModeAckRequired - say you have seen this. Only an ack clears you, and
//	                  letting go of the resource does not: the point of the
//	                  mode is that somebody answered, not that the resource
//	                  went quiet on its own.
const (
	ModeDrain       = "drain"
	ModePause       = "pause"
	ModeAckRequired = "ack-required"
)

// The window.
const (
	AnnouncementActive   = "active"
	AnnouncementResolved = "resolved"
)

// The quiesce log. All three are minted by the endpoints that do the thing and
// refused to a client writing events by hand, exactly like a status move: an
// ack that anybody can write is an ack that means nothing.
const (
	EventQuiesceHold    = "quiesce.hold"
	EventQuiesceRelease = "quiesce.release"
	EventQuiesceAck     = "quiesce.ack"
	// EventAnnouncement is the log entry an announcement's own writes leave:
	// one when it is posted, one when it is resolved.
	EventAnnouncement = "announcement"
)

// QuiesceRoom is the room the quiesce log lands in.
const QuiesceRoom = "quiesce"

var (
	announcementScopes = map[string]bool{
		ScopeNode: true, ScopeProject: true, ScopeFederation: true,
	}
	announcementSeverities = map[string]bool{
		SeverityInfo: true, SeverityWarning: true,
		SeverityMaintenance: true, SeverityBreaking: true,
	}
	quiesceModes = map[string]bool{
		ModeDrain: true, ModePause: true, ModeAckRequired: true,
	}
)

// ScopeOK, SeverityOK and ModeOK are the closed sets, so a caller naming
// something else is told rather than quietly given the default.
func ScopeOK(s string) bool    { return announcementScopes[s] }
func SeverityOK(s string) bool { return announcementSeverities[s] }
func ModeOK(s string) bool     { return quiesceModes[s] }

// QuiesceSeverity reports whether a severity is one that may carry a quiesce.
// Info and warning are notices; maintenance and breaking are changes.
func QuiesceSeverity(s string) bool {
	return s == SeverityMaintenance || s == SeverityBreaking
}

// AnnouncementFields is the fields blob of an announcement artifact. It is
// inside the row's signature - fields is folded into the canonical encoding as
// its sha256 - so a relay cannot widen a node announcement into a federation
// one on its way past.
type AnnouncementFields struct {
	Scope      string `json:"scope"`
	Resource   string `json:"resource,omitempty"`
	Mode       string `json:"mode,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// Encode is the blob as it is stored.
func (f AnnouncementFields) Encode() (json.RawMessage, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("store: encode announcement fields: %w", err)
	}
	return raw, nil
}

// DecodeAnnouncementFields reads the blob back. A blob that will not parse
// carries no scope, and a row with no scope is treated as node-scope
// everywhere it matters: the safe end of the one decision this field makes.
func DecodeAnnouncementFields(raw []byte) AnnouncementFields {
	var f AnnouncementFields
	if len(raw) == 0 {
		return AnnouncementFields{Scope: ScopeNode}
	}
	if err := json.Unmarshal(raw, &f); err != nil || !ScopeOK(f.Scope) {
		return AnnouncementFields{Scope: ScopeNode}
	}
	return f
}

// AnnouncementScope is the scope of an artifact, or "" when it is not an
// announcement at all. Callers that have to decide whether a row may cross a
// node boundary use IsLocalAnnouncement, which is this question and the type
// question asked together.
func AnnouncementScope(a *Artifact) string {
	if a == nil || a.Type != AnnouncementType {
		return ""
	}
	return DecodeAnnouncementFields(a.Fields).Scope
}

// IsLocalAnnouncement reports whether a row is an announcement that must not
// leave this node.
//
// It is the Go half of localAnnouncementSQL and says the same thing: the two
// doors are the pull that offers rows to a peer and the push that takes them
// from one, and the project's own history is full of rules that were on one
// door and not the other. So this predicate and that clause are written next to
// each other and are read by both.
func IsLocalAnnouncement(a *Artifact) bool {
	return AnnouncementScope(a) == ScopeNode
}

// localAnnouncementSQL is IsLocalAnnouncement as a SQL predicate over an
// artifacts row, for the pull side where the rows are still in the database.
//
// The coalesce matters: a row with a NULL fields, or one whose blob has no
// scope, has no scope to widen it, and the safe reading of "no scope" is the
// one that does not travel.
func localAnnouncementSQL(alias string) string {
	return alias + `.type = '` + AnnouncementType + `' AND ` +
		`coalesce(` + alias + `.fields->>'scope', '` + ScopeNode + `') = '` + ScopeNode + `'`
}

// ReplicableArtifactSQL is what a pull may offer: everything that is not a
// node-scope announcement.
func ReplicableArtifactSQL(alias string) string {
	return `NOT (` + localAnnouncementSQL(alias) + `)`
}

// WriteAnnouncement writes the announcement and the log entry that records it,
// in one transaction and under one clock reading - the same shape as a memory
// write and a status move, and for the same reason: an announcement with no
// entry behind it is a change nobody can audit, and the half that did land
// replicates on its own.
func (d *DB) WriteAnnouncement(ctx context.Context, a *Artifact, e *Event) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "announcement.write")
	defer span.End()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write announcement: %w", err)
	}
	d.fillAt(a, at)
	e.SeqHLC = at
	e.Artifact, e.Project = a.ID, a.Project

	return d.inTx(ctx, "write announcement "+a.ID, func(tx *sql.Tx) error {
		if err := d.upsertArtifact(ctx, tx, a); err != nil {
			return err
		}
		return d.appendEvent(ctx, tx, e)
	})
}

// ActiveAnnouncements is what the console banner reads: the announcements this
// principal may see that have not been resolved, worst first and newest first
// within a severity.
//
// It is ListArtifacts' permission filter and nothing else - an announcement is
// an artifact, and one you cannot read is one you are not shown.
func (d *DB) ActiveAnnouncements(
	ctx context.Context, p *Principal, scopeAll bool,
) ([]*Artifact, error) {
	list, err := d.ListArtifacts(ctx, p, ArtifactQuery{
		Type:     AnnouncementType,
		Status:   AnnouncementActive,
		ScopeAll: scopeAll,
	})
	if err != nil {
		return nil, err
	}
	rank := map[string]int{
		SeverityBreaking: 0, SeverityMaintenance: 1, SeverityWarning: 2, SeverityInfo: 3,
	}
	at := func(a *Artifact) int {
		if r, ok := rank[a.Severity]; ok {
			return r
		}
		return len(rank)
	}
	sort.SliceStable(list, func(i, j int) bool { return at(list[i]) < at(list[j]) })
	return list, nil
}

// Quiesce is what an announcement is still waiting for.
type Quiesce struct {
	Announcement string   `json:"announcement"`
	Resource     string   `json:"resource"`
	Mode         string   `json:"mode"`
	Holders      []string `json:"holders"`
	Acked        []string `json:"acked"`
	Pending      []string `json:"pending"`
	// State is "held" while something has not answered, "released" once
	// everything has. It is what POST /api/announcement/{id}/resolve reads
	// before it lets the change proceed.
	State string `json:"state"`
}

// Quiesce states.
const (
	QuiesceHeld     = "held"
	QuiesceReleased = "released"
)

// ErrNoQuiesce is an announcement that names no resource: there is nothing to
// drain, so there is no quiesce to report.
var ErrNoQuiesce = errors.New("store: this announcement names no resource")

// QuiesceOf works out where an announcement's quiesce has got to.
//
// Holders are the actors that took a hold on the resource. Under drain and
// pause, letting the resource go is an answer - that is what the mode asked
// for - so a released hold is no longer a holder. Under ack-required it is not:
// the announcement asked to be acknowledged, and a process that quietly went
// away has not acknowledged anything. Either way an ack clears the holder.
//
// It does not filter by principal. The caller gates on being able to read the
// announcement, which is one question instead of one per event, and it keeps
// the answer the same for everybody who can see the announcement at all - a
// quiesce that reported a different pending list to each reader would be a
// release that depends on who asked.
func (d *DB) QuiesceOf(ctx context.Context, a *Artifact) (*Quiesce, error) {
	f := DecodeAnnouncementFields(a.Fields)
	if f.Resource == "" {
		return nil, ErrNoQuiesce
	}
	q := &Quiesce{
		Announcement: a.ID, Resource: f.Resource, Mode: f.Mode,
		Holders: []string{}, Acked: []string{}, Pending: []string{},
	}

	holders, err := d.quiesceActors(ctx,
		`SELECT DISTINCT actor FROM events
		  WHERE type = $1 AND actor <> '' AND meta->>'resource' = $2`,
		EventQuiesceHold, f.Resource)
	if err != nil {
		return nil, err
	}
	if f.Mode != ModeAckRequired {
		released, err := d.quiesceActors(ctx,
			`SELECT DISTINCT actor FROM events
			  WHERE type = $1 AND actor <> '' AND meta->>'resource' = $2`,
			EventQuiesceRelease, f.Resource)
		if err != nil {
			return nil, err
		}
		holders = without(holders, released)
	}
	acked, err := d.quiesceActors(ctx,
		`SELECT DISTINCT actor FROM events
		  WHERE type = $1 AND actor <> '' AND artifact = $2`,
		EventQuiesceAck, a.ID)
	if err != nil {
		return nil, err
	}

	q.Holders = holders
	q.Acked = intersect(acked, holders)
	q.Pending = without(holders, acked)
	q.State = QuiesceReleased
	if len(q.Pending) > 0 {
		q.State = QuiesceHeld
	}
	return q, nil
}

// quiesceActors runs one of the three set queries above and returns the actors
// it found, sorted so that two reads of the same state read the same.
func (d *DB) quiesceActors(ctx context.Context, query string, vals ...any) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, query, vals...)
	if err != nil {
		return nil, fmt.Errorf("store: quiesce actors: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil {
			return nil, fmt.Errorf("store: quiesce actors: %w", err)
		}
		out = append(out, actor)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: quiesce actors: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// without is the members of a that are not in b; intersect is the ones that
// are. Both keep a's order, which quiesceActors already sorted.
func without(a, b []string) []string {
	drop := make(map[string]bool, len(b))
	for _, s := range b {
		drop[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}

func intersect(a, b []string) []string {
	keep := make(map[string]bool, len(b))
	for _, s := range b {
		keep[s] = true
	}
	out := []string{}
	for _, s := range a {
		if keep[s] {
			out = append(out, s)
		}
	}
	return out
}

// ResolvedFields is an announcement's fields with the moment it was resolved
// written into them, so the window is on the row and travels with it rather
// than being inferred from a column the merge happens to have updated.
func ResolvedFields(raw []byte, at time.Time) (json.RawMessage, error) {
	f := DecodeAnnouncementFields(raw)
	f.ResolvedAt = at.UTC().Format(time.RFC3339Nano)
	return f.Encode()
}
