// ROW LINKS: a pasted row id resolves to the row it names.
//
// The operator, 2026-08-25 (row 01M0XDPFSA7M73): "filing ids are not links".
// Every message in this room carries ULIDs because that is how four agents
// refer to work, and none of them is clickable. A message body renders as GFM
// (web/src/lib/markdown.ts), bare URLs are autolinked, and what is missing is
// one more pattern: a bare row id becomes an anchor to /a/<ulid>, and this
// door turns that anchor into the row's own address.
//
// WHY A RESOLVER RATHER THAN A LOOKUP AT RENDER TIME. A renderer cannot look a
// row up synchronously while producing markup, and batch-resolving ids after
// render costs a request per page and a state the room does not otherwise
// have. One route, no state: the renderer links the strict ULID pattern, this
// door looks the row up and 302s to wherever it lives, and the same route
// fixes a pasted id everywhere else - a row body, a commit message, a note.
//
// WHO THIS DOOR SERVES. A browser navigation carries a session cookie, so a
// cookie seat's click lands here and follows the 302. A token seat carries no
// cookie - its token rides fetches, not navigations - and for those the
// console intercepts the click itself and resolves through GET
// /api/artifact/{id} (web/src/lib/rowlink.ts). Two roads, one filter, because
// the door both of them run is the same ReadArtifact below.
//
// THE SAME PERMISSION FILTER AS READING THE ROW. The redirect does not check
// scope itself - the destination page does - but which row the redirect names
// is decided by the same ReadArtifact the /api/artifact/{id} door runs, so a
// caller cannot use /a/ to learn that a row exists out of their reach. A row
// that cannot be read answers 404, which is the point: /a/<ulid> only ever
// answers 302 or 404.
//
// The pattern is the strict one the client links, spelled again here so the
// two halves cannot drift: 26 characters, 01 plus 24 Crockford base32
// characters, which excludes I, L, O and U.

package main

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/deadtrickster/flowy/internal/store"
)

// rowULID is the shape of a row id worth looking up: anything wider would
// turn arbitrary tokens into dead links, which is worse than no links at all
// (the row's own words). The client's pattern in markdown.ts is this same
// charset with boundary classes; here the whole path value is the id.
var rowULID = regexp.MustCompile(`^01[0-9A-HJKMNP-TV-Z]{24}$`)

// GET /a/{ulid}
func (s *server) handleRowResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("ulid")
	if !rowULID.MatchString(id) {
		// Not even shaped like a row id: the resolver answers about rows, and
		// this is not one. Same status as a missing row - the door only ever
		// answers 302 or 404.
		writeJSON(w, http.StatusNotFound, errorBody("no such row"))
		return
	}
	p := principalOf(r)
	art, err := s.db.ReadArtifact(r.Context(), p, id, scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound,
			errorBody("no such row"+s.misreadIDNote(r, id)))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The row's own address, built the way the console builds it (artifactPath
	// in web/src/lib/api.ts): a missing project or type is "_", never a refusal
	// - a personal row still has a page, and this door's job is to point at it.
	project, typ := "_", "_"
	if art.Project != nil && *art.Project != "" {
		project = *art.Project
	}
	if art.Type != "" {
		typ = art.Type
	}
	http.Redirect(w, r, "/p/"+project+"/"+typ+"/"+art.ID, http.StatusFound)
}
