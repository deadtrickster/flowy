package flowy

// Resolving a refusal to the row that explains it, once, for every door.
//
// The rule and the storage are in internal/store/knownissue.go; this is the half
// that runs at a door. It is one function rather than one per surface because
// the whole value of the thing is that a refusal looks the same wherever it is
// read: an agent hitting it through MCP, a person hitting it in the console and
// a script hitting it over HTTP are all staring at the same no, and sending them
// to three different renderers is how two of them end up with no pointer.
//
// A LOOKUP THAT FAILS MUST NOT FAIL THE ANSWER. The refusal is already correct
// and already useful without its pointer - that is exactly the state this whole
// feature is an improvement on - so a database error here is logged and dropped,
// and the caller gets what it would have got yesterday. Turning a working
// verdict into a 500 because the footnote could not be fetched would be a strict
// regression, and it would happen precisely on the bad nights.

import (
	"context"
	"log"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// knownIssues resolves a page of refusal codes in one query, for a reader.
//
// It hands back the map rather than the pick, because a caller usually has an
// opinion about precedence that this function cannot have - see PickKnownIssue.
func knownIssues(ctx context.Context, db *store.DB, p *store.Principal, codes []string, scopeAll bool) map[string]*store.KnownIssue {
	if db == nil || len(codes) == 0 {
		return nil
	}
	found, err := db.KnownIssues(ctx, p, codes, scopeAll)
	if err != nil {
		log.Printf("known issue lookup failed, refusals go out without their rows: %v", err)
		return nil
	}
	return found
}

// knownIssueFor is knownIssues for a single refusal, which is what a door
// answering one request has.
//
// It reads the code off the error itself, so a caller does not have to know
// which refusals carry one: an error with no code costs a type assertion and no
// query at all, which is what nearly every refusal in this fabric still is.
func knownIssueFor(ctx context.Context, db *store.DB, p *store.Principal, err error, scopeAll bool) *store.KnownIssue {
	code := store.RefusalCodeOf(err)
	if code == "" {
		return nil
	}
	return store.PickKnownIssue(knownIssues(ctx, db, p, []string{code}, scopeAll), code)
}

// writeRefusal is how a door says no: the status it decided on, the sentence it
// wrote, and the row explaining this refusal when somebody has written one.
//
// One function for all of them so that a refusal looks the same whichever door
// made it, and so that a refusal given a code later starts carrying its row
// without its door being touched. A refusal with no code costs one type
// assertion here and no query, which is what all but the queue's still are.
func (s *server) writeRefusal(w http.ResponseWriter, r *http.Request, status int, err error, msg string) {
	p := principalOf(r)
	issue := knownIssueFor(r.Context(), s.db, p, err, scopeAll(r, p))
	writeJSON(w, status, errorBodyIssue(msg, issue))
}

// errorBodyIssue is errorBody with the pointer attached, for the doors whose
// whole answer is the refusal.
//
// A second constructor rather than a wider errorBody: the plain one is called
// from about eighty places that have no error object to ask, and a signature
// change there would be eighty edits to say nil. The key is `known_issue`, the
// same key the queue reads it under, so a client writes one renderer.
func errorBodyIssue(msg string, issue *store.KnownIssue) map[string]any {
	body := map[string]any{"error": msg}
	if issue != nil {
		body["known_issue"] = issue
	}
	return body
}
