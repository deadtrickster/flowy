package store

import (
	"fmt"
	"strings"
)

// FabricVisibility is the value that makes a row readable from every project on
// this node - see artifactReachSQL. It is what a GLOBAL skill is: a procedure
// the fabric keeps rather than one a project owns.
const FabricVisibility = "fabric"

// FabricWriteRefusal says why a principal may not write this visibility, or "".
//
// Exported and taking plain values because the door asks it, not the store: the
// store's write path carries no principal - its shape checks are about the row -
// and this question is about who is asking.
//
// IT WAS OPERATOR-ONLY AND THAT WAS WRONG. The reasoning was sound and the
// premise was not: a read branch this wide should have a narrow write door, so I
// gave it the narrowest one available. Then the operator asked for a skill to be
// made global and there was no way to do it - every credential on the fleet is a
// worker, including the orchestrator's, so the door could not be satisfied by
// anybody who was actually here. "lol no, if i say global something you do global
// something, no way i curl myself."
//
// A guard only the absent can pass is not a guard, it is a wall. What remains is
// the narrowing that still holds:
//
//   - ONLY A SKILL. A procedure is what the fabric keeps. A visibility that
//     quietly worked for todos, findings or readings would cross projects in
//     places nobody asked for, and each new kind gets added by name with its own
//     reason written down.
//   - ONLY YOUR OWN ROW, which the create door already enforces: owner_user must
//     be the calling principal, so a seat can publish ITS OWN procedure to the
//     fabric and cannot widen somebody else's.
//
// What that gives up: any seat can now make a project read a document it did not
// ask for. That is the real cost and it is smaller than it looks - a skill is
// text, read-only, owned and attributed, and a fleet whose procedures cannot
// leave the project that wrote them rediscovers them one project at a time,
// which is the thing this exists to stop.
func FabricWriteRefusal(p *Principal, kind, visibility string) string {
	if visibility != FabricVisibility {
		return ""
	}
	if p == nil || p.UserID == "" {
		return "visibility " + FabricVisibility + " is readable from every project on this node, so an unauthenticated write cannot set it"
	}
	if !fabricKind(kind) {
		return fmt.Sprintf("kind %q cannot be %s - the fabric keeps %s, and every other kind belongs to a project",
			kind, FabricVisibility, strings.Join(FabricKinds, " and "))
	}
	return ""
}

// FabricKinds is what the fabric may keep, and it is a closed list ON PURPOSE.
//
// The operator: "memory can cross project too. and somehow all vlaudes glms
// opencodes etc etc must learn to use flowy and read memories and skills from
// here." So a note joins a skill - both are things one seat learned and every
// other seat needs, and both are read-only text.
//
// It stays a LIST rather than becoming "anything": a todo that crossed projects
// would appear on boards nobody filed it to, a metric would land in dashboards
// that never asked for the series, and a merge row would offer another project's
// branch to this project's drainer. Each of those is a different mistake and none
// of them is what "global memories" meant. A future kind gets added here by name,
// with its own reason written next to it.
var FabricKinds = []string{SkillKind, NoteKind}

// NoteKind is a memory written from a shell - `flowy note write`. type memory,
// kind note.
const NoteKind = "note"

func fabricKind(k string) bool {
	for _, f := range FabricKinds {
		if k == f {
			return true
		}
	}
	return false
}

// SkillKind is a procedure the fabric keeps: how something is done here, written
// once and read by whoever needs it. Two exist on this node today and both are
// project-scoped, which is the state the operator named - "we dont have global
// flowy skills, they are per project now".
const SkillKind = "skill"
