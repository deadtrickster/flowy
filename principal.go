package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// The operator's side of authorship: which principals this node can sign for,
// which it can check, and from when.
//
// It is deliberately the same shape as `flowy identity`, because it is the same
// decision made about a different subject. A node key answers "which machine
// wrote these bytes"; a principal key answers "who wrote this". Pinning a node
// says you will carry what it relays - and a relay carrying other people's rows
// is what federation IS - so it can never also mean that whatever it says about
// who wrote what is true. That was the hole: a pinned peer could write rows
// under any name at all, this node's own people included, and every surface
// rendered them as that person's own word.
//
// Two commands, and between them the whole of the provisioning story:
//
//	keygen  on the node a principal actually writes from. The private half is
//	        written there and nowhere else, and that node signs what they write.
//	pin     on every node that receives their rows. It holds the public half and
//	        an epoch, and from that epoch it refuses a row naming that principal
//	        that does not carry their signature.
//
// Distribution between the two is the operator's, out of band, exactly as a
// node key's is - a principal key that travelled on a page would be an
// authorship a relay could grant itself, which is the thing being closed. It is
// out of scope here and so is rotation: replacing a key is deleting the row by
// hand, on the machine.
//
// BE CLEAR ABOUT WHAT THIS BUYS. Not "forgery is impossible". The trust
// boundary moves from any-pinned-node to the one node holding that principal's
// key: that node can still write anything as them, because that is what holding
// a key means, and a principal with no key here is exactly where every
// principal was before.

const principalUsage = `flowy principal - whose word a row is

usage:
  flowy principal                              print every principal key this node holds
  flowy principal list                         the same
  flowy principal keygen --as P [--epoch N]    mint P a keypair here, and sign P's rows with it
  flowy principal pin --as P --key K [--epoch N]
                                               record P's public key, out of band
  flowy principal repudiate --as P --from N [--project X] [--reason R]
                                               rotate P's key and disown the window it replaces
  flowy principal exposed                      every principal with rows here and no key

A row carries two signatures. The node's says which machine relayed it; the
principal's says who wrote it. From a principal's EPOCH - a clock reading - this
node refuses any row naming them as its author that does not carry a signature
of theirs. Rows below the epoch are taken as they always were and shown as
attributed rather than as that person's own word.

Run keygen on the node a person or agent writes from, and pin on the nodes that
receive their rows. The epoch defaults to this node's clock now, which is what
makes this safe to turn on for one principal at a time on a fabric that is
already running: everything already here stays, and everything after it has to
be signed.
`

// principalCmd is `flowy principal ...`.
func principalCmd(args []string) error {
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "list", "show":
		return principalList(args)
	case "keygen":
		return principalKeygen(args)
	case "repudiate":
		return principalRepudiate(args)
	case "pin":
		return principalPin(args)
	case "exposed", "unkeyed":
		return principalExposed(args)
	case "help", "-h", "--help":
		fmt.Print(principalUsage)
		return nil
	default:
		return fmt.Errorf("unknown principal command %q\n\n%s", sub, principalUsage)
	}
}

// withPrincipalDB opens the node's store for one of these commands. It is
// withIdentityDB's counterpart and takes the same --dsn/--node pair: --node is
// always this node here, because a principal key is not a node's.
func withPrincipalDB(
	fs *flag.FlagSet, args []string, fn func(context.Context, *store.DB) error,
) error {
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()

	db, err := store.Open(ctx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(ctx, db)
}

// principalList prints every principal key this node holds, so that "whose word
// would this node take as their own, and from when" is a question with an
// answer. Private halves are never printed: local says one is here.
func principalList(args []string) error {
	fs := flag.NewFlagSet("principal list", flag.ContinueOnError)
	return withPrincipalDB(fs, args, func(ctx context.Context, db *store.DB) error {
		held, err := db.ListPrincipalKeys(ctx)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(held))
		for _, id := range held {
			out = append(out, map[string]any{
				"principal":  id.Principal,
				"public_key": store.EncodeKey(id.PublicKey),
				"epoch":      id.Epoch,
				"local":      id.Local,
			})
		}
		return printJSON(map[string]any{"node": db.Node(), "principals": out})
	})
}

// principalKeygen mints a principal a keypair on this node. From then on this
// node signs what that principal writes here, and the public key and epoch it
// prints are what the operator pins on the nodes that receive those rows.
func principalKeygen(args []string) error {
	fs := flag.NewFlagSet("principal keygen", flag.ContinueOnError)
	who := fs.String("as", "", "the principal - a user id or an agent id, as it appears in an "+
		"event's actor column")
	seedHex := fs.String("seed", "", "32 byte ed25519 seed in hex; a fresh random one by default")
	epoch := fs.Int64("epoch", 0, "the clock reading from which this principal's rows must carry "+
		"their signature; this node's clock now by default")
	return withPrincipalDB(fs, args, func(ctx context.Context, db *store.DB) error {
		if *who == "" {
			return errors.New("keygen needs --as")
		}
		seed, err := decodeSeed(*seedHex)
		if err != nil {
			return err
		}
		id, err := db.MintPrincipalKey(ctx, *who, seed, *epoch)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"principal":  id.Principal,
			"public_key": store.EncodeKey(id.PublicKey),
			"epoch":      id.Epoch,
			"local":      id.Local,
		})
	})
}

// principalRepudiate is the two halves of recovering from a compromised key,
// done as one act because they are useless apart.
//
// Repudiating without rotating leaves the principal unable to write anything
// anybody will believe, and the stolen key still admissible under the old
// epoch. Rotating without repudiating leaves every row the thief wrote reading
// as authentic, because the authorship check only ever asked whether the
// signature verified - and it did.
//
// ONE CLOCK READING, TAKEN ONCE. The window ends exactly where the new epoch
// begins, and the reading comes back from MintPrincipalKey rather than being
// asked for again here. Reading the clock twice leaves a gap between the last
// disowned row and the first row that must carry the new key - which is
// precisely the moment a thief would want to write in.
//
// A PROJECT IS OPTIONAL AND DOES NOT BOUND WHAT THIS REACHES. It was required,
// because the marking path read repudiations through the permission filter and
// a fabric-wide claim written with no project was therefore readable by its
// author alone. That was the defect rather than the design: a repudiation is a
// fact about a PRINCIPAL, and a principal writes in more than one project, so
// requiring one meant any project the subject forgot kept rendering the thief's
// rows as their own word. The marking path now reads every repudiation this
// node holds - see store.FillDisowned for why that reveals nothing - so this
// flag decides only where the row is filed for people to READ, and one run
// covers the principal everywhere.
func principalRepudiate(args []string) error {
	fs := flag.NewFlagSet("principal repudiate", flag.ContinueOnError)
	who := fs.String("as", "", "the principal disowning their own rows")
	from := fs.Int64("from", 0, "the clock reading the compromise starts at - everything this "+
		"principal signed from here to the new epoch is disowned")
	project := fs.String("project", "", "the project to file it in; optional, and it does not "+
		"limit who the repudiation reaches")
	reason := fs.String("reason", "", "one line for a person reading the row later")
	seedHex := fs.String("seed", "", "32 byte ed25519 seed in hex for the NEW key; random by default")
	return withPrincipalDB(fs, args, func(ctx context.Context, db *store.DB) error {
		if *who == "" || *from <= 0 {
			return errors.New("repudiate needs --as and --from")
		}
		seed, err := decodeSeed(*seedHex)
		if err != nil {
			return err
		}
		// THE READING IS CHECKED BEFORE ANYTHING IS WRITTEN.
		//
		// This check used to sit after the mint, and my own smoke test caught
		// what that costs: a --from in the future was refused, correctly, and
		// left the principal holding a NEW KEY with no repudiation behind it -
		// the half-state the ordering below exists to prevent, produced by the
		// guard against it. A refusal that mutates is not a refusal.
		now, err := db.Clock().Pack()
		if err != nil {
			return err
		}
		if *from > now {
			return fmt.Errorf("the window starts at %d and this node's clock reads %d - "+
				"a window that starts in the future disowns nothing and nothing has been "+
				"changed", *from, now)
		}
		// THE NEW KEY NEXT. If this fails nothing has been claimed and the
		// principal is where they were. The other order would leave a
		// repudiation standing over a window with no key to write after it.
		//
		// ROTATE WHEN THERE IS SOMETHING TO ROTATE, mint when there is not.
		//
		// The first cut of this called MintPrincipalKey alone, which refuses to
		// replace a key in place - so it worked for a principal with no key and
		// refused for one with a key, which is every principal this command
		// exists for. My smoke test used fresh names and never met the case;
		// flowy-claude's browser check met it in its first minute:
		// "a principal's signing key is not replaced in place".
		id, hadEpoch, err := db.RotatePrincipalKey(ctx, *who, seed)
		if errors.Is(err, store.ErrNotFound) {
			// Nothing to replace. Minting is still the right act - the window
			// being disowned was written under no key of their own at all,
			// which is the position every principal is in before their first
			// keygen. There is no old epoch in that case, and the answer says
			// so rather than reporting a zero as a reading.
			id, err = db.MintPrincipalKey(ctx, *who, seed, 0)
			hadEpoch = 0
		}
		if err != nil {
			return err
		}
		if id.Epoch < *from {
			// Belt to the check above: the clock moved backwards between the
			// two reads, which should be impossible and is worth saying rather
			// than writing a window that runs backwards.
			return fmt.Errorf("the new epoch (%d) is before the window starts (%d)",
				id.Epoch, *from)
		}
		title := "rows attributed to " + *who + " in this window are not theirs"
		body := *reason
		if body == "" {
			body = "The key that signed rows in this window is no longer this principal's. " +
				"Rows below the window and from the new epoch are unaffected."
		}
		a := &store.Artifact{
			ID:   ulid.NewString(),
			Type: store.RepudiationType,
			// Nil when no project was named: the claim is about a principal
			// rather than about a project, and a row filed nowhere still marks
			// their rows everywhere.
			Project:    projectOrNil(project),
			OwnerUser:  *who,
			Title:      title,
			Body:       body,
			Visibility: "project",
			Fields: mustFields(map[string]any{
				store.SubjectField: *who,
				store.SpeakerField: store.SpeakerSubject,
				// AS TEXT, because a packed reading does not survive a JSON
				// number: encoding/json decodes into float64, and 1.17e17 is
				// past 2^53 where consecutive integers stop being
				// representable. Written as digits it crosses every encoder
				// unchanged.
				store.FromField: fmt.Sprint(*from),
				// CLOSED AT THE EPOCH, on purpose. A row written at exactly
				// the epoch reading must carry the new key, so including it
				// disowns at most a row that is already refused - and
				// excluding it would leave one reading nobody covers.
				store.ToField: fmt.Sprint(id.Epoch),
			}),
		}
		e := &store.Event{
			Type:    "repudiation",
			Project: a.Project,
			Actor:   *who,
			Body:    "disowned " + fmt.Sprint(*from) + " to " + fmt.Sprint(id.Epoch),
		}
		// The subject speaks, and the key minted a moment ago is what makes
		// that a first-hand claim rather than the node's opinion of one.
		p := &store.Principal{UserID: *who, Project: *project}
		if err := db.WriteRepudiation(ctx, p, a, e); err != nil {
			return err
		}
		out := map[string]any{
			"principal":   id.Principal,
			"public_key":  store.EncodeKey(id.PublicKey),
			"epoch":       id.Epoch,
			"repudiation": a.ID,
			"disowned":    map[string]int64{"from": *from, "to": id.Epoch},
			"pin_this_key": "flowy principal pin --as " + *who + " --key " +
				store.EncodeKey(id.PublicKey) + " --epoch " + fmt.Sprint(id.Epoch),
		}
		// WHERE THE KEY THAT WAS REPLACED BEGAN, when there was one. It is the
		// number that says whether the window covers everything the old key
		// could have signed: a --from later than this leaves rows the stolen key
		// signed still reading as authentic, and nothing else in this answer
		// would show that.
		if hadEpoch > 0 {
			out["replaced_key_epoch"] = hadEpoch
			if *from > hadEpoch {
				out["note"] = fmt.Sprintf(
					"the key you replaced was authoritative from %d and you disowned from %d - "+
						"rows it signed between those readings are not covered", hadEpoch, *from)
			}
		}
		return printJSON(out)
	})
}

// projectOrNil is the project to file under, or nothing.
func projectOrNil(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return p
}

// mustFields is the fields blob for a row this command builds itself.
func mustFields(f map[string]any) []byte {
	raw, err := json.Marshal(f)
	if err != nil {
		// The map is a literal three lines up; a failure here is a programming
		// error rather than a runtime one.
		panic("principal: marshal repudiation fields: " + err.Error())
	}
	return raw
}

// principalPin is the operator saying, on this machine, that a principal id
// belongs to a key and that their rows have had to carry it since a reading. It
// is what makes the refusal bite on the receiving side: a node holding no key
// for somebody has nothing to check a claim about them against.
func principalPin(args []string) error {
	fs := flag.NewFlagSet("principal pin", flag.ContinueOnError)
	who := fs.String("as", "", "the principal whose key this is")
	key := fs.String("key", "", "the principal's ed25519 public key, hex or base64")
	epoch := fs.Int64("epoch", 0, "the clock reading from which their rows must carry it; "+
		"this node's clock now by default")
	return withPrincipalDB(fs, args, func(ctx context.Context, db *store.DB) error {
		if *who == "" || *key == "" {
			return errors.New("pin needs --as and --key")
		}
		public, err := store.DecodeKey(*key)
		if err != nil {
			return err
		}
		if err := db.PinPrincipalKey(ctx, *who, public, *epoch); err != nil {
			return err
		}
		held, err := db.GetPrincipalKey(ctx, *who)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"pinned":     held.Principal,
			"public_key": store.EncodeKey(held.PublicKey),
			"epoch":      held.Epoch,
		})
	})
}

// principalSeed reads `flowy sign --principal-seed`: the key an authorship
// signature on a hand-assembled delta is made with. Empty means the delta
// carries no authorship signature at all, which is what most of them are.
func principalSeed(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	seed, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("--principal-seed is hex: %w", err)
	}
	return seed, nil
}

// principalExposed prints every principal this node has rows from and holds no
// key for - which is every name a pinned peer could author rows under here and
// have them stored and shown as that person's own.
//
// It is the other half of `list`, and it is the half that was missing. `list`
// answers "whose word would this node take as their own", and a fabric that has
// provisioned nothing gets an empty answer that reads like nothing to do. This
// answers "whose word would it take from anybody at all", which on that same
// fabric is everybody, and prints the one command that closes each name.
//
// It exits 0 with an empty list rather than refusing: nothing exposed is a real
// answer and a script should be able to read it. See internal/store/unkeyed.go
// for why the credentialed flag chooses the command and decides nothing else.
func principalExposed(args []string) error {
	fs := flag.NewFlagSet("principal exposed", flag.ContinueOnError)
	return withPrincipalDB(fs, args, func(ctx context.Context, db *store.DB) error {
		open, err := db.UnkeyedPrincipals(ctx)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(open))
		for _, u := range open {
			out = append(out, map[string]any{
				"principal":    u.Principal,
				"handle":       u.Handle,
				"rows":         u.Rows,
				"credentialed": u.Credentialed,
				"fix":          u.Fix(),
			})
		}
		return printJSON(map[string]any{
			"node": db.Node(), "exposed": len(out), "principals": out,
		})
	})
}
