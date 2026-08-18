package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
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
		// Resolving the token is the first span inside the request's, because
		// "which principal is this" is the question every read below it is
		// narrowed by, and a trace that does not show it shows a query with no
		// reason for the rows it returned.
		//
		// request is kept: it is the context the handler runs under, and it
		// holds the request's own span. Handing the handler this child instead
		// would hand it a span that has already ended - so its queries would
		// hang off nothing, and a handler that moves the request into the trace
		// a handoff arrived in (see adoptThreadTrace) would be moving a span
		// that is already written down.
		request := r.Context()
		ctx, permission := otel.Start(request, otel.KindPermission, "principal.resolve")
		defer permission.End()
		r = r.WithContext(ctx)

		token, ok := bearerToken(r)
		if !ok {
			permission.Fail("no bearer token")
			unauthorized(w, "missing bearer token")
			return
		}
		p, err := s.db.PrincipalForToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			permission.Fail("unknown token")
			unauthorized(w, "unknown token")
			return
		}
		if err != nil {
			permission.Fail("the store could not resolve the token")
			serverError(w, r, err)
			return
		}
		if p.UserID == "" && p.AgentID == "" {
			permission.Fail("token resolves to no principal")
			unauthorized(w, "token resolves to no principal")
			return
		}
		p.Operator = s.operator != "" && p.UserID == s.operator

		// The principal goes onto the request's span as well as into its
		// context: a span is filtered by who it was for, so a span with no
		// principal on it is one nobody but the operator can ever read back.
		actor, _ := chatActor(p)
		permission.SetPrincipal(p.UserID, actor, p.Project)
		permission.Root().SetPrincipal(p.UserID, actor, p.Project)
		permission.OK()
		permission.End()

		next.ServeHTTP(w, r.WithContext(context.WithValue(request, principalKey{}, p)))
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
	if p.UserID == "" {
		return false
	}
	// THE STORE DECIDES, and the env var only bootstraps.
	//
	// This used to be `p.UserID == s.operator` - one id from $FLOWY_OPERATOR,
	// fixed at boot. A second human could then only be given the operator's own
	// token, which is not a second operator: it is the same principal twice, so
	// nothing attributes anything and nothing is revocable separately. That
	// stopped being hypothetical on 2026-08-18, when an agent fell through to
	// the operator's token and its messages were recorded as the operator's,
	// indistinguishable in the store.
	//
	// So the role is a fact about a person. s.operator survives as the
	// BOOTSTRAP only: it names who is operator on a node whose store holds none
	// yet, which is how the first one exists at all. A node with neither has no
	// operator, and the join door is the only way in - which is the correct
	// state for a fresh node.
	if role, err := s.db.RoleOf(ctx, p.UserID); err == nil && role != "" {
		return role == store.RoleOperator
	}
	return p.UserID == s.operator
}

// operatorOnly wraps a handler so that nobody but this node's operator reaches
// it, answering 403 to everyone else.
//
// It is a wrapper rather than a line at the top of each handler because a set
// of routes that all need the same gate is a set where one of them eventually
// does not have it - which is exactly what happened to the mock forge's control
// surface, where some routes checked and the rest answered 200 to any token
// that authenticated at all.
func (s *server) operatorOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOperator(r.Context(), r) {
			writeJSON(w, http.StatusForbidden,
				errorBody("this is the operator's, and you are not the operator"))
			return
		}
		h(w, r)
	}
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
