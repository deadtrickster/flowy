package flowy

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The login doors are outside the token, on the outer mux, exactly like
// POST /api/join - and NOT in the parameter table, because the table describes
// the guarded mux and these are not on it.
//
// Asserted because being registered in the wrong place is silent in both
// directions: inside authenticate, the login form can only be used by somebody
// who is already logged in; missing from serve.go, the console posts into a
// 404 that reads like a wrong password.
func TestTheLoginDoorsAreOutsideTheToken(t *testing.T) {
	src := readSource(t, "serve.go")
	for _, pattern := range []string{"POST /api/login", "POST /api/logout"} {
		if !strings.Contains(src, `mux.HandleFunc("`+pattern+`"`) {
			t.Errorf("serve.go does not register %q on the outer mux", pattern)
		}
		if _, guarded := routeParams[pattern]; guarded {
			t.Errorf("%s is in routeParams, which describes the guarded mux - it is not on it", pattern)
		}
	}
}

// WHAT THE BROWSER IS TOLD TO DO WITH THE COOKIE, asserted because every one of
// these is silent when wrong: a readable cookie is stolen by any injected
// script, a cross-site-sendable one is a CSRF, and a Secure cookie on a plain
// http LAN node is accepted by the browser and never sent back - a login that
// succeeds and then does nothing at all.
func TestTheSessionCookieIsHttpOnlyLaxAndSecureOnlyOnTLS(t *testing.T) {
	s := &server{}
	plain := httptest.NewRequest("POST", "http://192.168.1.55:8787/api/login", nil)
	c := s.sessionCookieFor(plain, "abc", time.Now().Add(time.Hour))
	if !c.HttpOnly {
		t.Error("the session cookie is readable by page scripts")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite is %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("the cookie's path is %q, so it is not sent to every route", c.Path)
	}
	if c.Secure {
		t.Error("Secure on a plain-http request - the browser would keep it and never send it back")
	}

	tls := httptest.NewRequest("POST", "https://flowy.example/api/login", nil)
	tls.TLS = &tlsState
	if got := s.sessionCookieFor(tls, "abc", time.Now().Add(time.Hour)); !got.Secure {
		t.Error("no Secure on a TLS request - the cookie would go out over plain http on a downgrade")
	}
}

// Logging out with no cookie is 200 and clears anyway.
//
// It must not 401: an unauthenticated caller learning whether its cookie is
// live is the same oracle the login door refuses to be, and a person clicking
// logout on a tab whose session already ended is not reporting a fault.
func TestLogoutWithNoCookieSaysOkAndClears(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	s.handleLogout(w, httptest.NewRequest("POST", "/api/logout", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("no expiring %s cookie in the answer: %v", sessionCookie, w.Result().Cookies())
	}
}

// A misspelt field is a 400 naming it, not an empty password.
//
// `pass` for `password` would otherwise be a login attempt with no password at
// all, refused as "handle or password is wrong" - a caller reading their own
// JSON for the mistake, which is the trade every other write door here already
// makes in the other direction.
func TestLoginRefusesAFieldItDoesNotKnow(t *testing.T) {
	s := &server{joins: newJoinLimiter()}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"handle":"dead","pass":"correct horse battery"}`)
	s.handleLogin(w, httptest.NewRequest("POST", "/api/login", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pass") {
		t.Errorf("the refusal does not name the field: %s", w.Body.String())
	}
}

// An empty handle or password never reaches the store. The store would answer
// ErrBadLogin, which is right for a wrong password and wrong for a request that
// did not carry one - 400 says fix your request, 401 says try another password.
func TestLoginNeedsBothHalvesBeforeItAsksTheStore(t *testing.T) {
	for _, body := range []string{
		`{"handle":"","password":"correct horse battery"}`,
		`{"handle":"dead","password":""}`,
	} {
		// A nil db, on purpose: reaching the store here is a nil dereference,
		// so this cannot pass by accident if the guard is removed.
		s := &server{joins: newJoinLimiter()}
		w := httptest.NewRecorder()
		s.handleLogin(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", body, w.Code)
		}
	}
}

// A minimal non-nil TLS state, so the Secure arm above measures the request's
// scheme rather than a nil check somewhere else.
var tlsState = tls.ConnectionState{HandshakeComplete: true}
