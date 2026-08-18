package store

import (
	"context"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestUpstreamVocabularyAndRendering is the pure half: what the vocabulary
// accepts, what a row carrying nothing reads as, and what an entry looks like on
// a surface that knows nothing about this event type. No DATABASE_URL needed.
func TestUpstreamVocabularyAndRendering(t *testing.T) {
	// Empty is unfiled rather than a refusal - the absence of the fact is the
	// fact, which is what makes the 22-of-24 case free.
	for _, tc := range []struct{ asked, want string }{
		{"", UpstreamUnfiled},
		{"  FILED ", UpstreamFiled},
		{"Accepted", UpstreamAccepted},
		{"referenced", UpstreamReferenced},
		{"withdrawn", UpstreamWithdrawn},
	} {
		got, err := NormalizeUpstreamState(tc.asked)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeUpstreamState(%q) = %q, %v; want %q", tc.asked, got, err, tc.want)
		}
	}
	if _, err := NormalizeUpstreamState("reported"); err == nil {
		t.Error("a word outside the vocabulary was accepted; the whole point is that " +
			"a reader can count what has been sent upstream")
	} else if _, ok := err.(DepRefusal); !ok {
		t.Errorf("an unknown state is the caller's mistake, so it must be a DepRefusal: %T", err)
	}
	// An issue and a pull request are different claims, and a guess at which
	// would record "we told them" about a row carrying a fix.
	if _, err := NormalizeUpstreamKind(""); err == nil {
		t.Error("a reference with no kind was accepted")
	}
	if kind, err := NormalizeUpstreamKind(" PR "); err != nil || kind != UpstreamKindPR {
		t.Errorf("NormalizeUpstreamKind(PR) = %q, %v", kind, err)
	}

	// A finding carrying no fields at all - every finding raised before this
	// file existed - is unfiled, and says so rather than saying nothing.
	if got := FindingUpstreamOf(&Artifact{ID: "f1"}); got.State != UpstreamUnfiled || got.Filed() {
		t.Errorf("a row with no fields read as %+v; want unfiled", got)
	}
	if got := FindingUpstreamOf(nil); got.State != UpstreamUnfiled {
		t.Errorf("a nil artifact read as %+v; want unfiled", got)
	}
	// A NUMBER IS NOT A FILING. A row carrying references and no state word is
	// referenced, never filed - the mistake the import's dry run made on 7 rows.
	cited := FindingUpstreamOf(&Artifact{ID: "f1", Fields: []byte(
		`{"upstream_refs":[{"tracker":"ragflow","kind":"pr","id":"16958"}]}`)})
	if cited.State != UpstreamReferenced || cited.Filed() {
		t.Errorf("a row citing a PR read as %+v; want referenced and not filed", cited)
	}
	filed := FindingUpstreamOf(&Artifact{ID: "f1", Fields: []byte(
		`{"upstream_tracker":"serenedb","upstream_kind":"issue","upstream_id":"12",
		  "upstream_state":"accepted"}`)})
	if !filed.Filed() || filed.Tracker != "serenedb" || filed.ID != "12" {
		t.Errorf("a filed row read as %+v", filed)
	}

	// A reference names whose tracker and which number; a bare one is refused,
	// and two spellings of one reference fold into the one that has the link.
	if _, err := normalizeUpstreamRefs([]UpstreamRef{{Kind: UpstreamKindPR, ID: "16958"}}); err == nil {
		t.Error("a reference with no tracker was accepted")
	}
	refs, err := normalizeUpstreamRefs([]UpstreamRef{
		{Tracker: "ragflow", Kind: "PR", ID: "16958"},
		{Tracker: "ragflow", Kind: "pr", ID: "16958", URL: "https://example.invalid/pull/16958"},
		{Tracker: "ragflow", Kind: "issue", ID: "12109"},
	})
	if err != nil {
		t.Fatalf("normalize refs: %v", err)
	}
	if len(refs) != 2 || refs[0].URL == "" || refs[0].Kind != UpstreamKindPR {
		t.Errorf("refs = %+v; want the PR folded into one entry that kept the link", refs)
	}

	// filed_at is normalised, never stored as free text: a console sorts it and
	// an importer is checked against it.
	if at, err := normalizeFiledAt("2026-03-14"); err != nil || at != "2026-03-14T00:00:00Z" {
		t.Errorf("normalizeFiledAt(date) = %q, %v", at, err)
	}
	if _, err := normalizeFiledAt("last spring"); err == nil {
		t.Error("free text was accepted as filed_at")
	}

	// The body names the issue at both ends, so a re-file reads as two numbers
	// and one story.
	body := upstreamBody(
		UpstreamFiling{State: UpstreamWithdrawn, Tracker: "serenedb", Kind: UpstreamKindIssue, ID: "12"},
		UpstreamFiling{State: UpstreamFiled, Tracker: "serenedb", Kind: UpstreamKindIssue, ID: "31"})
	if body != "withdrawn as serenedb #12->filed as serenedb #31" {
		t.Errorf("body = %q", body)
	}
	if got := upstreamBody(
		UpstreamFiling{State: UpstreamUnfiled},
		UpstreamFiling{State: UpstreamReferenced, Refs: []UpstreamRef{
			{Tracker: "ragflow", Kind: UpstreamKindPR, ID: "16959"}}},
	); got != "unfiled->referenced (1 cited)" {
		t.Errorf("body = %q", got)
	}
}

// TestFindingUpstreamRoundTrip is the store half, and it walks the cases the
// corpus actually contains: never filed, cited but not filed, filed, and filed
// then taken back and filed again somewhere else.
func TestFindingUpstreamRoundTrip(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "upstream")
	owner := &User{Handle: "filer-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	finding := &Artifact{
		Type: "finding", Project: &project, OwnerUser: owner.ID,
		Title: "abandoned read blocks checkpoint",
	}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	// NEVER FILED is the common case and costs nothing: no keys, no call.
	if got := FindingUpstreamOf(finding); got.State != UpstreamUnfiled {
		t.Fatalf("a fresh finding read as %+v; want unfiled", got)
	}

	// A filed state with no number is a status word, which is the defect.
	if _, _, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		State: UpstreamFiled,
	}); err == nil {
		t.Fatal("a filing with no tracker and no number was accepted")
	}
	// And a citation may not be written into the filing's own keys, which is
	// how one filed row became eight.
	if _, _, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		State: UpstreamReferenced, Tracker: "ragflow", Kind: UpstreamKindPR, ID: "16958",
	}); err == nil {
		t.Fatal("a referenced row was allowed to name the issue we filed")
	}

	// REFERENCED: numbers we cite, nobody claiming we sent anything. Seven of
	// the sixteen RAGFlow findings are exactly this.
	art, _, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		State: UpstreamReferenced,
		Refs: []UpstreamRef{
			{Tracker: "ragflow", Kind: UpstreamKindIssue, ID: "12109"},
			{Tracker: "ragflow", Kind: UpstreamKindPR, ID: "16959"},
		},
	})
	if err != nil {
		t.Fatalf("record references: %v", err)
	}
	referenced := FindingUpstreamOf(art)
	if referenced.State != UpstreamReferenced || referenced.Filed() || len(referenced.Refs) != 2 {
		t.Fatalf("after citing two things the row reads %+v", referenced)
	}
	if referenced.ID != "" || referenced.FiledBy != "" {
		t.Errorf("a citation filled in the filing's keys: %+v", referenced)
	}

	art, entry, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		Tracker: "serenedb", Kind: UpstreamKindIssue, ID: "12",
		URL:   "https://example.invalid/serenedb/issues/12",
		State: UpstreamFiled, FiledAt: "2026-03-14", FiledBy: "i.khaprov",
	})
	if err != nil {
		t.Fatalf("file upstream: %v", err)
	}
	stood := FindingUpstreamOf(art)
	if !stood.Filed() || stood.Tracker != "serenedb" || stood.ID != "12" {
		t.Fatalf("after filing the row reads %+v", stood)
	}
	// The date and the person the corpus recorded, not this principal and not
	// now: an imported filing keeps whoever made it.
	if stood.FiledAt != "2026-03-14T00:00:00Z" || stood.FiledBy != "i.khaprov" {
		t.Errorf("filed_at/filed_by = %q/%q; the stated pair must ride as data",
			stood.FiledAt, stood.FiledBy)
	}
	// Filing does not drop what the finding already cited, and what we filed
	// joins the list - so one containment query answers "what is in this PR".
	if len(stood.Refs) != 3 || !stood.Refs[2].Same(stood.Ref()) {
		t.Errorf("refs after filing = %+v; want the two citations plus the filing", stood.Refs)
	}
	if entry.Type != EventFindingUpstream || entry.Artifact != finding.ID {
		t.Errorf("entry = %+v", entry)
	}

	// OUR lifecycle is untouched by THEIR filing, which is the whole point.
	if art.Status != finding.Status {
		t.Errorf("filing upstream moved our status to %q", art.Status)
	}

	// A state move states what changes and keeps the number, the day and the
	// person behind the filing it advances.
	art, _, err = db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{State: UpstreamAccepted})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	accepted := FindingUpstreamOf(art)
	if accepted.State != UpstreamAccepted || accepted.ID != "12" ||
		accepted.FiledAt != "2026-03-14T00:00:00Z" || accepted.FiledBy != "i.khaprov" {
		t.Fatalf("after accept the row reads %+v", accepted)
	}

	// FILED TWICE while the first stands: refused, and the row does not move.
	// The other issue would be live over there with nothing pointing at it.
	if _, _, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		Tracker: "ragflow", Kind: UpstreamKindPR, ID: "17375", State: UpstreamFiled,
	}); err == nil {
		t.Fatal("a second filing over a standing one was accepted")
	}
	// And calling it unfiled would erase the number rather than say what
	// happened to it.
	if _, _, err := db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		State: UpstreamUnfiled,
	}); err == nil {
		t.Fatal("unfiled was accepted over a filing that exists")
	}
	if got := FindingUpstreamOf(mustRead(t, ctx, db, p, finding.ID)); got.ID != "12" ||
		got.State != UpstreamAccepted {
		t.Fatalf("a refused write moved the row: %+v", got)
	}

	// WITHDRAWN keeps the number - that is why it is a word - and it is what
	// makes a re-file legal.
	art, _, err = db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{State: UpstreamWithdrawn})
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	withdrawn := FindingUpstreamOf(art)
	if withdrawn.State != UpstreamWithdrawn || withdrawn.ID != "12" || withdrawn.Filed() {
		t.Fatalf("after withdrawal the row reads %+v", withdrawn)
	}

	art, _, err = db.SetFindingUpstream(ctx, p, finding.ID, UpstreamFiling{
		Tracker: "ragflow", Kind: UpstreamKindPR, ID: "17375", State: UpstreamFiled,
	})
	if err != nil {
		t.Fatalf("re-file after withdrawal: %v", err)
	}
	refiled := FindingUpstreamOf(art)
	if refiled.Tracker != "ragflow" || refiled.ID != "17375" || !refiled.Filed() {
		t.Fatalf("after re-filing the row reads %+v", refiled)
	}
	// The issue we withdrew is still something this finding touches.
	if len(refiled.Refs) != 4 {
		t.Errorf("refs after re-filing = %+v; the withdrawn number stays cited", refiled.Refs)
	}

	// The log is the thing the row cannot be: every number this finding ever
	// had, in order, including the one it no longer carries.
	log, err := db.FindingUpstreamLog(ctx, p, finding.ID)
	if err != nil {
		t.Fatalf("upstream log: %v", err)
	}
	if len(log) != 5 {
		t.Fatalf("got %d entries, want 5 (referenced, filed, accepted, withdrawn, re-filed)",
			len(log))
	}
	if log[0].State != UpstreamReferenced || len(log[0].Refs) != 2 {
		t.Errorf("entry 0 = %+v", log[0])
	}
	if log[1].State != UpstreamFiled || log[1].UpstreamID != "12" {
		t.Errorf("entry 1 = %+v", log[1])
	}
	if log[4].UpstreamID != "17375" || log[4].FromID != "12" {
		t.Errorf("entry 4 = %+v; a re-file names the number it replaced", log[4])
	}
	if stands := LatestFindingUpstream(log); stands == nil || stands.ID != "17375" {
		t.Errorf("the fold of the log = %+v", stands)
	}
}

// TestFindingUpstreamRefusals covers the two refusals that are about who can
// read what rather than about the filing itself.
func TestFindingUpstreamRefusals(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "upstream-refuse")
	owner := &User{Handle: "filer-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	// A projectless finding refuses a filing, RecordFindingRun's rule: the entry
	// would be readable by whoever recorded it and by nobody else who can read
	// the finding.
	personal := &Artifact{
		Type: "finding", OwnerUser: owner.ID, Visibility: VisibilityPersonal,
		Title: "mine alone",
	}
	if err := db.UpsertArtifact(ctx, personal); err != nil {
		t.Fatalf("upsert personal finding: %v", err)
	}
	if _, _, err := db.SetFindingUpstream(ctx, p, personal.ID, UpstreamFiling{
		Tracker: "serenedb", Kind: UpstreamKindIssue, ID: "1", State: UpstreamFiled,
	}); err == nil {
		t.Error("a projectless finding took a filing whose entry only its writer could read")
	}

	// An id that is not a finding is answered the way a read of it would be.
	todo := &Artifact{
		Type: MemoryType, Kind: "todo", Project: &project, OwnerUser: owner.ID, Title: "not a finding",
	}
	if err := db.UpsertArtifact(ctx, todo); err != nil {
		t.Fatalf("upsert todo: %v", err)
	}
	_, _, err := db.SetFindingUpstream(ctx, p, todo.ID, UpstreamFiling{
		Tracker: "serenedb", Kind: UpstreamKindIssue, ID: "1", State: UpstreamFiled,
	})
	var notFinding NotAFindingError
	if !errors.As(err, &notFinding) {
		t.Errorf("filing against a todo answered %v; want NotAFindingError", err)
	}
}

// TestUpstreamRefsAcrossFindings is the many-to-many case measured rather than
// asserted: RAGFlow PR #16958 covers findings 01, 04 and 05, and the question
// asked when it is turned down is which findings go with it.
func TestUpstreamRefsAcrossFindings(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "upstream-refs")
	owner := &User{Handle: "filer-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	pr := UpstreamRef{Tracker: "ragflow", Kind: UpstreamKindPR, ID: "16958"}
	covered := 0
	for _, title := range []string{"ragflow 01", "ragflow 04", "ragflow 05"} {
		art := &Artifact{Type: "finding", Project: &project, OwnerUser: owner.ID, Title: title}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		if _, _, err := db.SetFindingUpstream(ctx, p, art.ID, UpstreamFiling{
			State: UpstreamReferenced, Refs: []UpstreamRef{pr},
		}); err != nil {
			t.Fatalf("cite the PR on %s: %v", title, err)
		}
		for _, ref := range FindingUpstreamOf(mustRead(t, ctx, db, p, art.ID)).Refs {
			if ref.Same(pr) {
				covered++
			}
		}
	}
	if covered != 3 {
		t.Errorf("the same PR was recorded on %d findings, want 3 - one reference "+
			"covering several findings is the case the corpus has", covered)
	}
}

// mustRead re-reads a row so a test asserts against what the database holds
// rather than against the struct the write handed back.
func mustRead(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) *Artifact {
	t.Helper()
	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return art
}
