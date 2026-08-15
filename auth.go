package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// principalKey is the context key the middleware hands the principal to the
// handlers under. It is a private type so nothing outside this file can put a
// principal into a request context.
type principalKey struct{}

// authenticate resolves `Authorization: Bearer <token>` to a principal and
// refuses the request when it cannot. It is the only way a principal enters the
// system: a handler that reaches for one and does not find it is a bug, not a
// request to fall back to something more permissive.
//
// The token names a (user, agent, project) triple. Operator is decided here,
// from this node's own configuration, and never from the tokens row - it is a
// property of who runs the node, and the tokens table is not replicated.
func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w, "missing bearer token")
			return
		}
		p, err := s.db.PrincipalForToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			unauthorized(w, "unknown token")
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		if p.UserID == "" && p.AgentID == "" {
			unauthorized(w, "token resolves to no principal")
			return
		}
		p.Operator = s.operator != "" && p.UserID == s.operator

		ctx := context.WithValue(r.Context(), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isOperator reports whether r carries this node's operator's token.
//
// It resolves the token itself because the endpoints that ask are the open
// ones, outside the authenticate mount - /healthz is answered whether or not
// anybody holds a credential, and it has to be. A missing token, an unknown one
// and somebody else's are all simply "no": a health check that answered 401 to
// a typo would be a health check that fails when the node is fine.
func (s *server) isOperator(ctx context.Context, r *http.Request) bool {
	if s.operator == "" {
		return false
	}
	token, ok := bearerToken(r)
	if !ok {
		return false
	}
	p, err := s.db.PrincipalForToken(ctx, token)
	if err != nil {
		return false
	}
	return p.UserID != "" && p.UserID == s.operator
}

// bearerToken pulls the token out of the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// principalOf returns the principal the middleware resolved for r.
func principalOf(r *http.Request) *store.Principal {
	p, _ := r.Context().Value(principalKey{}).(*store.Principal)
	return p
}

// scopeAll reports whether the request asked to bypass the permission filter.
// The answer is only ever yes for this node's operator; for anyone else the
// parameter is simply not there as far as the store is concerned.
//
// Nothing that replicates consults this: it is a local view of a local
// database, not a capability that travels.
func scopeAll(r *http.Request, p *store.Principal) bool {
	return r.URL.Query().Get("scope") == "all" && p != nil && p.Operator
}

func unauthorized(w http.ResponseWriter, why string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="flowy"`)
	writeJSON(w, http.StatusUnauthorized, errorBody(why))
}

func errorBody(msg string) map[string]string { return map[string]string{"error": msg} }
