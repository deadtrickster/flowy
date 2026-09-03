package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// EVERY AGENT KIND SAYS WHETHER A DOOR DISTINGUISHES IT.
//
// 01M1M8BE2E02G2ZE2D3FCCY9YW, filed against the set as it stood: "agent_kind
// accepts reviewer and no door reads it". The measurement was close and the
// truth is worse - announce.go reads AgentKind on every announcement and
// MayAnnounceFederation hands a reviewer the same answer it hands a worker. So
// the value is read and NON-DISTINGUISHING, and the closed set carried two
// values that change what a door does beside two that do not, with nothing
// saying which was which.
//
// That is the shape this walk exists to stop coming back. A reader who sees
// system and monitor gating something concludes reviewer does too, and the
// person who added the fourth value had no place to say otherwise.
//
// IT READS THE SOURCE, in the family of TestEveryRegisteredAPIRouteIsAdvertised
// and TestEveryNoSuchArtifactSaysWhereItLooked: the property is about what the
// tree DECLARES, and no request exercises the difference between a label and a
// capability until somebody is refused by one or waved through by the other.
//
// THREE THINGS, and the third is the one that would have caught the original:
//
//	every kind in agentKinds has an AgentKindGates answer and a line in
//	agentKindGateDoc - a value added to the set alone fails here
//
//	AgentKindGates agrees with MayAnnounceFederation, which is the only door
//	that branches today - so the declaration cannot drift from the code it
//	describes without this going red
//
//	the set is not all-gates or all-labels. If it were, the distinction this
//	file asserts would be vacuous and the walk would be passing on a property
//	nothing has. Named rather than left implicit, because a check that cannot
//	fail is worse than no check.
func TestEveryAgentKindSaysWhetherItGates(t *testing.T) {
	if len(agentKinds) == 0 {
		t.Fatal("agentKinds is empty, so this walk measured nothing - the set it reads has gone or been renamed")
	}

	gates, labels, undeclared := 0, 0, 0
	for kind := range agentKinds {
		// COUNTED BEFORE THE SKIP, so the totals reconcile with the set's own
		// size. The first version counted only declared kinds and continued
		// past the rest, so an undeclared value printed "5 kinds: 2 gate, 2 are
		// labels" - a quantity that does not add up is the reader's problem to
		// notice, on the line whose whole job is being read.
		if AgentKindGates(kind) {
			gates++
		} else {
			labels++
		}
		doc, ok := agentKindGateDoc[kind]
		if !ok {
			undeclared++
			t.Errorf("agent kind %q is in the closed set and has no line in agentKindGateDoc.\n"+
				"Say which door distinguishes it, or say plainly that nothing does - "+
				"a value that arrives undeclared is how %q sat beside two gating values meaning nothing.",
				kind, AgentKindReviewer)
			continue
		}
		if strings.TrimSpace(doc) == "" {
			t.Errorf("agent kind %q has an empty line in agentKindGateDoc, which reads as declared and says nothing", kind)
		}
	}

	// THE DECLARATION AGAINST THE ONLY DOOR THAT BRANCHES. Asserting the two
	// agree is what stops this file becoming a comment: a kind given a door
	// tomorrow and not declared here, or declared here and given none, is the
	// drift the row was about.
	for kind := range agentKinds {
		if got, want := AgentKindGates(kind), MayAnnounceFederation(kind); got != want {
			t.Errorf("agent kind %q: AgentKindGates says %v and MayAnnounceFederation says %v.\n"+
				"MayAnnounceFederation is the only door that branches on this column today, so the two cannot differ.\n"+
				"If a SECOND door has started branching, this test is the thing to widen - and widening it is the point.",
				kind, got, want)
		}
	}

	// AND THE DISTINCTION IS NOT VACUOUS.
	if gates == 0 || labels == 0 {
		t.Errorf("the set is %d gating and %d label kind(s), so this walk is asserting a distinction the data does not have.\n"+
			"That is not necessarily a defect - it may mean every kind now gates, or none does - but it means "+
			"this test passes without measuring anything and should be rewritten rather than left green.",
			gates, labels)
	}
	t.Logf("%d agent kind(s): %d gate a door, %d are labels, %d undeclared", len(agentKinds), gates, labels, undeclared)
}

// AND THE ENUM IS THE SET. A constant named AgentKind* that never reaches
// agentKinds is a value mint would refuse while the tree advertises it, which is
// the other half of "the enum and the doors agree" and the cheaper half to get
// wrong: the constant is what a person greps for.
func TestEveryAgentKindConstantIsInTheClosedSet(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^\s*(AgentKind[A-Za-z0-9]+)\s*=\s*"([^"]+)"`)
	found := decl.FindAllStringSubmatch(string(src), -1)
	if len(found) == 0 {
		t.Fatal("no AgentKind constants found in store.go, so this walk measured nothing - " +
			"they have moved, and this test is looking at the wrong file")
	}
	// COUNTED, so the line at the end cannot contradict the errors above it.
	// The first version logged "N constant(s), all in the closed set"
	// unconditionally, and printed it on the same run as an error saying one
	// was not - the reader gets two lines that cannot both be true and has to
	// decide which to believe.
	stray := 0
	for _, m := range found {
		name, value := m[1], m[2]
		if !agentKinds[value] {
			stray++
			t.Errorf("%s = %q is declared and is not in agentKinds, so mint refuses a value the tree advertises", name, value)
		}
	}
	if stray == 0 {
		t.Logf("%d AgentKind constant(s), all in the closed set", len(found))
		return
	}
	t.Logf("%d AgentKind constant(s), %d of them not in the closed set", len(found), stray)
}
