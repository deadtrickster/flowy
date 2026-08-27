package flowy

// WHICH PROJECT AN ANSWER IS ABOUT, said by the answer.
//
// Five list doors - artifacts, the nag, the merge queue, a room read and search
// - each compute for ONE project, the caller's, and none of them said which.
// Measured on the dogfood node: five projects hold rows there today, and every
// one of those answers came back indistinguishable from the same answer about
// any other.
//
// It matters because a project is now a real dimension rather than a label. A
// console with two tabs on two tokens, a script that took its token from an env
// var, a person reading a pasted response - none of them can tell whose board
// they are looking at without a second call to /api/whoami, and nobody makes it.
// The operator asked for multi-project and this is the smallest thing standing
// in the way of reading one: an answer that does not say what it is about.
//
// ONE HELPER RATHER THAN FIVE SPELLINGS, which is the whole reason this is its
// own file. The fleet has been bitten twice this week by two readers rebuilding
// one shape, and five doors each stamping their own idea of "which project" is
// the same defect waiting.

import (
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// answerScope is what every list answer says about its own reach.
type answerScope struct {
	// Project is the one this answer was computed for, empty when the caller is
	// reading across all of them.
	Project string `json:"project,omitempty"`
	// All is true when the answer spans every project this node holds, which
	// only an operator asking for it can produce. It is a separate field rather
	// than a magic project name because "" and "everything" are different
	// answers and a reader must not have to guess which one an empty string is.
	All bool `json:"all_projects,omitempty"`
}

// answerScopeOf says which project an answer covers, for the principal that
// asked.
//
// Not `scopeOf`, which is taken: that one turns a visibility into a scope word
// and lives in mcp_tools.go. The compiler caught the collision here, which is
// the cheap end of the class that has cost this fleet a file, a container, a
// port and an inode in two days - a name is not the thing it names, and two
// things wanting one name is the moment to notice.
func answerScopeOf(r *http.Request, p *store.Principal) answerScope {
	if p == nil {
		// This node's own administration, which reads everything.
		return answerScope{All: true}
	}
	if scopeAll(r, p) {
		return answerScope{All: true}
	}
	return answerScope{Project: p.Project}
}

// stampScope puts the scope on a map-shaped answer.
//
// It writes the fields rather than nesting an object, so that adding it to a
// door does not change the shape of anything already on that answer - a reader
// that ignores it sees exactly what it saw before.
func stampScope(body map[string]any, scope answerScope) map[string]any {
	if scope.All {
		body["all_projects"] = true
		return body
	}
	if scope.Project != "" {
		body["project"] = scope.Project
	}
	return body
}
