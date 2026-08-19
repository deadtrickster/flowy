package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AN EXPLICIT CREDENTIAL LOSING TO AN INHERITED NAME HAS TO SAY SO.
//
// resolveToken prefers FLOWY_AGENT over FLOWY_TOKEN, and one line above it the
// SAME collision between the two flags is a refusal - "two principals named".
// The asymmetry is the defect: FLOWY_TOKEN is a credential somebody chose,
// while FLOWY_AGENT is usually inherited from a drainer, a wrapper or a stale
// shell, so the ambient value silently outranked the deliberate one and every
// write went out attributed to a seat the caller never named.
//
// A warning that is printed unconditionally is the same as no warning, so both
// arms are here.
func TestAnInheritedAgentBeatingAnExplicitTokenSaysSo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	seats := filepath.Join(dir, "flowy", "agents")
	if err := os.MkdirAll(seats, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seats, "drainer"), []byte("tok-drainer\n"), 0o600); err != nil {
		t.Fatalf("write seat: %v", err)
	}

	said := func(t *testing.T, flagToken, envToken, flagAgent, envAgent string) (string, string) {
		t.Helper()
		var buf bytes.Buffer
		old := tokenWarnings
		tokenWarnings = &buf
		defer func() { tokenWarnings = old }()
		got, err := resolveToken(flagToken, envToken, flagAgent, envAgent)
		if err != nil {
			t.Fatalf("resolveToken: %v", err)
		}
		return got, buf.String()
	}

	// BOTH SET IN THE ENVIRONMENT: the agent wins, and the line names it.
	token, warning := said(t, "", "tok-explicit", "", "drainer")
	if token != "tok-drainer" {
		t.Errorf("resolved %q, want the seat's token - the precedence itself is not "+
			"what this changes", token)
	}
	if !strings.Contains(warning, "FLOWY_AGENT=drainer wins") {
		t.Errorf("nothing said that an explicit FLOWY_TOKEN was set aside. Said: %q", warning)
	}
	if !strings.Contains(warning, "unset FLOWY_AGENT") {
		t.Errorf("the warning does not say how to use the token instead: %q", warning)
	}

	// TWO NAMES FOR ONE PRINCIPAL IS NOT A COLLISION, which is the arm that was
	// missing and the one the deployment found within minutes: scripts/say.sh
	// passes FLOWY_TOKEN=<the named seat's own token> beside FLOWY_AGENT naming
	// that seat, so every message every agent sent printed four lines about a
	// conflict that did not exist.
	if _, quiet := said(t, "", "tok-drainer", "", "drainer"); quiet != "" {
		t.Errorf("the token IS the named seat's own and it warned anyway: %q\n"+
			"Nothing is being set aside - this is one principal named two ways, "+
			"and a warning on it fires on every scripted call in the fleet", quiet)
	}

	// A NAMED SEAT ALONE IS THE NORMAL CASE and must be silent, or the warning
	// is noise on every command this fleet runs.
	if _, quiet := said(t, "", "", "", "drainer"); quiet != "" {
		t.Errorf("FLOWY_AGENT alone warned: %q", quiet)
	}

	// AND A TOKEN ALONE IS NOT A COLLISION EITHER.
	if got, quiet := said(t, "", "tok-explicit", "", ""); got != "tok-explicit" || quiet != "" {
		t.Errorf("FLOWY_TOKEN alone resolved %q and said %q", got, quiet)
	}

	// --agent BESIDE FLOWY_TOKEN IS SOMEBODY OVERRIDING ON PURPOSE, which is
	// what the flag is for, so it is silent too. Nothing here is about stopping
	// people naming a seat; it is about them not being named one by accident.
	if _, quiet := said(t, "", "tok-explicit", "drainer", ""); quiet != "" {
		t.Errorf("--agent beside FLOWY_TOKEN warned, and that is a deliberate override: %q", quiet)
	}
}
