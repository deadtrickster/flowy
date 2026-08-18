package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWaitOnInboxNamesTheSwitchedTokenTrap is the deaf-agent fix: when the
// node refuses a wait because the principal holds no reader of that name, the
// waiter must say what that probably is - a token switch that left the reader
// behind under the old identity - and how to look at both causes, not just
// relay the 404. The relayed text is what a monitoring loop sees; the
// diagnosis is what the agent behind it needs. Filed as 01M09NM7B3BJSW19TRD3N81NHC
// after a minted seat ran deaf for six and a half hours with the 404 printing
// the whole time.
func TestWaitOnInboxNamesTheSwitchedTokenTrap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no inbox reader called claude-glm for this ` +
			`principal - declare it first with --new. readers here: flowy-glm",` +
			`"readers":["flowy-glm"]}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	err := waitOnInbox(context.Background(), client, srv.URL, "token", "claude-glm",
		"", false, 2)
	if err == nil {
		t.Fatal("the refusal came back as success")
	}
	for _, want := range []string{
		"no inbox reader called",            // the server's own fact, still present
		"the token was switched",            // the diagnosis the row asked for
		"flowy inbox --as claude-glm --new", // the way out, printed not linked
		"every message since",               // the stake: unread waits at the old mark
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}
