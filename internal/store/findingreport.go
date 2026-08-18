package store

// THE REPORT DRAFT: THE TEXT THAT GOES UPSTREAM, WHICH IS A THIRD DOCUMENT.
//
// A finding carries three write-ups and they are for three different readers,
// which is why they do not share a field:
//
//	Body       what is wrong, for somebody here who has to fix it or judge it
//	Discovery  how it was found - what was tried, what the evidence shows, what
//	           turned out to be a dead end. The candid investigation record, and
//	           the one document that deliberately never leaves this node: see
//	           cmd/handoff-runner/main.go, which keeps Discovery out of every
//	           package it builds.
//	report     what we send THEM: a defect written for a maintainer who has
//	           never seen this setup, does not know the fleet's vocabulary, and
//	           will close it if it cannot be acted on as written.
//
// Writing a defect up for your own record and writing it for a stranger's
// tracker are different jobs, and the third one is the one that gets skipped.
// Without somewhere to keep it, the report is composed in whatever window is
// open at the moment of filing, pasted into their issue, and then exists only
// there - so nobody here can review it before it goes, nobody can see what was
// claimed after it went, and re-filing a rejected finding somewhere else starts
// from nothing. That is why this is a field on the row and not a habit.
//
// IT IS A DRAFT UNTIL A FILING SAYS OTHERWISE, and this file deliberately holds
// no state word for that. Where the filing stands is findingupstream.go's axis -
// unfiled, filed as #123, accepted, rejected - and a second "is it sent yet"
// living here would be a copy of that, drifting the first time one of them is
// written without the other. The draft is words; the filing is a fact about
// somebody else's tracker; the lifecycle is how far our own work got. Three
// axes, three homes, no collapsing.
//
// WHERE IT LIVES: in the finding's Fields, under one key, for the reason the
// head of findingrepro.go gives for the repro manifest and knownissue.go gives
// for `explains` - a finding gets no columns of its own, and a jsonb key
// already replicates, is signed with the row, and is filtered per reader.
//
// LATEST-CALL-WINS, like the repro manifest and unlike a run's verdict. A draft
// is a fact about what this finding currently proposes to say upstream, so a
// rewrite states what stands now. The history worth keeping is on the other
// axis: findingupstream.go's log keeps every filing this draft ever became, and
// that is a sequence, not an edit.

import "encoding/json"

// ReportDraftField is the key the upstream-facing write-up rides in a finding's
// fields. Named `report` rather than `upstream_report` because that is the word
// the corpus already uses - REPORT.md sits next to DISCOVERY.md and RESULT.md in
// every reproduction directory the importers read, and a key that renamed it
// would be one more thing to translate on the way in.
const ReportDraftField = "report"

// MaxReportDraft is the ceiling on a draft, in bytes: the same 100KB a finding's
// body and discovery carry, for the same reason those have one. Over it, the
// document goes through attachment_write and the draft names the id - a report
// that outgrew the row would be a report search cannot reach.
const MaxReportDraft = 100_000

// FindingReportDraft reads the draft off a finding already in hand, touching
// nothing else - FindingRepro's shape, for a caller that has the row and wants
// the document.
//
// An absent key and an empty draft are ONE answer here, and that is not a
// collapse of the kind this package usually refuses: "nobody has written the
// upstream report yet" is what both of them mean. What must not be collapsed is
// this answer with the FILING state, and that lives in findingupstream.go where
// nothing about it is guessed from whether a draft exists - a finding can be
// filed from a report written by hand elsewhere, and a long draft can sit here
// having been sent to nobody.
//
// Fields that do not parse read as no draft rather than an error: every caller
// of this is rendering a document beside a row it already has, and a page that
// refused to draw a finding because some other key on it was malformed would be
// hiding the finding to protect the report.
func FindingReportDraft(finding *Artifact) string {
	if finding == nil || len(finding.Fields) == 0 {
		return ""
	}
	var raw struct {
		Report string `json:"report"`
	}
	if err := json.Unmarshal(finding.Fields, &raw); err != nil {
		return ""
	}
	return raw.Report
}
