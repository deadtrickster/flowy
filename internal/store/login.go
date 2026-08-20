package store

// A person's way in, as opposed to a seat's.
//
// The operator said it in one line - "i dont want to bother with token. token
// is for api, not for me" - and the console said the same thing from the other
// side: session.tsx carried the sentence "there is no login". A bearer pasted
// into a browser is a credential with no expiry, no revocation and no name on
// it, held by the one principal whose actions most need attributing.
//
// TWO FACTS, TWO TABLES, AND NEITHER IS tokens. user_secrets holds a verifier
// and sessions holds what a verified login produced. tokens stays what it is: a
// long-lived API credential a process holds, SELECTed on every request by
// PrincipalForToken. See the schema for why sharing a table would be wrong even
// though nothing replicates any of them today.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// BcryptCost is the work factor new secrets are written at.
//
// Stored beside the hash rather than assumed, so raising it later is a decision
// with a migration rather than a silent divergence: bcrypt encodes the cost in
// its own output, and VerifyLogin re-hashes on a correct password whose stored
// cost is below this one. A password that is right is the only moment the
// plaintext exists, so it is the only moment an upgrade is possible.
const BcryptCost = 12

// SessionLifetime is how long a login lasts.
//
// Written into the row rather than computed at read time. Shortening this later
// must not retroactively end a session somebody is in the middle of using, and
// a value that lives only in code cannot say when an existing session ends.
const SessionLifetime = 14 * 24 * time.Hour

// AlgoBcrypt names what user_secrets.hash holds.
const AlgoBcrypt = "bcrypt"

// ErrBadLogin is the one answer to every failed login.
//
// ONE ERROR FOR BOTH HALVES, deliberately. "no such handle" and "wrong
// password" told apart is an oracle for which accounts exist, and this node
// has a handful of principals whose names are already in every room - but the
// habit is what matters, because the next node's are not.
var ErrBadLogin = errors.New("store: handle or password is wrong")

// Session is one logged-in browser.
type Session struct {
	ID       string    `json:"id"`
	UserID   string    `json:"user_id"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	LastSeen time.Time `json:"last_seen"`
}

// SetPassword writes or replaces a user's verifier.
//
// It refuses a user that does not exist rather than writing a secret for
// nobody: the foreign key would refuse it anyway, and the caller gets a
// sentence instead of a constraint name.
func (d *DB) SetPassword(ctx context.Context, userID, password string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("store: set password: no user")
	}
	// A LENGTH FLOOR AND A CEILING. bcrypt silently truncates at 72 bytes, so a
	// longer password would be accepted here and then verified on its first 72 -
	// two different passwords that both work is not something to discover later.
	if len(password) < 8 {
		return fmt.Errorf("store: a password needs at least 8 characters")
	}
	if len(password) > 72 {
		return fmt.Errorf("store: bcrypt reads only the first 72 bytes, so a longer " +
			"password would be silently truncated - use at most 72")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO user_secrets (user_id, algo, hash, updated)
		 SELECT $1, $2, $3, now() FROM users WHERE id = $1
		 ON CONFLICT (user_id) DO UPDATE
		    SET algo = excluded.algo, hash = excluded.hash, updated = now()`,
		userID, AlgoBcrypt, string(hash))
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasPassword reports whether this user can log in at all.
//
// The console asks so it can say "ask the operator to set one" rather than
// "handle or password is wrong" to somebody who has no password and cannot
// know it. It answers for a user id, which the caller already had to resolve -
// it is not a handle probe.
func (d *DB) HasPassword(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM user_secrets WHERE user_id = $1)`, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: has password: %w", err)
	}
	return ok, nil
}

// dummyHash is a real bcrypt hash of a value nobody has.
//
// It exists so a login for a handle that does not exist costs the same as one
// for a handle that does. Without it the miss returns in microseconds and the
// hit takes the cost-12 work, which is a timing oracle for which accounts are
// real - measurable over a LAN without trying hard.
var dummyHash = []byte("$2a$12$C6UzMDM.H6dfI/f/IKcEe.7ZAJ.k3f9Xn8sq/qU9uHGPMLuGQvBSC")

// VerifyLogin resolves a handle and password to the user behind them.
//
// Every failure is ErrBadLogin, and every failure costs a bcrypt comparison.
func (d *DB) VerifyLogin(ctx context.Context, handle, password string) (*User, error) {
	handle = strings.TrimSpace(handle)

	var (
		id, algo, hash string
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT u.id, coalesce(s.algo, ''), coalesce(s.hash, '')
		   FROM users u LEFT JOIN user_secrets s ON s.user_id = u.id
		  WHERE u.handle = $1`, handle).Scan(&id, &algo, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Spend the time anyway. See dummyHash.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadLogin
	case err != nil:
		return nil, fmt.Errorf("store: verify login: %w", err)
	}
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadLogin
	}
	if algo != AlgoBcrypt {
		// A verifier this build cannot check is not a licence to let anybody in.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, fmt.Errorf("store: user %s has a %q secret this build cannot verify", id, algo)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrBadLogin
	}
	// RE-HASH ON A CORRECT PASSWORD whose stored cost has fallen behind. This
	// is the only moment the plaintext exists, so it is the only moment the
	// work factor can be raised without asking anybody to type it again. A
	// failure here is not a failed login - the password was right.
	if cost, cerr := bcrypt.Cost([]byte(hash)); cerr == nil && cost < BcryptCost {
		_ = d.SetPassword(ctx, id, password)
	}
	return d.GetUser(ctx, id)
}

// StartSession records a login and hands back the row.
//
// The id is the cookie value, so it is minted the way every other identifier
// here is and never derived from anything about the user.
func (d *DB) StartSession(ctx context.Context, userID, userAgent string) (*Session, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("store: start session: no user")
	}
	s := &Session{ID: ulid.NewString(), UserID: userID}
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO sessions (id, user_id, expires, user_agent)
		 SELECT $1, $2, now() + $3::interval, nullif($4, '') FROM users WHERE id = $2
		 RETURNING created, expires, last_seen`,
		s.ID, userID, intervalArg(SessionLifetime), userAgent).
		Scan(&s.Created, &s.Expires, &s.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: start session: %w", err)
	}
	return s, nil
}

// UserForSession resolves a cookie to a user, or ErrNotFound.
//
// EXPIRY IS ENFORCED IN THE STATEMENT, not by comparing in Go after the read:
// the row and the clock are then read at one instant, and a session that
// expires between the two cannot be honoured. It bumps last_seen in the same
// statement for the same reason - two statements would be two chances for one
// of them to be the only one that ran.
func (d *DB) UserForSession(ctx context.Context, id string) (*User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrNotFound
	}
	var user string
	err := d.sql.QueryRowContext(ctx,
		`UPDATE sessions SET last_seen = now()
		  WHERE id = $1 AND expires > now()
		 RETURNING user_id`, id).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve session: %w", err)
	}
	return d.GetUser(ctx, user)
}

// SessionProject is WHERE THIS BROWSER IS WORKING: the project the session has
// been put into, or "" when it has not been put into one.
//
// MEASURED 2026-08-20: a cookie session resolved to a principal with no project
// at all, so a logged-in person wrote nowhere and "switch projects" had nothing
// to switch. This is the fact that was missing, and it lives on the session
// rather than on the user because two windows may be in two projects and
// neither is more true than the other.
//
// AN EXPIRED SESSION ANSWERS NOTHING rather than answering its last project: a
// session that has ended is not somewhere, and a caller that got a project back
// from a dead session would write with it.
func (d *DB) SessionProject(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	var project sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT project FROM sessions WHERE id = $1 AND expires > now()`, id).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: session project: %w", err)
	}
	return strings.TrimSpace(project.String), nil
}

// EnterProject puts a session into one of its user's projects.
//
// MEMBERSHIP IS THE WHOLE CHECK, and it is done here rather than at the door
// for once, because there is nothing else this call could mean: entering a
// project you do not belong to is not a narrower version of entering one, it is
// a different act with no answer. A door checking it as well would be a second
// implementation of the same question - the thing that made /api/rooms take its
// permission from ReadableProjects rather than from a copy.
//
// A REFUSAL NAMES THE PROJECT AND SAYS WHICH FACT DECIDED. "You are not a member
// of X" and "there is no project called X" send somebody to two different
// people.
func (d *DB) EnterProject(ctx context.Context, sessionID, userID, project string) error {
	sessionID, project = strings.TrimSpace(sessionID), strings.TrimSpace(project)
	if sessionID == "" {
		return fmt.Errorf("store: no session to put into a project")
	}
	if project == "" {
		// Leaving the active project unset is a real act: a person who has left
		// every project should not be silently left writing into the last one.
		_, err := d.sql.ExecContext(ctx,
			`UPDATE sessions SET project = NULL WHERE id = $1`, sessionID)
		if err != nil {
			return fmt.Errorf("store: leave project: %w", err)
		}
		return nil
	}

	var exists bool
	if err := d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, project).Scan(&exists); err != nil {
		return fmt.Errorf("store: enter project: %w", err)
	}
	if !exists {
		return fmt.Errorf("store: there is no project called %q on this node", project)
	}

	var member bool
	if err := d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM project_members WHERE user_id = $1 AND project = $2)`,
		userID, project).Scan(&member); err != nil {
		return fmt.Errorf("store: enter project: %w", err)
	}
	if !member {
		return fmt.Errorf("store: you are not a member of %q, so you cannot work in it - "+
			"which is not the same as that project not existing", project)
	}

	if _, err := d.sql.ExecContext(ctx,
		`UPDATE sessions SET project = $2 WHERE id = $1 AND expires > now()`,
		sessionID, project); err != nil {
		return fmt.Errorf("store: enter project: %w", err)
	}
	return nil
}

// ProjectsOfUser is where this person works: their memberships, by name.
//
// SEPARATE FROM ReadableProjects, which is what a principal may READ. A person
// reads more than they are a member of - a grant points at a project they have
// never joined - and writing where you can read would put work in places nobody
// is looking. Two questions, two answers, and the second one is not a filter of
// the first.
func (d *DB) ProjectsOfUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT project FROM project_members WHERE user_id = $1 ORDER BY project`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: projects of user: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, fmt.Errorf("store: projects of user: %w", err)
		}
		out = append(out, project)
	}
	return out, rows.Err()
}

// The roles a person has in a PROJECT are the same words a room already uses -
// see RoleOwner and RoleMember in rooms.go, and RoleReader beside them. One
// vocabulary for both, deliberately: "owner" meaning two different things
// depending on which table it is in is how a reader learns to distrust the
// word.
//
// Only two of them decide anything today: a reader cannot write, and an owner
// can invite. The operator's finer cases - "some cant close or cant rause" -
// are real and are NOT here yet, because a role name that no door checks is a
// label, and this whole file exists to keep that from happening.
// RoleName is what to call a role in a sentence a person reads, including the
// case where they have none: BEING IN NO PROJECT IS NOT BEING A READER IN IT,
// and a refusal that said "you are a reader" to somebody who is not a member at
// all would send them to ask for the wrong thing.
func RoleName(role string) string {
	switch strings.TrimSpace(role) {
	case "":
		return "not a member"
	case RoleReader:
		return "a reader"
	case RoleOwner:
		return "an owner"
	case RoleMember:
		return "a member"
	default:
		return strconv.Quote(role)
	}
}

// RoleMayWrite reports whether that role can put something into the project.
//
// UNKNOWN ROLES MAY NOT WRITE. A role this build does not recognise arrives
// from a newer node, a hand-edited row, or a name somebody will add next week -
// and the safe reading of "I do not know what this means" is the one that
// refuses. A reader wrongly refused says so immediately; a writer wrongly
// allowed is discovered by reading the rows they wrote.
func RoleMayWrite(role string) bool {
	switch strings.TrimSpace(role) {
	case RoleMember, RoleOwner:
		return true
	default:
		return false
	}
}

// RoleInProject is what this person is in that project, or "" when they are not
// a member of it at all.
func (d *DB) RoleInProject(ctx context.Context, userID, project string) (string, error) {
	userID, project = strings.TrimSpace(userID), strings.TrimSpace(project)
	if userID == "" || project == "" {
		return "", nil
	}
	var role string
	err := d.sql.QueryRowContext(ctx,
		`SELECT role FROM project_members WHERE user_id = $1 AND project = $2`,
		userID, project).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: role in project: %w", err)
	}
	return strings.TrimSpace(role), nil
}

// MayInvite says whether this user can put somebody else into that project.
//
// The operator, 2026-08-20: "normal ownership and collaboration - i will invite
// other humans to projects." So it is not the node operator's act alone: the
// people who own a project bring others into it, which is what every ordinary
// control panel does and what this node did not have at all.
//
// WHO OWNS ONE: whoever declared it, and anybody they have made an owner. The
// creator is read from the registry row rather than from a membership, because
// a project can be declared before anybody is a member of it - including by the
// person declaring it - and an owner who cannot invite is an owner in name.
//
// A MEMBER IS NOT AN OWNER. Being able to work somewhere and being able to
// decide who else works there are different powers, and collapsing them is how
// a project quietly becomes open to everybody who was ever added to it.
func (d *DB) MayInvite(ctx context.Context, userID, project string) (bool, error) {
	userID, project = strings.TrimSpace(userID), strings.TrimSpace(project)
	if userID == "" || project == "" {
		return false, nil
	}
	var yes bool
	err := d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $2 AND created_by = $1)
		     OR EXISTS (SELECT 1 FROM project_members
		                 WHERE user_id = $1 AND project = $2 AND role = 'owner')`,
		userID, project).Scan(&yes)
	if err != nil {
		return false, fmt.Errorf("store: may invite: %w", err)
	}
	return yes, nil
}

// JoinProject makes somebody a member. The OPERATOR's act - see the door - and
// idempotent, because "make sure they are in it" is the thing an operator
// actually wants and a second call is not an error.
func (d *DB) JoinProject(ctx context.Context, userID, project, role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "member"
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO project_members (user_id, project, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, project) DO UPDATE SET role = EXCLUDED.role`,
		strings.TrimSpace(userID), strings.TrimSpace(project), role)
	if err != nil {
		return fmt.Errorf("store: join project: %w", err)
	}
	return nil
}

// EndSession deletes one session. Deleting one that is already gone is not an
// error: logging out twice is not a failure, and saying so would tell a caller
// something about a session it no longer holds.
func (d *DB) EndSession(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: end session: %w", err)
	}
	return nil
}

// EndSessionsFor deletes every session a user has, and answers how many.
//
// This is "sign me out everywhere", and it is one statement because it is the
// thing a person reaches for when they think a session has been taken. It is
// also what a password change should call.
func (d *DB) EndSessionsFor(ctx context.Context, userID string) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("store: end sessions: %w", err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return 0, fmt.Errorf("store: end sessions: %w", err)
	}
	return n, nil
}

// ExpireSessions removes rows that are past their time.
//
// Expired sessions are already refused by UserForSession, so this is
// housekeeping rather than enforcement - which is the order that matters: a
// cleanup that has not run must never be the reason a dead session still
// works.
func (d *DB) ExpireSessions(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: expire sessions: %w", err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return 0, fmt.Errorf("store: expire sessions: %w", err)
	}
	return n, nil
}

// intervalArg renders a duration for postgres.
func intervalArg(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(d/time.Second))
}

// UserByHandle resolves a handle to the person behind it.
//
// Separate from VerifyLogin and never called by it: this one says whether a
// handle exists, which is exactly the question the login door must not answer.
// It is here for the CLI, which is run by somebody holding the DSN and is past
// the point where hiding it would mean anything.
func (d *DB) UserByHandle(ctx context.Context, handle string) (*User, error) {
	var id string
	err := d.sql.QueryRowContext(ctx,
		`SELECT id FROM users WHERE handle = $1`, strings.TrimSpace(handle)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: user by handle: %w", err)
	}
	return d.GetUser(ctx, id)
}

// SetHandle names a person after they exist.
//
// users.handle could be written when the row was CREATED and never again -
// InsertUser inserts it, MintAgent inserts it, and no other statement in this
// store touches it. That was invisible until login started matching on it: the
// operator's row carried a placeholder handle nobody could change, and the only
// way to set a real one was raw SQL by whoever was at the box.
//
// UNIQUE IS A SENTENCE, NOT A CONSTRAINT NAME. The index already refuses a
// duplicate; what a caller needs to hear is which name is taken.
func (d *DB) SetHandle(ctx context.Context, user, handle string) error {
	user, handle = strings.TrimSpace(user), strings.TrimSpace(handle)
	if user == "" {
		return fmt.Errorf("store: a handle belongs to somebody")
	}
	if handle == "" {
		return fmt.Errorf("store: a handle is how a person is addressed and how they log in - " +
			"it cannot be empty")
	}
	var taken string
	err := d.sql.QueryRowContext(ctx,
		`SELECT id FROM users WHERE handle = $1 AND id <> $2`, handle, user).Scan(&taken)
	switch {
	case err == nil:
		return fmt.Errorf("store: %q is somebody else's handle", handle)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("store: set handle: %w", err)
	}

	res, err := d.sql.ExecContext(ctx, `UPDATE users SET handle = $2 WHERE id = $1`, user, handle)
	if err != nil {
		return fmt.Errorf("store: set handle of %s: %w", user, err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return fmt.Errorf("store: set handle of %s: %w", user, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
