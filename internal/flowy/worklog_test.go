package flowy

// The worklog's two properties, tested without a database: vouched is not
// authored, and the marker that says which is inside the signature.
//
// Neither of these needs a store. What an entry CLAIMS is decided by the row the
// write builds - worklogEvent - and whether a relay could change that claim is
// decided by the bytes the node signs. The doors, the reference check and the
// view are the gate's, in run-tests.sh, because those are properties of two
// processes talking.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The harness that drains a queue of runs, and the agent whose shift it reports
// on. The harness is the right author - it knows the run id and the verify
// status and cannot lie about whether the gate passed - and it is not the agent.
var (
	harness = &store.Principal{UserID: "u-harness", Project: "flowy"}
	claude  = "ag-flowy-claude"
)

// vouchedArgs is one entry the harness writes about a run it drove.
func vouchedArgs() worklogAppendArgs {
	return worklogAppendArgs{
		What:   "drained the queue: the dm branch landed on twelve newer commits",
		Next:   "the four gate failures on master are somebody else's",
		Branch: "wl/dm",
		Run:    "9f6af5dc9032",
		Verify: "428/0",
	}
}

// TestAVouchedEntryIsNotItsSubjectsOwnWord is the one that matters.
//
// An entry written BY the harness ABOUT an agent must never read as the agent's
// own account. So the row carries both: the actor is who wrote it, the subject is
// whose work it is, and a reader can tell an account from a report of one. If
// those two collapse into one field, or the subject silently becomes the actor,
// this is the shape of the impersonation finding this project has open - an entry
// that reads like somebody's own word and was written by something else.
func TestAVouchedEntryIsNotItsSubjectsOwnWord(t *testing.T) {
	e, err := worklogEvent(harness, vouchedArgs(), nil, claude)
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	entry := entryOf(e)

	if entry.Actor != harness.UserID {
		t.Errorf("the entry's actor is %q, want the seat that wrote it, %q",
			entry.Actor, harness.UserID)
	}
	if entry.Subject != claude {
		t.Errorf("the entry's subject is %q, want the seat whose work it is, %q",
			entry.Subject, claude)
	}
	if !entry.Vouched {
		t.Error("an entry written by one seat about another's work does not say it is vouched, " +
			"so it reads as that seat's own account")
	}
	// The run and the verify status are what a reader of a vouched entry is
	// actually deciding about, so they are on the entry and not only in the
	// harness's own logs.
	if entry.Run != "9f6af5dc9032" || entry.Verify != "428/0" {
		t.Errorf("the entry says run %q verify %q, want the run and what the gate said",
			entry.Run, entry.Verify)
	}

	// And the body says it, because the body is what every surface that knows
	// nothing about this kind renders - the TUI's timeline, the activity view.
	// Vouching that only existed in a field those surfaces do not read would show
	// there as the author's own account of somebody else's shift.
	if !strings.HasPrefix(e.Body, "vouched for "+claude) {
		t.Errorf("the body of a vouched entry is %q, and a surface that renders bodies "+
			"and nothing else cannot tell it from an authored one", short(e.Body))
	}
}

// An entry about your own shift is your own account of it, and there is nothing
// to vouch for. Absent and self are one state, for the same reason an absent
// addressee and an empty one are one row: a "vouched" badge on somebody's own
// entry teaches a reader to ignore the badge.
func TestVouchingForYourselfIsAuthoring(t *testing.T) {
	own, err := worklogEvent(harness, vouchedArgs(), nil, "")
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	if entry := entryOf(own); entry.Vouched || entry.Subject != "" {
		t.Errorf("an entry with no subject says vouched=%v subject=%q, want its author's own",
			entry.Vouched, entry.Subject)
	}
	if strings.Contains(own.Body, "vouched") {
		t.Errorf("an authored entry's body says %q", short(own.Body))
	}

	// worklogSubject is what drops a subject that is the writer, and it is the
	// only place that decides it - both doors call it.
	subject, err := worklogSubject(nil, nil, harness.UserID, harness.UserID)
	if err != nil {
		t.Fatalf("resolving your own id as the subject: %v", err)
	}
	if subject != "" {
		t.Errorf("naming yourself as the subject kept %q, want it dropped: an entry about "+
			"your own shift is your own account of it", subject)
	}
	// And entryOf derives it rather than trusting a flag, so a row from some
	// other build whose subject IS its actor still reads as authored.
	self, err := worklogEvent(harness, vouchedArgs(), nil, harness.UserID)
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	if entryOf(self).Vouched {
		t.Error("a row whose subject is its own actor reads as vouched")
	}
}

// TestVouchingIsInsideTheRowSignature is the half a relay would otherwise
// decide.
//
// If it changes what a row claims, it is signed or it is decoration. A
// vouched-vs-authored marker a relay could strip would be believed as
// authorship - the row is correctly signed and correctly actored, and every
// reader would take it as the subject's own account. The marker rides in meta,
// meta is folded into the event signature as its sha256 - see
// sign.CanonicalEvent - so adding it, removing it or swapping it for another
// seat all produce a different message and a signature that does not verify.
//
// This asks the store for the bytes it actually signs rather than re-deriving
// them here, because a test that built its own idea of the encoding would pass
// while the real one left the field out.
func TestVouchingIsInsideTheRowSignature(t *testing.T) {
	vouched, err := worklogEvent(harness, vouchedArgs(), nil, claude)
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	base := store.CanonicalEventBytes(vouched)

	// Every way a relay could rewrite the claim, and what it must cost. The body
	// is left alone in each: a relay that also had to rewrite the body would be
	// caught by the digest over that, and the point here is the field.
	for _, tc := range []struct {
		what string
		to   any
	}{
		{"stripping the subject off a vouched entry", nil},
		{"pointing the entry at another seat", "ag-somebody-else"},
		{"blanking the subject", ""},
	} {
		rewritten := *vouched
		rewritten.Meta = metaWith(t, vouched.Meta, worklogMetaSubject, tc.to)
		if bytes.Equal(base, store.CanonicalEventBytes(&rewritten)) {
			t.Errorf("%s did not change the bytes this node signs, so a relay can do it "+
				"and the row still verifies", tc.what)
		}
	}

	// The evidence goes with it. "the gate passed" is what a reader of a vouched
	// entry acts on, so a verify status a relay could edit is worse than none.
	for _, key := range []string{worklogMetaRun, worklogMetaVerify} {
		rewritten := *vouched
		rewritten.Meta = metaWith(t, vouched.Meta, key, "green, honestly")
		if bytes.Equal(base, store.CanonicalEventBytes(&rewritten)) {
			t.Errorf("rewriting %s did not change the bytes this node signs", key)
		}
	}

	// And a vouched entry is not the same row as the authored one it would be
	// read as, which is the whole claim stated once more from the other end.
	authored, err := worklogEvent(harness, vouchedArgs(), nil, "")
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	authored.ID, authored.Created = vouched.ID, vouched.Created
	if bytes.Equal(base, store.CanonicalEventBytes(authored)) {
		t.Error("a vouched entry and the authored entry it would be mistaken for sign the same")
	}
}

// TestTheWorklogsClaimsAreNotAClientsToWrite closes the other entrance.
//
// A worklog entry is not a minted type - it has to replicate, and a minted one
// does not - so POST /api/events will write an event of that type. Every field
// the worklog verb CHECKS before stamping has to be dropped off a client's meta
// there, or the check is optional: an entry pointing at work its author cannot
// read, or vouching for a seat, on a row that is correctly signed and correctly
// actored. What an entry says about its own shift is not on that list.
func TestTheWorklogsClaimsAreNotAClientsToWrite(t *testing.T) {
	handed := json.RawMessage(`{"what":"did a thing","next":"carry on","as_of":"0e3b7f6",
		"branch":"wl/x","refs":["art-nobody-can-read"],"subject":"ag-flowy-claude",
		"run":"r-1","verify":"pass"}`)
	var kept map[string]json.RawMessage
	if err := json.Unmarshal(speakerStripped(handed), &kept); err != nil {
		t.Fatalf("what came back is not an object: %v", err)
	}
	for _, key := range []string{"refs", "subject", "run", "verify"} {
		if _, found := kept[key]; found {
			t.Errorf("a client handed in %s and it survived: the claim is written without "+
				"the check the worklog verb makes of it", key)
		}
	}
	// And what an entry says about its own shift is still a client's to write.
	// Stripping those would break nothing and gain nothing, and the body beside
	// them was always theirs.
	for _, key := range []string{"what", "next", "as_of", "branch"} {
		if _, found := kept[key]; !found {
			t.Errorf("%s was stripped: it claims nothing about anybody else", key)
		}
	}
}

// The worklog reads on the timeline and does not post there, and this is the
// property the whole dedicated-verb change must not have broken. POST
// /api/activity takes no refs and could not check them, so a kind accepted there
// would be a second entrance that skips the check on the first - which is
// exactly what POST /api/worklog is NOT, because it makes the same check.
func TestTheWorklogIsReadableOnTheTimelineAndNotPostable(t *testing.T) {
	if _, postable := postableKinds[activityWorklog]; postable {
		t.Error("the worklog is postable through POST /api/activity, which cannot check the " +
			"artifact ids an entry references")
	}
	if readableKinds[activityWorklog] != worklogEventType {
		t.Errorf("the timeline cannot be narrowed to the worklog: readableKinds says %q",
			readableKinds[activityWorklog])
	}
}

// metaWith is an event's meta with one key set, or removed when to is nil - a
// relay rewriting one field of a row it is passing on.
func metaWith(t *testing.T, meta json.RawMessage, key string, to any) json.RawMessage {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(meta, &fields); err != nil {
		t.Fatalf("the entry's meta is not an object: %v", err)
	}
	if to == nil {
		delete(fields, key)
	} else {
		fields[key] = to
	}
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("re-encode the meta: %v", err)
	}
	return out
}
