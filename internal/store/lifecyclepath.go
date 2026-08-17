package store

import (
	"fmt"
	"strings"
)

// THE LIFECYCLE PATH: which moves are legal from where, and what a reader may do
// next.
//
// The vocabulary was already closed - todo, active, done, and NormalizeTodoStatus
// refuses anything else. What was missing is the EDGES. A closed set of states
// with every transition allowed is not a lifecycle, it is three words: an item
// could go from done straight back to active with nothing recording that it had
// been reopened, and nothing could answer the question a reader actually has,
// which is "what can I do with this now".
//
// The operator asked for the details panel to show a CLEAR LIFECYCLE PATH beside
// the title and body, and tied it to the kind ontology - bug, feature, chore,
// question - because we are building a tracker for AGENTS AND PEOPLE. Both halves
// of that phrase are load-bearing and they want the same data read two ways:
//
//	A PERSON reads POSITION - where is this, where has it been.
//	AN AGENT reads LEGAL NEXT MOVES - what may I do without guessing.
//
// So Next() is the function the panel draws and the drainer branches on. One
// table, two readers, no second answer to disagree with the first.
//
// ONE PATH NOW, KEYED BY KIND. The operator described per-kind paths - a bug does
// not move through the same states as a question - and they are right that it
// will come. The table is keyed by kind from the start so that a second path
// costs a row rather than a rewrite, but only the default is filled in, because
// a path invented before anybody has walked it is a guess with a schema around
// it. When a kind demonstrably needs its own, it gets an entry here and nothing
// else changes.
//
// WHY THE MOVES ARE THESE MOVES. Each edge below is something somebody did today
// and had to be able to do:
//
//   - todo -> active: taken up. The commonest move and the one nobody was making,
//     which is how the operator ended up asking why only two items were active.
//   - todo -> done: finished without ever being claimed. Half the work tonight
//     went this way - somebody just did it - and a lifecycle that refuses this
//     forces a lie in the log.
//   - active -> done: finished by whoever was carrying it.
//   - active -> todo: put back down. Not a failure state, and it must not need
//     an apology: an agent that cannot hand work back holds it forever.
//   - done -> todo: reopened, because it was not finished after all. This happened
//     to a todo of mine today and the queue had no way to say it.
//   - done -> active: reopened AND taken up in one move, by whoever noticed.
//
// What is deliberately NOT here: nothing. Every pair is legal except the ones
// that are not pairs at all. That is worth saying plainly rather than hiding
// behind a table - THE VALUE HERE IS NOT REFUSAL, IT IS THAT THE MOVES ARE NAMED
// AND ANSWERABLE. If a path later needs to forbid an edge, this is where it goes,
// and the refusal will have somewhere to be.
var lifecyclePaths = map[string]map[string][]string{
	// The default path, walked by every kind until one earns its own.
	"": {
		TodoStatus:   {ActiveStatus, DoneStatus},
		ActiveStatus: {DoneStatus, TodoStatus},
		DoneStatus:   {TodoStatus, ActiveStatus},
	},
}

// pathFor returns the transition table this kind walks: its own if it has one,
// the default otherwise.
func pathFor(kind string) map[string][]string {
	if p, ok := lifecyclePaths[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return p
	}
	return lifecyclePaths[""]
}

// NextStatuses is what may be done to an item in this state, in the order a
// reader should be offered them - the likeliest move first, so a panel can draw
// them as buttons and a drainer can take the head without ranking them itself.
//
// An unrecognised state answers with the whole vocabulary rather than with
// nothing. A row written before any of this existed, or by a peer running older
// code, must not be a dead end: refusing to offer any move at all would strand
// it permanently, and stranded is worse than loose.
func NextStatuses(kind, from string) []string {
	at := strings.ToLower(strings.TrimSpace(from))
	if at == "" {
		at = TodoStatus
	}
	if next, ok := pathFor(kind)[at]; ok {
		out := make([]string, len(next))
		copy(out, next)
		return out
	}
	out := make([]string, 0, len(QueueStatuses))
	for _, s := range QueueStatuses {
		if s != at {
			out = append(out, s)
		}
	}
	return out
}

// CheckTransition says whether this move is one the path allows.
//
// Moving an item to where it already is is ALLOWED and is not a move: a caller
// that retries, or two agents that both close the same finished thing, must not
// get an error for agreeing with reality. Idempotence is the difference between
// a lifecycle and a tripwire.
//
// The refusal names the moves that ARE available, because a refusal that only
// says no leaves the caller to guess, and guessing is what this table exists to
// end.
func CheckTransition(kind, from, to string) error {
	at := strings.ToLower(strings.TrimSpace(from))
	if at == "" {
		at = TodoStatus
	}
	want, err := NormalizeTodoStatus(to)
	if err != nil {
		return err
	}
	if want == at {
		return nil
	}
	for _, allowed := range NextStatuses(kind, at) {
		if want == allowed {
			return nil
		}
	}
	return fmt.Errorf("a %s cannot go from %q to %q: from %q the moves are %s",
		kindWord(kind), at, want, at, strings.Join(NextStatuses(kind, at), ", "))
}

// LifecyclePath is the whole path for one kind, for a panel that wants to draw
// where an item has been and where it can go rather than only the next step.
// States is the path in walking order, which is what makes it a PATH on screen
// rather than a set of buttons.
type LifecyclePath struct {
	Kind   string              `json:"kind"`
	At     string              `json:"at"`
	States []string            `json:"states"`
	Next   []string            `json:"next"`
	Moves  map[string][]string `json:"moves"`
}

// PathOf is what the details panel draws beside the title and the body.
func PathOf(a *Artifact) LifecyclePath {
	kind, at := "", TodoStatus
	if a != nil {
		kind, at = a.Kind, TodoStatusOf(a)
	}
	moves := map[string][]string{}
	for _, s := range QueueStatuses {
		moves[s] = NextStatuses(kind, s)
	}
	return LifecyclePath{
		Kind: kind,
		At:   at,
		// Walking order, not the vocabulary's own order: a person reads a path
		// left to right and QueueStatuses happens to start at active.
		States: []string{TodoStatus, ActiveStatus, DoneStatus},
		Next:   NextStatuses(kind, at),
		Moves:  moves,
	}
}

// kindWord keeps the refusal readable for an item whose kind is empty, which is
// every row written before kinds were a thing.
func kindWord(kind string) string {
	if k := strings.TrimSpace(kind); k != "" {
		return k
	}
	return "queue item"
}
