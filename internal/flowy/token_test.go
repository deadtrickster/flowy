package flowy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of every test in this file is one sentence: no path through
// resolveToken hands out the operator's personal token without saying so. The
// bug it is written against is not that the wrong token was used - it is that
// nothing anywhere printed a word when it was, so the mistake was found by the
// operator noticing they could not see their own messages.

// configHome points the resolver at a throwaway config directory and returns
// it. Every test uses one so that a failure here can never read, or be rescued
// by, the real ~/.config/flowy of whoever is running the suite.
func configHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, agentTokenDir), 0o700); err != nil {
		t.Fatalf("making the agents directory: %v", err)
	}
	return dir
}

// writeToken puts a credential where the resolver will look for it.
func writeToken(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// captureWarnings redirects the loud line so a test can assert on it. A
// warning that is never read in a test is indistinguishable from the silence
// this whole change is about.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := tokenWarnings
	tokenWarnings = buf
	t.Cleanup(func() { tokenWarnings = previous })
	return buf
}

// The two files this is all about, one path segment apart.
func operatorTokenPath(dir string) string { return filepath.Join(dir, tokenFile) }
func seatTokenPath(dir, name string) string {
	return filepath.Join(dir, agentTokenDir, name)
}

func TestResolveTokenPrefersTheFlag(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	warnings := captureWarnings(t)

	got, err := resolveToken("typed-token", "", "", "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "typed-token" {
		t.Fatalf("token = %q, want the one passed to --token", got)
	}
	// A credential typed at the moment of the call names its own principal, so
	// there is nothing to warn about.
	if warnings.Len() != 0 {
		t.Fatalf("--token warned: %s", warnings)
	}
}

func TestResolveTokenRefusesTwoPrincipals(t *testing.T) {
	dir := configHome(t)
	writeToken(t, seatTokenPath(dir, "claude-host"), "seat-token")

	got, err := resolveToken("typed-token", "", "claude-host", "")
	if err == nil {
		t.Fatalf("--token with --agent was accepted, returning %q", got)
	}
	if !strings.Contains(err.Error(), "two principals") {
		t.Fatalf("error does not say what is wrong: %v", err)
	}
}

func TestResolveTokenReadsTheNamedSeat(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	writeToken(t, seatTokenPath(dir, "claude-host"), "seat-token")
	warnings := captureWarnings(t)

	for _, name := range []string{"flag", "env"} {
		flagAgent, envAgent := "claude-host", ""
		if name == "env" {
			flagAgent, envAgent = "", "claude-host"
		}
		got, err := resolveToken("", "", flagAgent, envAgent)
		if err != nil {
			t.Fatalf("%s: resolveToken: %v", name, err)
		}
		if got != "seat-token" {
			t.Fatalf("%s: token = %q, want the seat's own", name, got)
		}
	}
	// Naming a principal is the fix, not the fault: it says nothing.
	if warnings.Len() != 0 {
		t.Fatalf("a named seat warned: %s", warnings)
	}
}

func TestResolveTokenFlagAgentBeatsEnvAgent(t *testing.T) {
	dir := configHome(t)
	writeToken(t, seatTokenPath(dir, "one"), "one-token")
	writeToken(t, seatTokenPath(dir, "two"), "two-token")

	got, err := resolveToken("", "", "two", "one")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "two-token" {
		t.Fatalf("token = %q, want the one --agent named", got)
	}
}

// A name outranks a bare credential. FLOWY_TOKEN is usually a leftover export;
// FLOWY_AGENT is a statement about who is acting now, and the accident this
// change exists to stop is a seat that knows its own name speaking as somebody
// else because of something its harness set once.
func TestResolveTokenNamedSeatBeatsEnvToken(t *testing.T) {
	dir := configHome(t)
	writeToken(t, seatTokenPath(dir, "claude-host"), "seat-token")

	got, err := resolveToken("", "operator-token", "", "claude-host")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "seat-token" {
		t.Fatalf("token = %q, want the named seat's rather than $FLOWY_TOKEN", got)
	}
}

// The heart of it. A seat that is new, or misspelt, must not be quietly
// downgraded to the credential sitting one directory up.
func TestResolveTokenRefusesAMissingSeatRatherThanFallingBack(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	warnings := captureWarnings(t)

	got, err := resolveToken("", "", "claude-hsot", "")
	if err == nil {
		t.Fatalf("a missing seat resolved to %q", got)
	}
	if got == "operator-token" {
		t.Fatal("a missing seat fell back to the operator's own token")
	}
	if got != "" {
		t.Fatalf("a missing seat returned %q as well as an error", got)
	}
	if !strings.Contains(err.Error(), seatTokenPath(dir, "claude-hsot")) {
		t.Fatalf("error does not name the file it looked for: %v", err)
	}
	if warnings.Len() != 0 {
		t.Fatalf("a refusal also warned: %s", warnings)
	}
}

func TestResolveTokenRefusesAnEmptySeatFile(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	if err := os.WriteFile(seatTokenPath(dir, "claude-host"), []byte("  \n"), 0o600); err != nil {
		t.Fatalf("writing the empty seat file: %v", err)
	}

	got, err := resolveToken("", "", "claude-host", "")
	if err == nil {
		t.Fatalf("an empty seat file resolved to %q", got)
	}
	if got != "" {
		t.Fatalf("an empty seat file returned %q", got)
	}
}

// The name becomes a filename, and the one credential most worth reaching by
// traversal is in the parent directory.
func TestResolveTokenRefusesANameThatIsAPath(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")

	for _, name := range []string{"../token", "..", ".", "a/b", `a\b`, ".hidden"} {
		got, err := resolveToken("", "", name, "")
		if err == nil {
			t.Fatalf("--agent %q was accepted, returning %q", name, got)
		}
		if got != "" {
			t.Fatalf("--agent %q returned %q", name, got)
		}
	}
}

// The fallback still works - the operator's shell and every script written
// before this change keep running - but it can no longer happen quietly.
func TestResolveTokenWarnsWhenItFallsBackToTheOperatorToken(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	warnings := captureWarnings(t)

	got, err := resolveToken("", "", "", "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "operator-token" {
		t.Fatalf("token = %q, want the operator's - the fallback still has to work", got)
	}
	said := warnings.String()
	if said == "" {
		t.Fatal("the operator's own token was used and nothing was said, which is the bug")
	}
	// It has to be actionable where it is read, so it names both files and the
	// way to make it stop.
	for _, want := range []string{
		operatorTokenPath(dir),
		filepath.Join(dir, agentTokenDir),
		"FLOWY_AGENT",
		"--agent " + operatorSelf,
	} {
		if !strings.Contains(said, want) {
			t.Fatalf("the warning does not mention %q:\n%s", want, said)
		}
	}
}

// The operator gets to answer the warning once instead of being asked on every
// command.
func TestResolveTokenOperatorSelfIsQuiet(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	warnings := captureWarnings(t)

	got, err := resolveToken("", "", operatorSelf, "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "operator-token" {
		t.Fatalf("token = %q, want the operator's own", got)
	}
	if warnings.Len() != 0 {
		t.Fatalf("--agent %s warned: %s", operatorSelf, warnings)
	}
}

// Two readings of one word is worth refusing rather than silently preferring
// either: an actual seat called "me" would otherwise be read as the operator.
func TestResolveTokenRefusesASeatCalledMe(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	writeToken(t, seatTokenPath(dir, operatorSelf), "seat-token")

	got, err := resolveToken("", "", operatorSelf, "")
	if err == nil {
		t.Fatalf("the collision resolved to %q", got)
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error does not say why: %v", err)
	}
}

func TestResolveTokenEnvTokenIsQuiet(t *testing.T) {
	dir := configHome(t)
	writeToken(t, operatorTokenPath(dir), "operator-token")
	warnings := captureWarnings(t)

	got, err := resolveToken("", "env-token", "", "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "env-token" {
		t.Fatalf("token = %q, want $FLOWY_TOKEN", got)
	}
	if warnings.Len() != 0 {
		t.Fatalf("$FLOWY_TOKEN warned: %s", warnings)
	}
}

// Nothing anywhere is not an error here - it is the caller's, which turns it
// into errNoToken - and it is not a warning either, because there is no
// credential to have used by accident.
func TestResolveTokenWithNothingAnywhere(t *testing.T) {
	configHome(t)
	warnings := captureWarnings(t)

	got, err := resolveToken("", "", "", "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "" {
		t.Fatalf("token = %q, want empty", got)
	}
	if warnings.Len() != 0 {
		t.Fatalf("an empty machine warned: %s", warnings)
	}
	if !strings.Contains(errNoToken().Error(), "--agent") {
		t.Fatalf("the no-token error does not teach --agent: %v", errNoToken())
	}
}
