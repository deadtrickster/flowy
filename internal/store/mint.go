package store

// The mint: an agent seat as one atomic operation.
//
// Before this, a seat was four separate writes - user, agent, token, signing
// key - and the only thing composing them was cmd/smoke, a program whose job
// is to fabricate plausible fixtures. Between those two facts sat every
// identity problem this fabric had: an agent minted by a seeder rather than an
// authority, a principal that could speak before it could be signed for, and
// no way to seat anybody new without reaching for the fixture tool.
//
// MintAgent is the composition, in one transaction, refusing on any failure:
// a half-minted principal can talk and cannot be attributed, which is the
// state the accept-side fix made intolerable, and a transaction is the only
// thing that cannot leave it behind.
//
// The key is minted for the AGENT id, because that is the actor its events
// carry - authorEvent signs as e.Actor, and an agent speaks as its agent id.
// The user row exists so the agent inherits a person: the token names no user
// of its own, which is what lets it read its user's personal artifacts.
//
// Who may call this is not the store's question - the doors gate that, and
// every door is the operator's. An agent must not mint an agent, for the same
// flat-depth reason a spawned agent may not spawn VMs: the only account of who
// joined this fabric would be whoever felt like joining it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// MintSpec is a seat to be minted. Handle is the name the agent speaks under
// and what the roster shows; Kind is the runtime it runs on (claude, glm,
// opencode - the column the agent table has always had); Project is its home,
// which must already be declared, exactly as InsertAgent insists.
type MintSpec struct {
	Handle  string
	Kind    string
	Project string
	// AgentKind defaults to worker, as InsertAgent does.
	AgentKind string
}

// Minted is what came out: the ids and the one token. The private key is NOT
// handed back - it lives in principal_identity on this node, and a caller with
// the token speaks and is signed for; nobody needs the raw half, including the
// operator.
type Minted struct {
	User  string
	Agent string
	Token string
	Epoch int64
}

// ErrMintedAlready says a user with this handle already exists, which is the
// one refusal that is about identity rather than about the write: a handle is
// the name the room knows, and a second seat under it would be two voices one
// name.
var ErrMintedAlready = errors.New("store: a user with that handle already has a seat")

// MintAgent seats an agent: user row, agent row, agent token, and the agent's
// signing key with its epoch, in one transaction that refuses rather than
// half-creating.
func (d *DB) MintAgent(ctx context.Context, spec MintSpec) (*Minted, error) {
	spec.Handle = strings.TrimSpace(spec.Handle)
	spec.Project = strings.TrimSpace(spec.Project)
	if spec.Handle == "" {
		return nil, errors.New("store: a seat needs a handle")
	}
	if !AgentKindOK(spec.AgentKind) {
		return nil, ErrBadAgentKind
	}
	if err := requireProject(ctx, d.sql, spec.Project); err != nil {
		return nil, err
	}
	// The handle is the identity a seat is known by, so a collision is refused
	// before anything is written rather than discovered by a unique constraint
	// three rows in.
	var taken string
	err := d.sql.QueryRowContext(ctx,
		`SELECT id FROM users WHERE handle = $1`, spec.Handle).Scan(&taken)
	switch {
	case err == nil:
		return nil, fmt.Errorf("%w: %s", ErrMintedAlready, spec.Handle)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("store: mint %s: handle: %w", spec.Handle, err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("store: key for %s: %w", spec.Handle, err)
	}

	user := &User{Handle: spec.Handle, Display: spec.Handle}
	agent := &Agent{UserID: "", Kind: spec.Kind, AgentKind: spec.AgentKind, Project: spec.Project}
	if agent.AgentKind == "" {
		agent.AgentKind = AgentKindWorker
	}
	// Stamped before the transaction opens: a reading taken inside it is taken
	// back on rollback, and ids that escaped a refused mint would be ids
	// nothing ever owned.
	if err := d.stamp(&user.ID, &user.HLC, &user.Node); err != nil {
		return nil, err
	}
	agent.UserID = user.ID
	if err := d.stamp(&agent.ID, &agent.HLC, &agent.Node); err != nil {
		return nil, err
	}
	token := "t" + strings.ToLower(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return '-'
		}
	}, spec.Handle)) + "-" + ulid.NewString()

	epoch := int64(0)
	out := &Minted{User: user.ID, Agent: agent.ID, Token: token}

	err = d.inTx(ctx, "mint "+spec.Handle, func(tx *sql.Tx) error {
		// The statements below are InsertUser, InsertAgent and InsertToken
		// verbatim, against the transaction - one composition, not a second
		// idea of any of the three.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, handle, display, auto_delegate, hlc, node)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			user.ID, user.Handle, user.Display, user.AutoDelegate, user.HLC, user.Node); err != nil {
			return fmt.Errorf("store: mint %s: user: %w", spec.Handle, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agents (id, user_id, kind, agent_kind, project, hlc, node)
			 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7)`,
			agent.ID, agent.UserID, agent.Kind, agent.AgentKind, agent.Project, agent.HLC, agent.Node); err != nil {
			return fmt.Errorf("store: mint %s: agent: %w", spec.Handle, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tokens (token, user_id, agent_id, project) VALUES ($1, $2, $3, nullif($4, ''))`,
			token, user.ID, agent.ID, spec.Project); err != nil {
			return fmt.Errorf("store: mint %s: token: %w", spec.Handle, err)
		}

		// The epoch from which this agent's rows must carry its signature. The
		// reading is taken now - a fresh principal has nothing behind it, so
		// "from now" and "from always" are the same sentence.
		var err error
		epoch, err = d.clock.Pack()
		if err != nil {
			return fmt.Errorf("store: mint %s: epoch: %w", spec.Handle, err)
		}
		priv := ed25519.NewKeyFromSeed(seed)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO principal_identity (principal, public_key, private_key, epoch_hlc)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (principal) DO NOTHING`,
			agent.ID, priv.Public(), seed, epoch); err != nil {
			return fmt.Errorf("store: mint %s: key: %w", spec.Handle, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Epoch = epoch
	return out, nil
}
