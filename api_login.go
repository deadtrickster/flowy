package main

// The two doors a person uses, as opposed to the header a process sends.
//
// THEY LIVE OUTSIDE THE AUTHENTICATE MOUNT, like POST /api/join, and for the
// same reason: somebody logging in has no credential yet, and a 401 in front of
// the login form is a door that can only be opened by whoever is already
// through it. They earn that the same way join does - login grants exactly one
// thing, a session for a password that was already correct, and logout grants
// nothing at all.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// sessionCookie is the name the browser sends back.
const sessionCookie = "flowy_session"

// handleLogin exchanges a handle and password for a session cookie.
//
// POST /api/login {handle, password}
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// RATE LIMITED ON THE SAME LIMITER AS join, because this is the second
	// unauthenticated door and the first one already decided what that costs.
	// Password guessing is the attack this exists against, and bcrypt at cost
	// 12 makes each attempt expensive for THIS NODE as well - so the limit is
	// as much about not being a CPU sink as about the guessing.
	if !s.joins.allow("login:"+clientKey(r), time.Now()) {
		writeJSON(w, http.StatusTooManyRequests,
			errorBody("too many login attempts from here - wait a minute"))
		return
	}

	var req struct {
		Handle   string `json:"handle"`
		Password string `json:"password"`
	}
	// Strict, like every other write door: a field this struct does not know is
	// a 400 naming it rather than a value dropped on the floor. `pass` for
	// `password` would otherwise be an empty password and a refusal that reads
	// as a wrong one.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Handle) == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("handle and password are both required"))
		return
	}

	user, err := s.db.VerifyLogin(r.Context(), req.Handle, req.Password)
	if errors.Is(err, store.ErrBadLogin) {
		// ONE SENTENCE FOR BOTH HALVES. Which of the two was wrong is not the
		// caller's business, and telling them is an oracle for which handles
		// exist. 401, not 400: the request was well formed.
		unauthorized(w, "handle or password is wrong")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	session, err := s.db.StartSession(r.Context(), user.ID, r.UserAgent())
	if err != nil {
		serverError(w, r, err)
		return
	}
	http.SetCookie(w, s.sessionCookieFor(r, session.ID, session.Expires))
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    user,
		"expires": session.Expires,
	})
}

// handleLogout ends the session the cookie names.
//
// POST /api/logout
//
// It answers 200 whether or not there was a session to end. "You were not
// logged in" is not a failure a person can act on, and saying it tells an
// unauthenticated caller whether a cookie it holds is live.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.db.EndSession(r.Context(), c.Value); err != nil {
			serverError(w, r, err)
			return
		}
	}
	// Cleared with the same attributes it was set with, or the browser keeps
	// the old one alongside and sends it back.
	clear := s.sessionCookieFor(r, "", time.Unix(0, 0))
	clear.MaxAge = -1
	http.SetCookie(w, clear)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sessionCookieFor builds the cookie, so setting and clearing it cannot
// disagree about its attributes.
//
// HttpOnly: a session the page's own scripts can read is one an injected
// script can post elsewhere, and nothing in this console needs to see it.
//
// SameSite=Lax: the console is the only thing that calls these doors, and Lax
// still sends the cookie on an ordinary navigation - so a link to a room works
// while a cross-site form post does not.
//
// Secure is set only when the request arrived over TLS. This node is dogfooded
// over plain http on a LAN, and a Secure cookie there is one the browser
// accepts and never sends back - a login that appears to succeed and then does
// nothing, which is a worse failure than the one the flag prevents.
func (s *server) sessionCookieFor(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	}
}
