package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Principal identity: whose word a row is, as opposed to which node relayed it.
//
// Every replicated row is signed by the node that wrote it (see identity.go),
// and until this file existed that signature was the only thing behind an
// authorship claim. It is the wrong signature for the question. A node
// signature says "I wrote these bytes"; the actor column says "alice said
// this"; and the merge, having checked the first, believed the second. So a
// peer whose key the operator had pinned - which is what pinning is FOR, a
// relay carrying third parties' rows - could write rows attributed to anybody
// at all, including the receiving node's own people, and every surface here
// rendered them as that person's own word.
//
// The fix is a second key and a second claim:
//
//   - the NODE signature is a relay envelope. "This row passed through me,
//     unaltered." It stays exactly as it was.
//   - the PRINCIPAL signature is authorship. "I wrote this." It is made with a
//     key belonging to the principal named as the author, not to any node.
//
// Two claims, two keys, never conflated. A row that carries the first and not
// the second is a row somebody relayed and nobody signed for, and this node
// says so rather than rendering it as the named person speaking.
//
// THE EPOCH is what makes that deployable rather than a flag day. Each
// principal this node holds a key for carries an epoch: a clock reading, from
// which that principal's rows must be signed. A row naming that principal as
// author whose reading is at or after the epoch and which carries no valid
// principal signature is REFUSED. A row below the epoch is taken as it always
// was, and marked attributed. So a fabric that has been running for months
// keeps every row it has, and the rule bites from the moment an operator
// provisions a key and not one reading earlier.
//
// WHAT THIS BUYS, exactly: the trust boundary moves from any-pinned-node to the
// one node holding that principal's key. It is NOT "forgery is now impossible".
// A node that holds alice's private key can still write anything it likes as
// alice - that is what holding the key means - and a principal with no key here
// is in exactly the position every principal was in before. What is gone is the
// step from "the operator pinned this peer so it may relay" to "this peer may
// put words in anybody's mouth".
//
// Key distribution and rotation are deliberately out of scope here. Keys are
// provisioned locally, by the operator, the way peer node keys already are:
// `flowy principal keygen` on the node a principal writes from, `flowy
// principal pin` on the nodes that receive their rows. Nothing about a
// principal key travels on a page, because a key that a relay can serve is an
// authorship a relay can grant itself, which is the hole this closes.

// The two things a row can say about its authorship, and they are the two
// things a reader is shown. There is no third: either a principal signature
// over this row verified under a key this node holds, or it did not, and
// everything that did not is attributed.
const (
	// AuthorshipAuthored - a principal signature over this row verified here
	// under the key this node holds for the principal named as its author.
	AuthorshipAuthored = "authored"
	// AuthorshipAttributed - this node holds the row and cannot verify that the
	// principal named on it wrote it. It may be perfectly honest; it rests on
	// the word of a node rather than on the author's own key, and every surface
	// says so rather than rendering it as that person's word.
	AuthorshipAttributed = "attributed"
)

// AuthorshipOK reports whether a mark is one of the two. Anything else - a
// column from a store written before this existed, a value a peer put in a
// payload - is attributed, which is the answer that claims least.
func AuthorshipOK(mark string) bool {
	return mark == AuthorshipAuthored || mark == AuthorshipAttributed
}

// authorshipOr is what actually lands in a column: the mark when it is one of
// the two, attributed otherwise. Every write path goes through it, so a claim
// this node did not make itself cannot reach the column.
func authorshipOr(mark string) string {
	if AuthorshipOK(mark) {
		return mark
	}
	return AuthorshipAttributed
}

// PrincipalIdentity is one principal's signing key as this node holds it: the
// public half, the reading from which their rows have to be signed, and whether
// the private half is here.
//
// The private half is never in this struct and never leaves the machine, for
// the same reason a node's is not: the only column that holds one is this
// node's own row, and nothing that replicates selects it.
type PrincipalIdentity struct {
	Principal string `json:"principal"`
	PublicKey []byte `json:"public_key"`
	// Epoch is the packed clock reading from which this principal's rows must
	// carry their own signature. Rows below it are taken on a node's word, as
	// they always were, and marked attributed.
	Epoch int64 `json:"epoch"`
	// Local says this node holds the private half and so signs for this
	// principal when they write here.
	Local bool `json:"local"`
}

// ErrPrincipalKeyRotation is a second, different key for a principal this node
// already holds one for. Rotation is out of scope and this is the refusal that
// says so: replacing a key is deleting the row by hand, on the machine, which
// is the deliberate act it should be.
var ErrPrincipalKeyRotation = errors.New("store: a principal's signing key is not replaced in place")

// MintPrincipalKey makes a principal a keypair on this node and records the
// epoch from which their rows must carry it.
//
// epoch of 0 means "from now": this node's current reading, so that everything
// already written stays acceptable and everything written after this call has
// to be signed. That is the migration seam, and it is the whole reason this can
// be turned on for one principal at a time on a fabric that is already running.
//
// seed may be nil for a fresh random key. It is a parameter because a test that
// needs both halves of a principal's key has to be able to make them.
func (d *DB) MintPrincipalKey(
	ctx context.Context, principal string, seed []byte, epoch int64,
) (*PrincipalIdentity, error) {
	if principal == "" {
		return nil, errors.New("store: a principal key needs a principal")
	}
	if seed == nil {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, fmt.Errorf("store: new key for %s: %w", principal, err)
		}
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("store: an ed25519 seed is %d bytes, not %d",
			ed25519.SeedSize, len(seed))
	}
	if epoch <= 0 {
		at, err := d.clock.Pack()
		if err != nil {
			return nil, fmt.Errorf("store: new key for %s: %w", principal, err)
		}
		epoch = at
	}
	priv := ed25519.NewKeyFromSeed(seed)
	public := publicOf(priv)

	held, err := d.GetPrincipalKey(ctx, principal)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return nil, err
	case !equalKeys(held.PublicKey, public):
		return nil, fmt.Errorf("%w: %s already has one here", ErrPrincipalKeyRotation, principal)
	default:
		// The same key again, which is a keygen run twice. The epoch it already
		// has is the one its rows were written under, so it stands.
		return held, nil
	}

	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO principal_identity (principal, public_key, private_key, epoch_hlc)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (principal) DO NOTHING`, principal, public, seed, epoch)
	if err != nil {
		return nil, fmt.Errorf("store: write the key of %s: %w", principal, err)
	}
	return d.GetPrincipalKey(ctx, principal)
}

// PinPrincipalKey records a principal's public key and epoch as the operator's
// own decision, on a node that does not hold the private half. It is what makes
// the refusal bite on the receiving side: a node that holds no key for alice
// has nothing to check a claim about alice against.
//
// An epoch of 0 means this node's current reading, which is the sane default
// for a node joining a running fabric: everything it already holds stays, and
// everything that arrives from now on has to be signed.
func (d *DB) PinPrincipalKey(
	ctx context.Context, principal string, public []byte, epoch int64,
) error {
	if principal == "" {
		return errors.New("store: pin a principal key: no principal named")
	}
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("store: pin %s: an ed25519 public key is %d bytes, not %d",
			principal, ed25519.PublicKeySize, len(public))
	}
	if epoch <= 0 {
		at, err := d.clock.Pack()
		if err != nil {
			return fmt.Errorf("store: pin %s: %w", principal, err)
		}
		epoch = at
	}
	held, err := d.GetPrincipalKey(ctx, principal)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return err
	case !equalKeys(held.PublicKey, public):
		return fmt.Errorf("%w: %s is already here under another key", ErrPrincipalKeyRotation, principal)
	default:
		// The same key, so this is the operator moving the epoch - which is
		// allowed and is the only thing about a pinned key that ever changes.
		_, err := d.sql.ExecContext(ctx,
			`UPDATE principal_identity SET epoch_hlc = $2 WHERE principal = $1`, principal, epoch)
		if err != nil {
			return fmt.Errorf("store: pin %s: %w", principal, err)
		}
		return nil
	}

	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO principal_identity (principal, public_key, epoch_hlc)
		 VALUES ($1, $2, $3) ON CONFLICT (principal) DO NOTHING`, principal, public, epoch)
	if err != nil {
		return fmt.Errorf("store: pin %s: %w", principal, err)
	}
	return nil
}

// GetPrincipalKey reads one principal's identity. ErrNotFound when this node
// holds no key for them, which is the ordinary case and not an error condition:
// it is a principal whose rows rest on a node's word, as every principal's did
// before this file existed.
func (d *DB) GetPrincipalKey(ctx context.Context, principal string) (*PrincipalIdentity, error) {
	var (
		id      PrincipalIdentity
		private []byte
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT principal, public_key, private_key, epoch_hlc
		   FROM principal_identity WHERE principal = $1`, principal).
		Scan(&id.Principal, &id.PublicKey, &private, &id.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read the key of %s: %w", principal, err)
	}
	id.Local = len(private) > 0
	return &id, nil
}

// ListPrincipalKeys is every principal identity this node holds, public halves
// only. It is `flowy principal list`: "whose word would this node take as their
// own" is a question with an answer.
func (d *DB) ListPrincipalKeys(ctx context.Context) ([]PrincipalIdentity, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT principal, public_key, private_key, epoch_hlc
		   FROM principal_identity ORDER BY principal`)
	if err != nil {
		return nil, fmt.Errorf("store: list principal keys: %w", err)
	}
	defer rows.Close()

	out := []PrincipalIdentity{}
	for rows.Next() {
		var (
			id      PrincipalIdentity
			private []byte
		)
		if err := rows.Scan(&id.Principal, &id.PublicKey, &private, &id.Epoch); err != nil {
			return nil, fmt.Errorf("store: list principal keys: %w", err)
		}
		id.Local = len(private) > 0
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list principal keys: %w", err)
	}
	return out, nil
}

// principalSigner is the private key this node holds for a principal, and false
// when it holds none.
//
// Only the keys it FINDS are cached. A principal with no key here is looked up
// again on the next write on purpose: `flowy principal keygen` is run against a
// running node, and a cached absence would mean the node went on writing
// unsigned rows for that principal until somebody restarted it - which is a
// security fix that silently does not apply, the worst kind.
func (d *DB) principalSigner(ctx context.Context, principal string) (ed25519.PrivateKey, bool, error) {
	if principal == "" {
		return nil, false, nil
	}
	d.authorMu.Lock()
	if priv, ok := d.authors[principal]; ok {
		d.authorMu.Unlock()
		return priv, true, nil
	}
	d.authorMu.Unlock()

	var raw []byte
	err := d.sql.QueryRowContext(ctx,
		`SELECT private_key FROM principal_identity WHERE principal = $1`, principal).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || len(raw) == 0 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: read the signing key of %s: %w", principal, err)
	}
	priv, err := keyFromColumn(raw)
	if err != nil {
		return nil, false, err
	}
	d.authorMu.Lock()
	if d.authors == nil {
		d.authors = map[string]ed25519.PrivateKey{}
	}
	d.authors[principal] = priv
	d.authorMu.Unlock()
	return priv, true, nil
}

// principalKeyOf reads a principal's public key and epoch inside a transaction:
// the lookup the merge makes of every row that names an author.
func principalKeyOf(
	ctx context.Context, tx *sql.Tx, principal string,
) (public []byte, epoch int64, ok bool, err error) {
	if principal == "" {
		return nil, 0, false, nil
	}
	err = tx.QueryRowContext(ctx,
		`SELECT public_key, epoch_hlc FROM principal_identity WHERE principal = $1`, principal).
		Scan(&public, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("store: read the key of %s: %w", principal, err)
	}
	return public, epoch, true, nil
}

// authorshipOf is the one rule, asked of one row: is this row's authorship
// claim one this node will take, and what does it get to say about it?
//
// It is asked of every row that names an author, at both merge doors, and
// whatever principal is carrying the page - including none at all. That is
// deliberate and it is the second half of the hole: the old rule only looked at
// who was vouching when the row named somebody OTHER than the carrier, so a
// node syncing AS the impersonated principal walked straight past it. Authorship
// is not a question about the carrier, so the carrier does not enter into it.
//
// Three answers:
//
//   - this node holds no key for the named author. Nothing to check: the row is
//     taken exactly as it was before any of this existed, and marked attributed,
//     because a row nobody signed for is not that person's word.
//   - the row carries a principal signature that verifies under that key. It is
//     authored, and that is the only way anything is ever marked authored.
//   - it does not, and then the epoch decides. At or after it, the row is
//     REFUSED with a reason. Below it, the row predates the key and is taken,
//     attributed.
//
// A refusal is WRITTEN DOWN, in the same transaction, into withheld_authorship,
// and the two acceptances clear whatever this node recorded about that row
// before. The merge already answers the peer that pushed a forgery with a count
// and a reason; nobody on this side was answered at all - the row was refused, so
// it was not in the log, so a queue read handed back a shorter list and said
// nothing. A refusal nobody sees is indistinguishable from success. See
// WithheldAuthorship, which is how a read says so.
func authorshipOf(
	ctx context.Context, tx *sql.Tx, row withheldRow, msg, sig []byte,
) (mark, why string, err error) {
	author := row.principal
	public, epoch, ok, err := principalKeyOf(ctx, tx, author)
	if err != nil {
		return "", "", err
	}
	if !ok {
		// No key here, so there is no rule by which this node refuses this
		// author's rows and nothing it could be withholding of them. The merge
		// path stays exactly as short as it was for a fabric that has
		// provisioned nothing, which is most of them.
		return AuthorshipAttributed, "", nil
	}
	if mine := verifyBytes(public, msg, sig); mine || row.hlc < epoch {
		// Taken - as this person's own word, or on the relaying node's, which
		// the mark beside it says. Either way this node is no longer withholding
		// it, so it stops saying that it is: "1 row withheld" about a row that
		// has since arrived is the same false statement the other way up.
		if err := clearWithheld(ctx, tx, row); err != nil {
			return "", "", err
		}
		if mine {
			return AuthorshipAuthored, "", nil
		}
		return AuthorshipAttributed, "", nil
	}
	carried := "carries no signature of theirs"
	if len(sig) > 0 {
		carried = "carries a signature that is not theirs"
	}
	why = row.label() + " says " + named(author) + " wrote it and " + carried +
		": this node holds " + named(author) + "'s signing key, and from that key's epoch " +
		"their rows are their own to sign. A node relaying a row is not the author of it - " +
		"pinning the node it came from does not make this one theirs"
	if err := recordWithheld(ctx, tx, row, why); err != nil {
		return "", "", err
	}
	return "", why, nil
}

// The two tables a withheld row would have landed in. They are the ledger's own
// names for them rather than the table names, because what is keyed here is a
// row of the log and not a row of that table - the whole point is that it is not
// in that table.
const (
	withheldArtifact = "artifact"
	withheldEvent    = "event"
)

// WithheldUnverifiedAuthorship is the reason a read reports, in the words a
// person reads. The prose refusal the pushing peer is handed is per row and names
// the principal and the key; this is the one line a COUNT is labelled with, and
// there is exactly one of them because there is exactly one rule.
const WithheldUnverifiedAuthorship = "unverified authorship"

// withheldRow is one refused row as the ledger keeps it: enough to count it and
// to decide who is told about it, and nothing more. See withheld_authorship in
// schema.sql for why the title and the body are deliberately not here.
type withheldRow struct {
	kind      string // withheldArtifact or withheldEvent
	id        string
	principal string
	project   *string
	// visibility is the artifact's own. An event has none, so it is left empty
	// and lands as shared, which makes the reach of the count that event's
	// project - which is what an event's own read rule is.
	visibility string
	// claimed is what the row said it was: an artifact's kind, an event's type.
	claimed string
	node    string
	hlc     int64
}

// label is the row as the refusal names it - "artifact 01M...", "event 01M..." -
// which is the prose both merge doors have always reported.
func (w withheldRow) label() string { return w.kind + " " + w.id }

// recordWithheld writes the refusal down. It is an upsert because a peer retries
// a delta it was refused, and because the merge asks the question again on every
// pass over the rows it has not settled: what is counted is refused ROWS, not
// deliveries and not passes, so a second look at the same row is the same one
// row.
func recordWithheld(ctx context.Context, tx *sql.Tx, row withheldRow, why string) error {
	visibility := row.visibility
	if visibility == "" {
		visibility = VisibilityShared
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO withheld_authorship (row_kind, row_id, principal, project, visibility,
		                                  kind, node, hlc, reason)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (row_kind, row_id) DO UPDATE SET
		     principal = excluded.principal, project = excluded.project,
		     visibility = excluded.visibility, kind = excluded.kind, node = excluded.node,
		     hlc = excluded.hlc, reason = excluded.reason, last_seen = now()`,
		row.kind, row.id, row.principal, row.project, visibility,
		row.claimed, row.node, row.hlc, why)
	if err != nil {
		return fmt.Errorf("store: record what was withheld of %s: %w", row.label(), err)
	}
	return nil
}

// clearWithheld forgets a refusal this node has stopped making. A peer that comes
// back with the author's signature over the same row has answered it.
func clearWithheld(ctx context.Context, tx *sql.Tx, row withheldRow) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM withheld_authorship WHERE row_kind = $1 AND row_id = $2`, row.kind, row.id)
	if err != nil {
		return fmt.Errorf("store: forget what was withheld of %s: %w", row.label(), err)
	}
	return nil
}

// Withheld is what a read says about the rows it could not hand over: how many,
// and why.
//
// It is the count and the reason together and never one without the other. A
// count with no reason is a number nobody can act on; a reason with no count
// reads as a warning about the fabric rather than a statement about THIS answer.
type Withheld struct {
	Rows   int    `json:"rows"`
	Reason string `json:"reason"`
}

// WithheldAuthorship is how much of what this reader asked for this node refused
// on authorship, and nil when the answer is none.
//
// Nil rather than a zero, so a surface with nothing to report renders nothing
// rather than a reassuring "0 withheld" on every page - a page that says 0 every
// day is a page nobody reads the day it says 3 - and so that adding this changed
// no answer that had nothing to say.
//
// The reach is the ARTIFACT READ RULE, asked of the three columns the ledger
// keeps for exactly that: a reader is told about a refusal in the places they
// would have been handed the row, and told nothing about one in a project they
// cannot read. It is the same clause ListArtifacts runs, spliced over the ledger
// instead of over the table, rather than a second idea of who may see what - see
// ReadableProjects, which reads the registry through the same filter for the same
// reason.
//
// The join onto principal_identity is what keeps the count honest rather than
// merely growing: the ledger speaks only while the rule that made it is live. A
// key removed by hand - which is how rotation is done here, deliberately - takes
// its refusals out of every count with it, because from that moment this node
// refuses nothing of that principal's and has no business saying it does.
func (d *DB) WithheldAuthorship(ctx context.Context, p *Principal, scopeAll bool) (*Withheld, error) {
	a := &args{}
	where := ArtifactFilterSQL(p, "ar", a, scopeAll)
	var rows int
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*)
		   FROM (SELECT w.row_id AS id, w.principal AS owner_user, w.project AS project,
		                w.visibility AS visibility
		           FROM withheld_authorship w
		           JOIN principal_identity pi ON pi.principal = w.principal) ar
		  WHERE `+where, a.vals...).Scan(&rows)
	if err != nil {
		return nil, fmt.Errorf("store: count what was withheld: %w", err)
	}
	if rows == 0 {
		return nil, nil
	}
	return &Withheld{Rows: rows, Reason: WithheldUnverifiedAuthorship}, nil
}

// PrincipalKeyFromSeed is a principal's private key from its 32 byte seed, for
// the callers that hold a seed and no database: `flowy sign --as`, and the
// tests that stand in for the node a principal actually writes from.
func PrincipalKeyFromSeed(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("store: an ed25519 seed is %d bytes, not %d",
			ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PrincipalPublicKey is the public half of a principal's private key, as the
// bytes a pin on another node is made from.
func PrincipalPublicKey(priv ed25519.PrivateKey) []byte { return publicOf(priv) }

// PinPrincipalsFromEnv is FLOWY_PRINCIPAL_KEYS: "principal=key[@epoch]", comma
// separated, the key in hex or base64. It is the same decision `flowy principal
// pin` makes, made at startup so a node can be brought up from a unit file with
// the principals it will be receiving rows about already known.
func (d *DB) PinPrincipalsFromEnv(ctx context.Context, raw string) (int, error) {
	n := 0
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		principal, rest, found := strings.Cut(entry, "=")
		if !found {
			return n, fmt.Errorf("store: %q is not a principal=key pair", entry)
		}
		key, epochText, hasEpoch := strings.Cut(rest, "@")
		principal, key = strings.TrimSpace(principal), strings.TrimSpace(key)
		if principal == "" || key == "" {
			return n, fmt.Errorf("store: %q is not a principal=key pair", entry)
		}
		public, err := DecodeKey(key)
		if err != nil {
			return n, err
		}
		var epoch int64
		if hasEpoch {
			if _, err := fmt.Sscan(strings.TrimSpace(epochText), &epoch); err != nil {
				return n, fmt.Errorf("store: %q does not end in a clock reading: %w", entry, err)
			}
		}
		if err := d.PinPrincipalKey(ctx, principal, public, epoch); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
