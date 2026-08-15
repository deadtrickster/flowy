package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Node identity: who a node is, and how another node comes to believe it.
//
// Every replicated row carries the name of the node that wrote it, and until
// Phase 6.5 that name was a claim with nothing behind it. The merge checked
// what the principal handing a row over was allowed to write - and it still
// does - but a peer serving a page could put any node's name on any row,
// rewrite every column, raise the reading and watch last-writer-wins make the
// rewrite permanent everywhere the page reached. Authenticity and authorisation
// are two questions, and this file is the first of them: the node named on the
// row is the node that signed it.
//
// A node's key is made here, on first use, and the private half is never
// anywhere else. What travels is the public half, in a self-signed row, by two
// routes:
//
//   - the operator pins it. `flowy identity pin --node N --key K`, or
//     FLOWY_PEER_KEYS at startup: the operator has the key from the other
//     machine's operator, out of band, and says so. This is authoritative and
//     it is what FLOWY_REQUIRE_PINNED_PEERS narrows a node to.
//   - it arrives on a page and this node has never heard of that node.
//     Trust-on-first-use: the identity is taken, marked unpinned, and held to.
//     A later, different key for a node already here is refused - a key
//     rotation a peer can serve is an impersonation a peer can serve - so the
//     window TOFU leaves open is the first contact and nothing after it.
//
// The second route is what makes a relay work: A pulls from B a page holding
// C's rows, and C's identity rides along in the same page. B cannot alter it,
// because the identity is signed by C over C's own name and key.

// ErrKeyRotation is what an identity for a node this one already knows, under a
// different key, comes back as. It is a named error because it is the one
// identity failure an operator has to act on: either the peer was replaced on
// purpose, and the pin has to be changed by hand on this machine, or somebody
// is trying to become it.
var ErrKeyRotation = errors.New("store: a node's key does not change over the wire")

// NodeIdentity is one node's public half: its name, its ed25519 public key, and
// its own signature over the two. Private keys are not in this struct and never
// cross a wire - the column that holds one is only ever read by the node it
// belongs to.
type NodeIdentity struct {
	NodeID     string `json:"node_id"`
	PublicKey  []byte `json:"public_key"`
	Pinned     bool   `json:"pinned"`
	CreatedHLC int64  `json:"created_hlc,omitempty"`
	Sig        []byte `json:"sig"`
}

// requirePinnedEnv is the deployment that will not take a row from a node its
// operator has not named. It costs transitive relay - a node whose key only
// arrived over the wire is refused, however well its rows verify - which is the
// trade a high-security deployment is making on purpose.
const requirePinnedEnv = "FLOWY_REQUIRE_PINNED_PEERS"

// truthy reads the environment's idea of yes.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RequirePinnedPeers reports whether this node refuses rows from nodes its
// operator has not pinned.
func (d *DB) RequirePinnedPeers() bool { return d.requirePinned }

// SetRequirePinnedPeers turns that rule on or off. Open reads the environment;
// this is for a caller that decides some other way.
func (d *DB) SetRequirePinnedPeers(on bool) { d.requirePinned = on }

// NewIdentity mints a keypair for a node and self-signs it. seed is the 32 byte
// ed25519 seed; a nil seed means a fresh random one. It is a package function
// rather than a method because two callers have no database in hand: `flowy
// identity keygen`, and the gate signing a delta as a node that only exists for
// the length of one check.
func NewIdentity(node string, seed []byte) (*NodeIdentity, ed25519.PrivateKey, error) {
	if node == "" {
		return nil, nil, errors.New("store: an identity needs a node name")
	}
	if seed == nil {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, nil, fmt.Errorf("store: new identity for %s: %w", node, err)
		}
	}
	if len(seed) != ed25519.SeedSize {
		return nil, nil, fmt.Errorf("store: an ed25519 seed is %d bytes, not %d",
			ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	id := &NodeIdentity{NodeID: node, PublicKey: publicOf(priv), Pinned: true}
	id.Sig = signIdentity(priv, id)
	return id, priv, nil
}

// publicOf is the public half of a private key, as plain bytes.
func publicOf(priv ed25519.PrivateKey) []byte {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return append([]byte(nil), pub...)
}

// keyFromColumn reads whatever the private_key column holds: the 32 byte seed
// this node writes, or a full 64 byte key from a store written by hand.
func keyFromColumn(raw []byte) (ed25519.PrivateKey, error) {
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
	default:
		return nil, fmt.Errorf("store: private key is %d bytes, which is neither a seed nor a key",
			len(raw))
	}
}

// Identity is this node's own identity, minting and persisting one the first
// time it is asked for. Everything that writes a replicated row goes through
// signer, which goes through here, so a node that has never had a key gets one
// on its first write rather than on a command somebody has to remember to run.
func (d *DB) Identity(ctx context.Context) (*NodeIdentity, error) {
	if _, err := d.signer(ctx); err != nil {
		return nil, err
	}
	return d.GetIdentity(ctx, d.node)
}

// signer is the local private key, read once and kept. The lock is held across
// the database round trip on purpose: two writers arriving at an empty table
// at the same moment must not each mint a key and each think theirs is the
// node's.
func (d *DB) signer(ctx context.Context) (ed25519.PrivateKey, error) {
	d.keyMu.Lock()
	defer d.keyMu.Unlock()
	if d.priv != nil {
		return d.priv, nil
	}

	var raw []byte
	err := d.sql.QueryRowContext(ctx,
		`SELECT private_key FROM node_identity WHERE node_id = $1`, d.node).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("store: read this node's identity: %w", err)
	case len(raw) > 0:
		priv, err := keyFromColumn(raw)
		if err != nil {
			return nil, err
		}
		d.priv = priv
		return priv, nil
	default:
		// A row for this node's name with no private key in it. That is another
		// node's identity under this node's name - the operator has given two
		// machines the same FLOWY_NODE, or pinned a peer as us - and signing
		// under it would produce rows nobody can verify.
		return nil, fmt.Errorf("store: node %q is in node_identity with somebody else's key: "+
			"this node cannot sign as a name it does not hold the key for", d.node)
	}

	id, priv, err := NewIdentity(d.node, nil)
	if err != nil {
		return nil, err
	}
	id.CreatedHLC, err = d.clock.Pack()
	if err != nil {
		return nil, fmt.Errorf("store: write this node's identity: %w", err)
	}
	seed := priv.Seed()
	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO node_identity (node_id, public_key, private_key, pinned, created_hlc, sig)
		 VALUES ($1, $2, $3, true, $4, $5)
		 ON CONFLICT (node_id) DO NOTHING`,
		id.NodeID, id.PublicKey, seed, id.CreatedHLC, id.Sig)
	if err != nil {
		return nil, fmt.Errorf("store: write this node's identity: %w", err)
	}

	// Somebody else got there first - two processes on one database, which is
	// an ordinary way to run a node - so the key that counts is the stored one.
	var stored []byte
	err = d.sql.QueryRowContext(ctx,
		`SELECT private_key FROM node_identity WHERE node_id = $1`, d.node).Scan(&stored)
	if err != nil {
		return nil, fmt.Errorf("store: read this node's identity: %w", err)
	}
	priv, err = keyFromColumn(stored)
	if err != nil {
		return nil, err
	}
	d.priv = priv
	return priv, nil
}

// SigningKey is this node's private key. It is exported for one caller -
// `flowy sign`, which assembles a delta this node will stand behind - and it
// stays inside this machine: nothing serialises it, no query replication can
// reach selects the column it lives in, and the four Sign* functions take it
// rather than returning it.
func (d *DB) SigningKey(ctx context.Context) (ed25519.PrivateKey, error) {
	return d.signer(ctx)
}

// signIdentity is a node's self-signature: its own name and its own public key,
// under its own key. It is what a relay cannot forge.
func signIdentity(priv ed25519.PrivateKey, id *NodeIdentity) []byte {
	return signBytes(priv, canonicalIdentity(id.NodeID, id.PublicKey))
}

// GetIdentity reads one node's public identity. ErrNotFound when this node has
// never heard of it.
func (d *DB) GetIdentity(ctx context.Context, node string) (*NodeIdentity, error) {
	var id NodeIdentity
	var created sql.NullInt64
	err := d.sql.QueryRowContext(ctx,
		`SELECT node_id, public_key, pinned, created_hlc, sig
		   FROM node_identity WHERE node_id = $1`, node).
		Scan(&id.NodeID, &id.PublicKey, &id.Pinned, &created, &id.Sig)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read the identity of %s: %w", node, err)
	}
	id.CreatedHLC = created.Int64
	return &id, nil
}

// ListIdentities reads every node identity this one holds, public halves only.
// It is what a pull hands over and what `flowy identity list` prints.
func (d *DB) ListIdentities(ctx context.Context) ([]NodeIdentity, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT node_id, public_key, pinned, created_hlc, sig
		   FROM node_identity ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list identities: %w", err)
	}
	defer rows.Close()

	out := []NodeIdentity{}
	for rows.Next() {
		var id NodeIdentity
		var created sql.NullInt64
		if err := rows.Scan(&id.NodeID, &id.PublicKey, &id.Pinned, &created, &id.Sig); err != nil {
			return nil, fmt.Errorf("store: list identities: %w", err)
		}
		id.CreatedHLC = created.Int64
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list identities: %w", err)
	}
	return out, nil
}

// SharableIdentities is what a page hands over: the identities that carry a
// self-signature, which is the only kind another node can check.
//
// An operator's pin has no self-signature - the operator has the peer's public
// key, not its private one - so a pinned row this node has never heard from
// stays here rather than travelling. That is not a gap: it is what a signature
// means. The moment the peer itself turns up on a page, its self-signed
// identity lands beside its rows and can be relayed on from here (see
// applyIdentity, which fills the signature in on a key it already agrees with).
func (d *DB) SharableIdentities(ctx context.Context) ([]NodeIdentity, error) {
	held, err := d.ListIdentities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NodeIdentity, 0, len(held))
	for _, id := range held {
		if len(id.Sig) == 0 {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

// PinIdentity records a peer's key as the operator's own decision: this node
// name, this public key, authenticated somewhere other than here.
//
// A key that is already here and is the same key is pinned rather than
// rewritten, which is how an identity that arrived on a page is promoted to one
// the operator vouches for. A different key is ErrKeyRotation, whether it came
// from the wire or from this call: replacing a pinned key is done by deleting
// the row by hand, on the machine, which is exactly the deliberate act it
// should be.
func (d *DB) PinIdentity(ctx context.Context, node string, public []byte) error {
	if node == "" {
		return errors.New("store: pin an identity: no node named")
	}
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("store: pin %s: an ed25519 public key is %d bytes, not %d",
			node, ed25519.PublicKeySize, len(public))
	}
	held, err := d.GetIdentity(ctx, node)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return err
	case !equalKeys(held.PublicKey, public):
		return fmt.Errorf("%w: %s is already here under another key", ErrKeyRotation, node)
	default:
		_, err := d.sql.ExecContext(ctx,
			`UPDATE node_identity SET pinned = true WHERE node_id = $1`, node)
		if err != nil {
			return fmt.Errorf("store: pin %s: %w", node, err)
		}
		return nil
	}

	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO node_identity (node_id, public_key, pinned, created_hlc)
		 VALUES ($1, $2, true, $3)
		 ON CONFLICT (node_id) DO NOTHING`, node, public, d.clock.Reading().Pack())
	if err != nil {
		return fmt.Errorf("store: pin %s: %w", node, err)
	}
	return nil
}

// equalKeys compares two keys. They are public, so there is nothing here to
// leak by comparing them the ordinary way.
func equalKeys(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// identityOf reads a node's public key inside a transaction: the lookup the
// merge makes of every row it is offered.
func identityOf(ctx context.Context, tx *sql.Tx, node string) (pub []byte, pinned, ok bool, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT public_key, pinned FROM node_identity WHERE node_id = $1`, node).
		Scan(&pub, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("store: read the identity of %s: %w", node, err)
	}
	return pub, pinned, true, nil
}

// applyIdentity merges one node identity. It answers the same three-way the row
// merges do: applied, already here (0 rows and no reason), or refused with the
// reason.
//
// Three rules, and they are the whole of the key distribution story:
//
//   - an identity has to be self-signed. Its own key over its own name and key,
//     so a relay passing it on cannot swap the key inside it.
//   - a node this one has never heard of is taken on trust and marked unpinned.
//     That is the only moment TOFU exists, and it is the one FLOWY_REQUIRE_
//     PINNED_PEERS closes: a deployment that will not take a row from an
//     unpinned node has no business taking the key that would make one
//     verifiable either, so first contact is refused there rather than
//     recorded. Every key that deployment holds is one its operator named.
//   - a node it has heard of keeps the key it has. Same key, nothing to do;
//     different key, refused - including when the row here is only TOFU'd,
//     because the first key seen is the one that node is, and including when
//     the new row is perfectly self-signed, because a key rotation a peer can
//     serve is an impersonation a peer can serve.
func (d *DB) applyIdentity(ctx context.Context, tx *sql.Tx, id *NodeIdentity) (int, string, error) {
	if id.NodeID == "" {
		return 0, "an identity with no node name", nil
	}
	if len(id.PublicKey) != ed25519.PublicKeySize {
		return 0, "identity for " + id.NodeID + " does not carry an ed25519 public key", nil
	}
	if !verifyBytes(id.PublicKey, canonicalIdentity(id.NodeID, id.PublicKey), id.Sig) {
		return 0, "identity for " + id.NodeID + " is not signed by the key it carries", nil
	}

	held, _, ok, err := identityOf(ctx, tx, id.NodeID)
	if err != nil {
		return 0, "", err
	}
	if ok {
		if equalKeys(held, id.PublicKey) {
			// The same key, so there is nothing to decide - except when the row
			// here came from an operator's pin and so has no self-signature on
			// it. Taking the one that just arrived changes no key and is what
			// lets this node relay that identity onward.
			_, err := tx.ExecContext(ctx,
				`UPDATE node_identity SET sig = $2, created_hlc = coalesce(created_hlc, $3)
				  WHERE node_id = $1 AND sig IS NULL`, id.NodeID, id.Sig, id.CreatedHLC)
			if err != nil {
				return 0, "", fmt.Errorf("store: record the signature of %s: %w", id.NodeID, err)
			}
			return 0, "", nil
		}
		return 0, "identity for " + id.NodeID + " carries a different key from the one held here, " +
			"and a node's key does not change over the wire", nil
	}

	// First contact with a node nobody here has named. It is the only trust this
	// file extends on its own, and the deployment that has switched that off
	// gets a refusal rather than a key: taking it would leave a node whose rows
	// are then refused one at a time by authentic, which reads as a peer that
	// half works instead of as a peer the operator has not pinned.
	if d.requirePinned {
		return 0, "identity for " + id.NodeID + " arrived on a page and nobody here pinned it, " +
			"and " + requirePinnedEnv + " is set: pin it with `flowy identity pin`", nil
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO node_identity (node_id, public_key, pinned, created_hlc, sig)
		 VALUES ($1, $2, false, $3, $4)
		 ON CONFLICT (node_id) DO NOTHING`,
		id.NodeID, id.PublicKey, id.CreatedHLC, id.Sig)
	if err != nil {
		return 0, "", fmt.Errorf("store: apply the identity of %s: %w", id.NodeID, err)
	}
	return rowsAffected(res), "", nil
}

// pinFromEnv is FLOWY_PEER_KEYS: "node=key,node=key", the key in hex or base64.
// It is the operator saying, in this machine's configuration, which key belongs
// to which peer - the same decision `flowy identity pin` makes, made at startup
// so a node can be brought up from a unit file with its peers already known.
func (d *DB) PinFromEnv(ctx context.Context, raw string) (int, error) {
	n := 0
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		node, key, found := strings.Cut(entry, "=")
		if !found {
			node, key, found = strings.Cut(entry, ":")
		}
		node, key = strings.TrimSpace(node), strings.TrimSpace(key)
		if !found || node == "" || key == "" {
			return n, fmt.Errorf("store: %q is not a node=key pair", entry)
		}
		public, err := DecodeKey(key)
		if err != nil {
			return n, err
		}
		if err := d.PinIdentity(ctx, node, public); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// requirePinnedFromEnv reads the flag Open applies.
func requirePinnedFromEnv() bool { return truthy(os.Getenv(requirePinnedEnv)) }
