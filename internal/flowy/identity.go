package flowy

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// The operator's side of row signing: what this node's key is, whose keys it
// holds, and how a key gets in.
//
// Key distribution is deliberately two things at once. The authoritative one is
// a pin: the operator of node A reads A's public key off A, hands it to the
// operator of node B by whatever channel two people running two machines
// already trust, and B pins it. Nothing over the wire can change a pinned key.
// The convenient one is trust-on-first-use, which is what makes a relay work
// without every operator having met every node - and which
// FLOWY_REQUIRE_PINNED_PEERS turns off for a deployment that will not have it.
//
// `flowy sign` is the third command here and it is not an operator's tool so
// much as a test's: it signs a delta with a key, which is what lets the gate
// hand a node rows it will take, and - by signing with the wrong key on purpose
// - rows it must refuse.

// identityTimeout bounds one of these commands. They are one or two statements.
const identityTimeout = 30 * time.Second

const identityUsage = `flowy identity - this node's key, and the keys it holds

usage:
  flowy identity                       print this node's node id and public key
  flowy identity list                  print every node identity this node holds
  flowy identity pin --node N --key K  record a peer's public key, out of band
  flowy identity keygen [--seed HEX]   mint a keypair without touching a database

Every replicated row carries the signature of the node that wrote it. A node
will not merge a row from a node whose public key it does not hold, so two
nodes that federate exchange keys once - by pin, or on the first page they
swap - and never again: a second, different key for a node already known is
refused rather than applied.
`

// identityCmd is `flowy identity ...`.
func identityCmd(args []string) error {
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "show":
		return identityShow(args)
	case "list":
		return identityList(args)
	case "pin":
		return identityPin(args)
	case "keygen":
		return identityKeygen(args)
	case "help", "-h", "--help":
		fmt.Print(identityUsage)
		return nil
	default:
		return fmt.Errorf("unknown identity command %q\n\n%s", sub, identityUsage)
	}
}

// withIdentityDB opens the node's store for one of these commands.
//
// --node names this node, except in `identity pin`, where it names the peer
// being pinned and this node's name comes from the environment: a command that
// is about somebody else's key should spell that name in the obvious place.
func withIdentityDB(fs *flag.FlagSet, args []string, fn func(context.Context, *store.DB) error) error {
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	local := envOr("FLOWY_NODE", defaultNode())
	var named *string
	if fs.Lookup("node") == nil {
		named = fs.String("node", local, "name of this node")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if named != nil {
		local = *named
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()

	db, err := store.Open(ctx, *dsn, local)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(ctx, db)
}

// identityShow prints this node's own identity, minting it if this node has
// never signed anything yet. The public key is what an operator copies to the
// other machine.
func identityShow(args []string) error {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	return withIdentityDB(fs, args, func(ctx context.Context, db *store.DB) error {
		id, err := db.Identity(ctx)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"node":       id.NodeID,
			"public_key": store.EncodeKey(id.PublicKey),
			"pinned":     id.Pinned,
		})
	})
}

// identityList prints every identity this node holds - its own, the peers its
// operator pinned, and the ones that arrived on a page - so that "who would
// this node believe" is a question with an answer.
func identityList(args []string) error {
	fs := flag.NewFlagSet("identity list", flag.ContinueOnError)
	return withIdentityDB(fs, args, func(ctx context.Context, db *store.DB) error {
		held, err := db.ListIdentities(ctx)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(held))
		for _, id := range held {
			out = append(out, map[string]any{
				"node":       id.NodeID,
				"public_key": store.EncodeKey(id.PublicKey),
				"pinned":     id.Pinned,
				"self":       id.NodeID == db.Node(),
			})
		}
		return printJSON(map[string]any{"node": db.Node(), "identities": out})
	})
}

// identityPin is the operator saying, on this machine, that a node name belongs
// to a key. It refuses to replace a key that is already here: a peer that has
// genuinely been rebuilt with a new key is a row somebody deletes by hand,
// which is the deliberate act replacing an identity should be.
func identityPin(args []string) error {
	fs := flag.NewFlagSet("identity pin", flag.ContinueOnError)
	node := fs.String("node", "", "the peer's node id, as it appears in the node column of its rows")
	key := fs.String("key", "", "the peer's ed25519 public key, hex or base64")
	return withIdentityDB(fs, args, func(ctx context.Context, db *store.DB) error {
		if *node == "" || *key == "" {
			return errors.New("pin needs --node and --key")
		}
		public, err := store.DecodeKey(*key)
		if err != nil {
			return err
		}
		if err := db.PinIdentity(ctx, *node, public); err != nil {
			return err
		}
		exposed, rows, err := exposedByPinning(ctx, db)
		if err != nil {
			return err
		}
		if exposed > 0 {
			fmt.Fprintf(os.Stderr,
				"pinned %s, and this node now takes its word about %d principal(s) "+
					"here that hold no signing key - %d rows already carry those names. "+
					"A pinned peer may author rows under any of them and this node will "+
					"store them and show them to that person in their own room. "+
					"Run `flowy principal exposed`: it names every one of them with the "+
					"command that closes it. Minting the keys is the way out and "+
					"un-pinning is not - the log is append-only, so what a pin let in "+
					"stays in.\n",
				*node, exposed, rows)
		}
		return printJSON(map[string]any{
			"pinned": *node, "public_key": store.EncodeKey(public),
			"exposed_principals": exposed, "exposed_rows": rows,
		})
	})
}

// exposedByPinning is what a pin costs, counted at the moment it is paid.
//
// The list itself is not new - `flowy principal exposed` has printed it since
// the finding was written down - and printing a list nobody reads at the moment
// it matters is how it stayed unread. Pinning is that moment: it is the single
// act that turns a peer's signature into this node's word about who wrote what,
// and until now it said nothing about whose names it was doing that for.
//
// It WARNS rather than refusing, and the reason is a measured one rather than a
// preference. An accept-path rule of this shape was built and gated red at 31
// checks: holding a token here is true of both machines of one person, so any
// rule that refuses a relayed row on that ground refuses ordinary two-machine
// federation. Refusing the pin is the same choice one door further out - it
// would break standing two nodes up before their principals have keys, which is
// the order the fixture and every real setup does it in. So the door states the
// trade and the operator makes it. See internal/store/unkeyed.go.
func exposedByPinning(ctx context.Context, db *store.DB) (principals, rows int, err error) {
	open, err := db.UnkeyedPrincipals(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, u := range open {
		rows += u.Rows
	}
	return len(open), rows, nil
}

// identityKeygen mints a keypair and prints both halves. It touches no
// database: it is for standing a node up before it has one, and for a test that
// needs to be some other node for the length of one check.
func identityKeygen(args []string) error {
	fs := flag.NewFlagSet("identity keygen", flag.ContinueOnError)
	node := fs.String("node", "", "the node id the key is for")
	seedHex := fs.String("seed", "", "32 byte ed25519 seed in hex; a fresh random one by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *node == "" {
		return errors.New("keygen needs --node")
	}
	seed, err := decodeSeed(*seedHex)
	if err != nil {
		return err
	}
	id, priv, err := store.NewIdentity(*node, seed)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{
		"node":       id.NodeID,
		"public_key": store.EncodeKey(id.PublicKey),
		"seed":       hex.EncodeToString(priv.Seed()),
	})
}

// decodeSeed reads the --seed flag: hex, or empty for a random one.
func decodeSeed(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	seed, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("--seed is hex: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("--seed is %d bytes; an ed25519 seed is %d",
			len(seed), ed25519.SeedSize)
	}
	return seed, nil
}

const signUsage = `flowy sign - sign a replication delta

usage:
  flowy sign [--seed HEX --node N]     read a delta on stdin, write it signed on stdout
  flowy sign [--dsn DSN --node N]      the same, with this node's own stored key
  flowy sign --identity ...            and put the signer's own identity on it

A delta a node will merge carries, on every row, the signature of the node named
in that row's node column. Rows written through the API are signed as they are
written; this is for the ones that are not - a delta assembled by hand, and the
checks that hand a node rows signed by the wrong key on purpose.

The node column is left exactly as it arrived. Signing is not the place to
decide who wrote a row.
`

// signCmd signs a delta read from stdin.
func signCmd(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(signUsage) }
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node")
	seedHex := fs.String("seed", "",
		"sign with the key this 32 byte hex seed makes, instead of this node's stored key")
	withIdentity := fs.Bool("identity", false,
		"put the signing node's own self-signed identity on the delta, the way a page from "+
			"that node carries it")
	as := fs.String("as", "",
		"also sign, as this principal, every row of the delta that names them as its author")
	principalSeedHex := fs.String("principal-seed", "",
		"32 byte hex seed of that principal's key; --as names who, this is what signs")
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxSyncBody))
	if err != nil {
		return fmt.Errorf("read the delta on stdin: %w", err)
	}
	var set store.SyncSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("the delta on stdin is not a sync set: %w", err)
	}

	priv, err := signingKey(*dsn, *node, *seedHex)
	if err != nil {
		return err
	}
	store.SignSet(priv, &set)
	// The authorship signature goes on after the node one, because it is made
	// over the row as it will travel - dates and all - and SignSet is what
	// dates a row that arrived without one.
	if err := signAuthorship(&set, *as, *principalSeedHex); err != nil {
		return err
	}
	if *withIdentity {
		id, _, err := store.NewIdentity(*node, priv.Seed())
		if err != nil {
			return err
		}
		// Unpinned: what travels is a claim about a key, and whether the
		// receiving node has been told to trust that name is its own business.
		id.Pinned = false
		set.Identities = append(set.Identities, *id)
	}

	out, err := json.Marshal(&set)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// signAuthorship puts a principal's own signature on the rows of a delta that
// name them as their author - the second of the two signatures a row can carry.
//
// It is `flowy sign --as P --principal-seed S`, and it is the same tool as the
// rest of this file: it makes a delta a node will take, and - by signing as the
// wrong principal, or over words that were then changed - the deltas a node
// must refuse.
func signAuthorship(set *store.SyncSet, as, seedHex string) error {
	if as == "" && seedHex == "" {
		return nil
	}
	if as == "" || seedHex == "" {
		return errors.New("--as and --principal-seed go together: one names the author, " +
			"the other is the key that signs")
	}
	seed, err := principalSeed(seedHex)
	if err != nil {
		return err
	}
	priv, err := store.PrincipalKeyFromSeed(seed)
	if err != nil {
		return err
	}
	for _, e := range set.Events {
		if e.Actor == as {
			store.SignEventAs(priv, as, e)
		}
	}
	for _, a := range set.Artifacts {
		if a.OwnerUser == as {
			store.SignArtifactAs(priv, as, a)
		}
	}
	return nil
}

// signingKey is the key `flowy sign` signs with: the one a seed makes, or the
// one this node holds in its own store.
func signingKey(dsn, node, seedHex string) (ed25519.PrivateKey, error) {
	if seedHex != "" {
		seed, err := decodeSeed(seedHex)
		if err != nil {
			return nil, err
		}
		_, priv, err := store.NewIdentity(node, seed)
		return priv, err
	}
	if dsn == "" {
		return nil, errors.New("no DSN and no --seed: set DATABASE_URL or pass one of them")
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()

	db, err := store.Open(ctx, dsn, node)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.SigningKey(ctx)
}

// printJSON writes one object per command, so a shell can read the answer back.
func printJSON(body any) error {
	out, err := json.Marshal(body)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
