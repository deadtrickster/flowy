package store

import "strings"

// THE WORDS A CALLER USES FOR ITSELF.
//
// "me" is what a person types and what several of this fleet's own scripts
// send, and until now the door stored it verbatim: a row claimed with
// {"assignee":"me"} read back assignee=me, and the board grew a seat called
// "me" holding one row that no roster could explain. Measured on 18 Aug, in a
// sweep somebody else ran: "unassigned 21, claude-host 1, orchestrator 3,
// flowy-claude 1, flowy-glm 1, 'me' 1".
//
// It matters past the cosmetics. Every coordination question here is "who has
// this", and the answer was a word rather than a principal: a steal request, a
// nag, a hand-off and a presence check all resolve a handle against the roster,
// and none of them resolve "me". The row looked owned and was unreachable.
//
// RESOLVED, NOT REFUSED. Refusing would move the breakage to every caller that
// already sends it, and the TUI has used "me" for the operator's own token
// since it was written - see operatorSelf. Resolving keeps them all working and
// makes the stored value a real handle.
//
// The list is deliberately short. "me", "self" and "mine" are what somebody
// reaches for; anything longer is guessing at words nobody has typed, and each
// guess is a handle a real seat can no longer have.
func SelfName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "me", "self", "mine":
		return true
	}
	return false
}
