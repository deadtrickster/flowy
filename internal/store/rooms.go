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
		      VALUES ($1, $2, $3, $4, $3)
		 ON CONFLICT (project, room, principal) DO NOTHING`,
		project, name, actor, RoleOwner); err != nil {
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
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("store: this token resolves to nobody, so it has no rooms")
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT r.project, r.name, r.topic, coalesce(r.created_by, ''), r.created,
		        (SELECT count(*) FROM room_members m WHERE m.project = r.project AND m.room = r.name),
		        coalesce((SELECT m.role FROM room_members m
		                   WHERE m.project = r.project AND m.room = r.name AND m.principal = $2), '')
		   FROM rooms r
		  WHERE r.project = $1
		  ORDER BY r.created`,
		strings.TrimSpace(p.Project), actor)
	if err != nil {
		return nil, fmt.Errorf("store: list rooms: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Room{}
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.Project, &r.Name, &r.Topic, &r.Creator, &r.Created, &r.Members, &r.Role); err != nil {
			return nil, fmt.Errorf("store: list rooms: %w", err)
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
		project, room, actor).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: no room %q here - create it first", room)
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
		strings.TrimSpace(p.Project), room, actor)
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
