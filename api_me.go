package main

// A person changes their own name and their own password.
//
// The operator, holding a token and looking at the console: "not doing that cli
// commands - im logged in via token - give me profile panel - i will change my
// password here." Setting a password was a shell verb on the box that holds the
// database, which is right for the FIRST account on a node and wrong for
// everything after it.
//
// PUT /api/me {handle?, password?} - both optional, and what is absent is left
// alone rather than cleared.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

type meRequest struct {
	Handle   string `json:"handle"`
	Password string `json:"password"`
	// Current is the password being replaced. Required only when the caller
	// arrived on a COOKIE and already has one - see below.
	Current string `json:"current"`
}

// handleReadMe is who the caller IS, in the words a person recognises.
//
// GET /api/whoami answers ids - user, agent, project - which is what a client
// needs to reason about permission and useless to a panel that has to render
// "your handle is X". A profile form with nothing to show its current value
// from would either leave the field blank, which reads as "you have no handle",
// or fetch the whole user list to find itself.
//
// It also says whether a password EXISTS, without saying anything about it. The
// panel needs that to choose between "set a password" and "change it", and to
// know whether to ask for the current one - which is the same rule the write
// half applies.
func (s *server) handleReadMe(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.UserID == "" {
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential resolves to an agent and not to a person"))
		return
	}
	user, err := s.db.GetUser(r.Context(), p.UserID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	has, err := s.db.HasPassword(r.Context(), p.UserID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	_, bearer := bearerToken(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         user,
		"has_password": has,
		// HOW THIS REQUEST ARRIVED, because it decides what the form must ask
		// for: a bearer may set a password without naming the old one, a
		// cookie may not. The panel would otherwise have to guess, and a form
		// that asks for a current password nobody has is a form nobody can
		// submit.
		"by_bearer": bearer,
	})
}

func (s *server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.UserID == "" {
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential resolves to an agent and not to a person, so there is no profile to change"))
		return
	}

	var req meRequest
	if err := decodeStrict(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	handle, password := strings.TrimSpace(req.Handle), req.Password
	if handle == "" && password == "" {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"nothing to change - send a handle, a password, or both"))
		return
	}

	// A COOKIE MAY NOT REPLACE A PASSWORD IT CANNOT NAME.
	//
	// A bearer is an API credential: whoever holds it can already act as this
	// person completely, so asking it for the old password protects nothing.
	// A session cookie is different - it is what a stolen browser has - and
	// letting one set a new password without knowing the old one is how
	// somebody is locked out of their own account by a tab they left open.
	//
	// The first password is exempt, because there is no old one to know. That
	// is the case the operator is in tonight.
	if password != "" {
		if _, bearer := bearerToken(r); !bearer {
			has, err := s.db.HasPassword(r.Context(), p.UserID)
			if err != nil {
				serverError(w, r, err)
				return
			}
			if has {
				if req.Current == "" {
					writeJSON(w, http.StatusBadRequest, errorBody(
						"changing a password from a browser needs the current one"))
					return
				}
				user, err := s.db.GetUser(r.Context(), p.UserID)
				if err != nil {
					serverError(w, r, err)
					return
				}
				if _, err := s.db.VerifyLogin(r.Context(), user.Handle, req.Current); err != nil {
					// The same sentence the login door gives, for the same
					// reason: a different one here would say whether the
					// account has a password.
					unauthorized(w, "handle or password is wrong")
					return
				}
			}
		}
	}

	// THE HANDLE FIRST, because the password is stored against the user id and
	// follows the person either way, while a failed rename after a successful
	// password change would leave the caller with a new secret and the old
	// name - two writes, one of which they were not told about.
	if handle != "" {
		if err := s.db.SetHandle(r.Context(), p.UserID, handle); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
				return
			}
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
	}
	if password != "" {
		if err := s.db.SetPassword(r.Context(), p.UserID, password); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
				return
			}
			// The store's sentences say what is wrong with a password - too
			// short, or past the 72 bytes bcrypt reads - and they are better
			// than a restatement.
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
	}

	user, err := s.db.GetUser(r.Context(), p.UserID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// EVERY OTHER SESSION ENDS ON A PASSWORD CHANGE, and the answer says how
	// many. The reason somebody changes a password is usually that they think
	// somebody else has it, and leaving the other browsers logged in means the
	// change did nothing about the thing they were worried about.
	//
	// The caller's own session is ended too, because the alternative is
	// exempting the session that made the request - which is precisely the
	// session an attacker would be using. The console asks them to log in
	// again, which is one form against the account being kept.
	ended := int64(0)
	if password != "" {
		ended, err = s.db.EndSessionsFor(r.Context(), p.UserID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		http.SetCookie(w, s.clearedSessionCookie(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":           user,
		"sessions_ended": ended,
	})
}

// clearedSessionCookie is the logout cookie, built the one way so setting and
// clearing cannot disagree about the attributes.
func (s *server) clearedSessionCookie(r *http.Request) *http.Cookie {
	c := s.sessionCookieFor(r, "", time.Unix(0, 0))
	c.MaxAge = -1
	return c
}
