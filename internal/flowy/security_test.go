package flowy

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestMintedTypesAgreeWithTheStore holds the two halves of one rule together.
//
// POST /api/events refuses the types this node's own handlers mint, and a
// pushed delta has to refuse the same ones - a replicated status event nobody
// moved is the same forgery arriving by another door. The two lists live in
// two packages because the store cannot import the server, so this is what
// stops them drifting apart.
func TestMintedTypesAgreeWithTheStore(t *testing.T) {
	for kind := range mintedTypes {
		if !store.MintedEventType(kind) {
			t.Errorf("%s is minted by this node's handlers, and replication would take it", kind)
		}
	}
	for _, kind := range []string{statusEventType, taskEventType, forgeEventType} {
		if !mintedTypes[kind] {
			t.Errorf("%s is written by a handler and is not on the endpoint's list", kind)
		}
	}
	// chat is not minted: it carries no authority beyond what saying something
	// already gives the same principal, on either side.
	if store.MintedEventType(chatEventType) || mintedTypes[chatEventType] {
		t.Error("chat is not a minted type; refusing it would stop conversations replicating")
	}
}

// TestTheVersionCarriesABuildStamp is the fourteenth round's operability half.
//
// The version was one constant, and it stayed the same string across half a
// dozen builds that differed - phases, security rounds, whatever was on the
// machine. Everything that reports a version reports that one: GET /healthz,
// GET /version, the MCP serverInfo, `flowy version`. So "which build refused
// that row" or "what is this peer actually running" had no answer on the wire,
// which is exactly the question this kind of work asks.
//
// The scheme is release+stamp, and the build sets the stamp - see versionOf. A
// build of another commit is therefore another string, which is the whole
// claim.
func TestTheVersionCarriesABuildStamp(t *testing.T) {
	if versionOf(release, "3305508") == versionOf(release, "b67a294") {
		t.Fatal("two builds of two different commits report the same version")
	}
	if versionOf(release, "3305508") != release+"+3305508" {
		t.Errorf("the scheme is release+stamp, and it made %q", versionOf(release, "3305508"))
	}
	// What the process actually reports is the release under the stamp this
	// binary was linked with, and not a literal somebody has to remember to
	// change.
	if want := versionOf(release, buildStamp); version != want {
		t.Errorf("the reported version is %q, want %q", version, want)
	}
	if !strings.HasPrefix(version, release) {
		t.Errorf("the reported version %q does not carry the release %q", version, release)
	}
	// An unstamped build says so rather than claiming a commit.
	if versionOf(release, "") != release {
		t.Errorf("a build with no stamp reports %q", versionOf(release, ""))
	}
}
