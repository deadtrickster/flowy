package store

import "fmt"

// FabricVisibility is the value that makes a row readable from every project on
// this node - see artifactReachSQL. It is what a GLOBAL skill is: a procedure
// the fabric keeps rather than one a project owns.
const FabricVisibility = "fabric"

// FabricWriteRefusal says why a principal may not write this visibility, or "".
//
// Exported and taking plain values because the door asks it, not the store: the
// store's write path carries no principal - its shape checks are about the row -
// and this question is about who is asking.
func FabricWriteRefusal(p *Principal, kind, visibility string) string {
	if visibility != FabricVisibility {
		return ""
	}
	if p == nil || !p.Operator {
		return "visibility " + FabricVisibility + " is readable from every project on this node, so only the operator writes it"
	}
	if kind != SkillKind {
		return fmt.Sprintf("kind %q cannot be %s - only a skill is kept by the fabric rather than by a project", kind, FabricVisibility)
	}
	return ""
}

// SkillKind is a procedure the fabric keeps: how something is done here, written
// once and read by whoever needs it. Two exist on this node today and both are
// project-scoped, which is the state the operator named - "we dont have global
// flowy skills, they are per project now".
const SkillKind = "skill"
