package flowy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// withPrincipal puts a principal on a request the way the auth middleware does,
// so the note is asked the same question it is asked in production.
func withPrincipal(r *http.Request, p *store.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// A 404 says which projects it searched, and says the same thing either way.
//
// 01M17RVV777776424HGXJZC46M. A row a credential cannot reach answers
// "no such todo: <id>", which is also what a deleted row and a typo answer.
// flowy-claude measured it by varying only the token against one row - 200 for
// one credential, 404 for another - and concluded for a minute that a row they
// had written had been deleted.
//
// THE PROPERTY UNDER TEST IS THAT THE SENTENCE DOES NOT DEPEND ON THE ROW. That
// is what makes this safe to say: a refusal that appeared only when the row
// existed would tell an unauthorised caller which ids are real, which is why
// this is not the 403 the row suggested.
func TestA404SaysWhereItLooked(t *testing.T) {
	s := &server{}

	with := func(p *store.Principal) string {
		r := httptest.NewRequest("GET", "/api/todo/01M0000000000000000000000/notes", nil)
		return s.scopeNote(withPrincipal(r, p))
	}

	t.Run("it names the projects the credential reads", func(t *testing.T) {
		note := with(&store.Principal{UserID: "u", Project: "pa"})
		if !strings.Contains(note, "pa") {
			t.Fatalf("the refusal does not say which project it searched: %q", note)
		}
	})

	t.Run("it names every project in reach, not just the current one", func(t *testing.T) {
		note := with(&store.Principal{UserID: "u", Project: "flowy", Projects: []string{"Lab"}})
		if !strings.Contains(note, "flowy") || !strings.Contains(note, "Lab") {
			t.Fatalf("a credential reaching two projects was told about one: %q", note)
		}
	})

	// THE ONE THAT MATTERS. A caller whose credential reaches nothing gets the
	// most useful sentence of all, because every row will answer this way and
	// nothing about the id is the problem.
	t.Run("a credential reaching nothing is told so", func(t *testing.T) {
		note := with(&store.Principal{UserID: "u"})
		if !strings.Contains(note, "no project") {
			t.Fatalf("a credential that reads nothing was not told that: %q", note)
		}
	})

	// AND IT IS NOT AN EXISTENCE ORACLE. scopeNote is a function of the
	// PRINCIPAL and never of the row, so two different ids asked by one
	// credential produce identical sentences - which is the whole reason this
	// stayed a 404.
	t.Run("the sentence does not vary with the id", func(t *testing.T) {
		p := &store.Principal{UserID: "u", Project: "pa"}
		a := s.scopeNote(withPrincipal(
			httptest.NewRequest("GET", "/api/todo/01M0000000000000000000001/notes", nil), p))
		b := s.scopeNote(withPrincipal(
			httptest.NewRequest("GET", "/api/todo/01M0000000000000000000002/notes", nil), p))
		if a != b {
			t.Fatalf("the refusal differs by id, which tells a caller which ids are real:\n  %q\n  %q", a, b)
		}
	})
}
