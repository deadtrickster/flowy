package flowy

import (
	"context"
	"errors"
	"log"
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

		// TWO WAYS TO BE SOMEBODY, and the bearer is tried first so that
		// nothing an agent does changes. A process sends a header; a person
		// sends a cookie, because the operator asked not to hold a token for a
		// browser - "token is for api, not for me".
		//
		// A cookie resolves to a USER AND NO AGENT, which is exactly right: the
		// person is not a seat. Everything downstream already handles a
		// principal with no agent - that is what an operator's own token has
		// always been - so this adds a way in rather than a kind of caller.
		p, err := s.principalFor(r)
		if errors.Is(err, errNoCredential) {
			permission.Fail("no credential")
			// NAMES BOTH WAYS IN, because there are now two and the caller is
			// one or the other. "missing bearer token" is the whole truth for a
			// script and nonsense to a person whose session ended - they never
			// had a bearer and cannot be told to send one.
			unauthorized(w, "no credential: send a bearer token, or log in")
			return
		}
		if errors.Is(err, errSessionEnded) {
			permission.Fail("session ended")
			unauthorized(w, "your session has ended - log in again")
			return
		}
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

// isOperator reports whether r is the operator - by whatever credential they
// presented.
//
// IT ASKS principalFor RATHER THAN RESOLVING A TOKEN ITSELF, and that is the
// whole of this fix. It used to call bearerToken and give up when there was
// none, so a person who LOGGED IN WITH A PASSWORD - a session cookie and no
// Authorization header - was never the operator. The operator reported it
// against the VMs page: "and I logged in with a password".
//
// That shut every operatorOnly door to every signed-in human at once - all six
// /api/vm/*, /api/agent/socket, the role door, agent projects, the schedules
// check and healthz?counts - and it inverts what the role is FOR. The role was
// moved into the store on 2026-08-18 precisely so operator-ness would be a fact
// about a PERSON rather than about one token; resolving only tokens made it a
// fact about a token again, one layer down.
//
// It still resolves rather than reading the request's principal, because the
// endpoints that ask are the open ones, outside the authenticate mount -
// /healthz is answered whether or not anybody holds a credential. principalFor
// keeps that: no credential is errNoCredential, which is false here, so an open
// door stays open and a health check never turns into a 401.
func (s *server) isOperator(ctx context.Context, r *http.Request) bool {
	if s.operator == "" {
		return false
	}
	p, err := s.principalFor(r)
	if err != nil {
		return false
	}
	if p == nil || p.UserID == "" {
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

// errNoCredential is "nothing was presented", as opposed to "what was
// presented is not known here". They answer differently and a caller acts on
// the difference: one means log in, the other means your session ended.
var errNoCredential = errors.New("no credential")

// errSessionEnded is a cookie that was presented and no longer resolves -
// logged out, expired, or its user deleted. Distinct from errNoCredential
// because the caller does something different: log in again, rather than start
// holding a credential at all.
var errSessionEnded = errors.New("session ended")

// principalFor resolves whoever is making this request.
//
// BEARER FIRST. An agent that also happens to carry a stale cookie - a browser
// tab and a script sharing a profile - is the agent it says it is, and a header
// is an explicit claim where a cookie is one the browser makes on its own.
//
// A SESSION IS NOT A TOKEN and does not become one: it names a user, and the
// Principal it produces carries no agent and no project. Project scoping for a
// person comes from what they ask for, not from a credential, which is the same
// place it comes from for an operator's own token today.
func (s *server) principalFor(r *http.Request) (*store.Principal, error) {
	if token, ok := bearerToken(r); ok {
		return s.db.PrincipalForToken(r.Context(), token)
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return nil, errNoCredential
	}
	user, err := s.db.UserForSession(r.Context(), c.Value)
	if errors.Is(err, store.ErrNotFound) {
		// A COOKIE THAT NO LONGER RESOLVES IS AN ENDED SESSION, and saying
		// "unknown token" to somebody who has never held a token sends them
		// looking for the wrong thing. Measured on a scratch node: log in, log
		// out, send the same id again - 401 "unknown token", which is true
		// about the store and useless to the person reading it.
		//
		// It tells a cookie-holder that their own cookie is dead, which is not
		// a leak - they already have it, and every other answer to it is a 401
		// too.
		return nil, errSessionEnded
	}
	if err != nil {
		return nil, err
	}
	// WHERE THIS PERSON IS WORKING, which until 2026-08-20 was nowhere.
	//
	// A cookie session resolved to a principal with no project at all, so a
	// logged-in person's writes had no home and "switch projects" had nothing to
	// switch - every answer this fleet gave the operator about it was really
	// "paste a different agent's token". The project is a fact about the
	// SESSION: two windows may be in two projects and neither is more true.
	//
	// A FAILURE HERE IS NOT A FAILED REQUEST. The person is authenticated
	// either way; what is unknown is where they are working, and the honest
	// version of that is the empty project - the same state as a session that
	// has not chosen one. Refusing the request instead would turn "I do not
	// know where you are" into "you are nobody".
	project, err := s.db.SessionProject(r.Context(), c.Value)
	if err != nil {
		log.Printf("session project: %v", err)
		project = ""
	}
	return &store.Principal{UserID: user.ID, Project: project, ViaSession: true}, nil
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
