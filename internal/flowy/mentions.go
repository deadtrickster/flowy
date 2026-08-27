package flowy

// @mentions: addressing somebody by writing their name, instead of by
// remembering a flag.
//
// The addressee field has existed since Phase 3 and it works: a waiter armed
// with --to-me wakes for what names it, so an addressed message forces a turn
// and an unaddressed one is ambient. The gap was never the mechanism, it was
// the affordance. `to` is a JSON field and `--to` is a flag, and nobody types a
// flag in the middle of a sentence - so agents, which write their calls
// programmatically, addressed each other constantly and the person in the room
// addressed nobody. Their messages were structurally the least likely in the
// room to be answered, for the same reason the --to-me filter needed the
// saidByAPerson clause in inbox.go: the fleet's habits, not anybody's intent.
//
// So the name in the prose IS the addressing. "@flowy-claude can you look" is
// the same request as `--to flowy-claude`, written the way a person writes.
//
// It resolves into the EXISTING addressee field and invents nothing beside it.
// A second "who this is for" would mean two answers to one question, and every
// reader of the first - the console's ring, the TUI's ->you, the waiter's
// wake-up, the signature that covers it - would have to learn about the second
// or be quietly wrong.

import (
	"context"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// mentionNames pulls the @names out of a message body, in the order they were
// written and without repeats.
//
// The rule is two clauses, and both of them are a case that a naive `@(\w+)`
// gets wrong:
//
// The @ has to START a word. "email@example.com" is not a mention of anybody
// called example - it is an address, people paste them into rooms constantly,
// and a regex that scans for @ followed by word characters turns every one of
// them into a mention. That is the case that would have broken the naive
// version, and it is the first test below.
//
// And a name stops at punctuation. "@name," and "@name." end a clause and a
// sentence; the comma and the full stop are the writer's, not the name's. A
// dot INSIDE a name is kept, because a handle like i.khaprov is a name people
// actually have - so what is trimmed is a trailing run of dots and nothing
// else. Hyphens and underscores are ordinary name bytes: a seeded handle here
// is alice-01J..., and a rule that stopped at the hyphen would resolve half of
// it to nobody.
//
// Nothing here decides whether a name means somebody. That is the store's
// answer, and a name that answers to nobody stays plain text - see
// resolveMentions.
func mentionNames(body string) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(body); i++ {
		if body[i] != '@' {
			continue
		}
		// The @ starts a word, or this is somebody's email address.
		if i > 0 && isNameByte(body[i-1]) {
			continue
		}
		end := i + 1
		for end < len(body) && isNameByte(body[end]) {
			end++
		}
		name := strings.TrimRight(body[i+1:end], ".")
		// Resume after what was scanned either way: an @ with nothing usable
		// after it is not a mention, and re-scanning those bytes cannot make
		// one appear.
		i = end - 1
		if name == "" || len(name) > maxMentionName {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
		// A bound on the work one message can ask the store to do. A body is
		// capped at maxBody, which is a megabyte, and a megabyte of @ signs
		// would otherwise be one query with a hundred thousand names in it.
		// Nobody addresses thirty-two people in one sentence; past that the
		// rest stay plain text, which is what an unresolved name already does.
		if len(out) == maxMentions {
			break
		}
	}
	return out
}

// maxMentionName is the longest thing that can be a name. Handles here are a
// word plus a ULID, so the bound is generous; what it rejects is a run of
// hyphens somebody drew a box with.
const maxMentionName = 64

// maxMentions is how many distinct names one message may address. See the
// bound in mentionNames for why there is one at all.
const maxMentions = 32

// isNameByte reports whether c can appear inside a name: letters, digits, and
// the three joiners that turn up in real handles. ASCII on purpose - a
// multi-byte rune is not a name byte, so a mention ends at one rather than
// splitting it, and every handle this node mints is ASCII.
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.'
}

// mention is one @name that turned out to mean somebody: what was written, and
// the principal it names.
type mention struct {
	Name string
	ID   string
}

// mentionResolver answers what a name means on this node, keyed by the
// lowercase name. It is a function rather than the store itself so that the
// rule about what mentions DO - who a message ends up addressed to - can be
// tested without a database, which is where every reader of the addressee is
// tested too.
type mentionResolver func(names []string) (map[string]string, error)

// resolveMentions is the @names of a body that mean somebody, in the order
// they were written.
//
// A name that resolves to nobody is left alone. People type names that do not
// exist - a nickname, a typo, a person on another node - and refusing the
// message would be the worse failure by a distance: what somebody wrote is
// lost, and the thing they were told off for is a word in a sentence. It stays
// plain text, exactly as it reads today. That is the opposite of the rule for
// an explicit `to`, and deliberately: `to` is a field somebody filled in, so a
// name nothing answers to is a mistake worth refusing at the door, while an @
// in prose is prose.
func resolveMentions(body string, resolve mentionResolver) ([]mention, error) {
	names := mentionNames(body)
	if len(names) == 0 {
		return nil, nil
	}
	found, err := resolve(names)
	if err != nil {
		return nil, err
	}
	out := make([]mention, 0, len(names))
	for _, name := range names {
		if id, ok := found[strings.ToLower(name)]; ok {
			out = append(out, mention{Name: name, ID: id})
		}
	}
	return out, nil
}

// mentionAddressee is who a message with these mentions is for: THE FIRST ONE.
//
// The event carries one addressee, and this takes the first mention rather
// than extending it to a list, on purpose. The addressee is inside the
// signature, it is what a waiter wakes on, and it is read by the TUI, the
// console, the merge and the sync path - so a list would be a change to an
// encoding other nodes are already running and to every reader of the field at
// once, and half of that done wrong is a wake-up somebody never gets or a row
// a peer refuses. The rest are not lost: every resolved mention is on the
// message, in meta, which is what the console draws.
func mentionAddressee(found []mention) string {
	if len(found) == 0 {
		return ""
	}
	return found[0].ID
}

// mentionMeta is how the resolved mentions ride along on the message:
// "name:id" pairs, space separated, in the order they were written.
//
// It is stamped by the node and never taken from the client, like the rest of
// the speaker meta beside it - a client that could write its own would be
// putting somebody else's name and id on a message nobody checked.
//
// The pairs and not just the ids, because the reader that wants this is a
// console drawing the body: it has the words and needs to know which of them
// were really somebody, and which of those is the reader themselves. Neither
// half is guessable from the other - a name is not an id, and the log is full
// of @words that mean nobody. A name cannot contain a space or a colon by
// mentionNames' own rule, and an id is a ULID, so the encoding is unambiguous.
func mentionMeta(found []mention) string {
	pairs := make([]string, 0, len(found))
	for _, m := range found {
		pairs = append(pairs, m.Name+":"+m.ID)
	}
	return strings.Join(pairs, " ")
}

// principalsNamed is the store's answer, as chat.go hands it to the parser.
func (s *server) principalsNamed(ctx context.Context) mentionResolver {
	return principalsNamedBy(ctx, s.db)
}

// principalsNamedBy is the same resolver for a caller that has a database and no
// server, which is what the say path is now that two doors share it - see
// sayInRoom. One resolver is what makes "@alice" mean the same principal
// whichever door the message came through.
func principalsNamedBy(ctx context.Context, db *store.DB) mentionResolver {
	return func(names []string) (map[string]string, error) {
		return db.PrincipalsNamed(ctx, names)
	}
}
