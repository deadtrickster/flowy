package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A room used to exist because somebody had spoken in it. These verbs give it
// an existence of its own - created before it has traffic, joined by invitation
// rather than by guessing the name - without changing how a message refers to
// one. See schema.sql: the key is (project, name), and events still carry the
// name as text.

// RoleOwner may invite and remove. RoleMember may do neither, and may always
// remove themselves. There is no third role, because there is not yet a third
// question.
//
// Named Role and not RoomMember because inbox.go already has a RoomMember, and
// it means something else: WHO HAS SPOKEN in a room this principal may read.
// That notion and this one will now coexist - one is participation observed
// from the traffic, the other is membership somebody granted - and they must
// not be conflated, least of all by sharing a name.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
	// RoleReader is a PROJECT role - somebody who reads a project and writes
	// nothing in it. It is declared beside the other two because they are one
	// vocabulary: the same word must not mean different things in the room
	// table and the project table. It has no meaning for a room yet; a room
	// nobody may write in is a different question and nobody has asked it.
	RoleReader = "reader"
)

// ErrRoomTaken is a create that lands on a name this project already has. It is
// its own error because "it is already there" and "you may not" are different
// answers and a caller does different things with them.
var ErrRoomTaken = errors.New("store: that room already exists here")

// Room is a room and, when a principal asked, that principal's role in it.
type Room struct {
	Project string    `json:"project"`
	Name    string    `json:"name"`
	Topic   string    `json:"topic,omitempty"`
	Creator string    `json:"created_by,omitempty"`
	Created time.Time `json:"created"`
	Members int       `json:"members"`
	Role    string    `json:"role,omitempty"`
	// Declared is false for a room that exists only because somebody spoke in
	// it. It has no owner, so nobody can invite into it until it is created.
	Declared bool `json:"declared"`
}

// roomName is the one place a room's name is judged, so the rules cannot
// disagree between the create door and the invite door.
//
// Lowercased because "General" and "general" being two rooms is a bug nobody
// would file and everybody would hit - the name is an address, not a title, and
// the topic is where a room says what it is called.
func roomName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", fmt.Errorf("store: a room needs a name")
	}
	if len(name) > 64 {
		return "", fmt.Errorf("store: %q is too long for a room name", name)
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return "", fmt.Errorf("store: a room name is letters, digits, - and _ : %q is not", name)
	}
	return name, nil
}

// roomPrincipal is WHO a membership is about, and it is a person rather than a
// seat.
//
// voteActor returns the agent id when a token has one, which is right for
// authorship - a message is said by a seat. It is wrong here. On 2026-08-18 an
// invite stored the user id it was handed while RoomsFor matched the caller's
// agent id, so an invited member was in the room and could not see it: written
// in one identity space, read in another, no error anywhere.
//
// A room is joined by a PERSON. Keying on the agent would mean every seat one
// human runs needs its own invitation, and a human who starts a new seat
// silently drops out of every room they were invited to. So an agent inherits
// its user's rooms, and this is the single place that decides it.
func roomPrincipal(p *Principal) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.UserID)
}

// CreateRoom makes a room and puts its creator in it as owner.
//
// Both writes or neither: a room with no owner cannot be invited into by
// anybody, so it would be a room nobody can ever join - created, listed, and
// permanently inert.
func (d *DB) CreateRoom(ctx context.Context, p *Principal, name, topic string) (*Room, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("store: this token resolves to nobody, so it cannot create a room")
	}
	name, err := roomName(name)
	if err != nil {
		return nil, err
	}
	project := strings.TrimSpace(p.Project)
	if project == "" {
		return nil, fmt.Errorf("store: a room belongs to a project, and this token names none")
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: create room %q: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()

	var created time.Time
	err = tx.QueryRowContext(ctx,
		`INSERT INTO rooms (project, name, topic, created_by)
		      VALUES ($1, $2, $3, $4)
		 ON CONFLICT (project, name) DO NOTHING
		   RETURNING created`,
		project, name, strings.TrimSpace(topic), actor).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRoomTaken
	}
	if err != nil {
		return nil, fmt.Errorf("store: create room %q: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO room_members (project, room, principal, role, added_by)
		      VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (project, room, principal) DO NOTHING`,
		project, name, roomPrincipal(p), RoleOwner, actor); err != nil {
		return nil, fmt.Errorf("store: create room %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: create room %q: %w", name, err)
	}
	return &Room{
		Project: project, Name: name, Topic: strings.TrimSpace(topic),
		Creator: actor, Created: created, Members: 1, Role: RoleOwner,
	}, nil
}

// RoomsFor lists the rooms this principal may see, newest membership first.
//
// It returns rooms the principal is IN, plus every room in the project they are
// not in, because membership does not gate reading yet - see schema.sql. When
// it does, this is the one query that changes, and Role being empty is already
// how a caller tells the two apart.
func (d *DB) RoomsFor(ctx context.Context, p *Principal) ([]Room, error) {
	return d.RoomsIn(ctx, p, strings.TrimSpace(p.Project))
}

// RoomsIn is RoomsFor for a NAMED project, which is what a person asks when
// they can see three projects on a node and nothing inside two of them.
//
// IT DOES NOT DECIDE WHETHER THEY MAY. The caller checks that against
// ReadableProjects and refuses by name - see handleListRooms. Putting the
// permission here as well would be a second implementation of "may this token
// read that project", and the two would disagree the first time one of them
// changed.
//
// Membership is still the ASKING principal's: role is what THIS reader is in
// that room, whichever project the room lives in.
func (d *DB) RoomsIn(ctx context.Context, p *Principal, project string) ([]Room, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("store: this token resolves to nobody, so it has no rooms")
	}
	// THE UNION IS THE MIGRATION. Rooms existed before this table did, as
	// nothing but a name on a message, and general/handoffs/incidents are full
	// of traffic nobody is going to migrate. Seeding rows for them would mean
	// guessing which rooms and which members, and a seed that guesses wrong is
	// worse than no seed.
	//
	// So a room is listed if somebody created it OR if somebody has spoken in
	// it. The second is not a reader inventing a value - it is the definition
	// a room has had all along, reported rather than assumed, and `created`
	// being null is how a caller tells an implicit room from a declared one.
	rows, err := d.sql.QueryContext(ctx,
		`WITH named AS (
		     SELECT project, name FROM rooms WHERE project = $1
		     UNION
		     SELECT project, room AS name FROM events
		      WHERE project = $1 AND coalesce(room, '') <> ''
		 )
		 SELECT n.project, n.name,
		        coalesce(r.topic, ''), coalesce(r.created_by, ''), r.created,
		        (SELECT count(*) FROM room_members m WHERE m.project = n.project AND m.room = n.name),
		        coalesce((SELECT m.role FROM room_members m
		                   WHERE m.project = n.project AND m.room = n.name AND m.principal = $2), '')
		   FROM named n
		   LEFT JOIN rooms r ON r.project = n.project AND r.name = n.name
		  ORDER BY r.created NULLS LAST, n.name`,
		strings.TrimSpace(project), roomPrincipal(p))
	if err != nil {
		return nil, fmt.Errorf("store: list rooms: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Room{}
	for rows.Next() {
		var (
			r       Room
			created sql.NullTime
		)
		if err := rows.Scan(&r.Project, &r.Name, &r.Topic, &r.Creator, &created, &r.Members, &r.Role); err != nil {
			return nil, fmt.Errorf("store: list rooms: %w", err)
		}
		// A room nobody declared has no creation moment, and saying so is the
		// point: Declared is what a caller checks before offering to invite
		// into it, because there is nobody to be its owner yet.
		r.Declared = created.Valid
		if created.Valid {
			r.Created = created.Time
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list rooms: %w", err)
	}
	return out, nil
}

// InviteToRoom puts somebody in a room. Owners only.
//
// The refusal names which of the two things is wrong - the room, or your
// standing in it - because "403" sends the caller to re-read their token when
// the answer is that they are a member and not an owner.
func (d *DB) InviteToRoom(ctx context.Context, p *Principal, room, principal string) error {
	actor, _ := voteActor(p)
	if actor == "" {
		return fmt.Errorf("store: this token resolves to nobody, so it cannot invite")
	}
	room, err := roomName(room)
	if err != nil {
		return err
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return fmt.Errorf("store: an invitation names somebody")
	}
	project := strings.TrimSpace(p.Project)

	var role string
	err = d.sql.QueryRowContext(ctx,
		`SELECT coalesce(m.role, '')
		   FROM rooms r
		   LEFT JOIN room_members m
		     ON m.project = r.project AND m.room = r.name AND m.principal = $3
		  WHERE r.project = $1 AND r.name = $2`,
		project, room, roomPrincipal(p)).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		// A room somebody has spoken in but nobody declared is not a room you
		// can be invited to: it has no owner, so there is nobody whose
		// invitation would mean anything. Saying "create it first" is the
		// actual next step rather than a bare not-found.
		return fmt.Errorf("store: no room %q has been created here - somebody may have spoken in one by that name, "+
			"but an invitation needs an owner, so create it first", room)
	}
	if err != nil {
		return fmt.Errorf("store: invite to %q: %w", room, err)
	}
	if role != RoleOwner {
		return fmt.Errorf("store: %q is invited into by its owners, and you are %q there",
			room, orDash(role))
	}
	// Already a member is not an error. An invitation is a statement about who
	// belongs, and repeating a true statement changed nothing - refusing it
	// would make the caller check first and then race anybody else inviting.
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO room_members (project, room, principal, role, added_by)
		      VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (project, room, principal) DO NOTHING`,
		project, room, principal, RoleMember, actor); err != nil {
		return fmt.Errorf("store: invite to %q: %w", room, err)
	}
	return nil
}

// LeaveRoom removes the caller, and only ever the caller. Removing somebody
// else is a different verb with a different check, and collapsing them is how
// an ordinary member ends up able to empty a room.
func (d *DB) LeaveRoom(ctx context.Context, p *Principal, room string) (bool, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return false, fmt.Errorf("store: this token resolves to nobody, so it is in no rooms")
	}
	room, err := roomName(room)
	if err != nil {
		return false, err
	}
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM room_members WHERE project = $1 AND room = $2 AND principal = $3`,
		strings.TrimSpace(p.Project), room, roomPrincipal(p))
	if err != nil {
		return false, fmt.Errorf("store: leave %q: %w", room, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: leave %q: %w", room, err)
	}
	return n > 0, nil
}

// orDash renders an empty role as something a sentence can contain.
func orDash(role string) string {
	if role == "" {
		return "not a member"
	}
	return role
}
