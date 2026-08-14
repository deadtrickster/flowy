package forge

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSelectPicksTheNamedForge covers the one decision a node makes at startup.
// FLOWY_FORGE is honoured whether or not the CLI it names is installed - a typo
// should fail here rather than at the first attempt to file something.
func TestSelectPicksTheNamedForge(t *testing.T) {
	for _, tc := range []struct {
		want string
		kind string
	}{
		{KindMock, KindMock},
		{KindGh, KindGh},
		{KindGlab, KindGlab},
		{" mock ", KindMock},
	} {
		client, why, err := Select(tc.want)
		if err != nil {
			t.Fatalf("Select(%q): %v", tc.want, err)
		}
		if client.Kind() != tc.kind {
			t.Errorf("Select(%q) picked %s, want %s", tc.want, client.Kind(), tc.kind)
		}
		if why == "" {
			t.Errorf("Select(%q) gave no reason", tc.want)
		}
	}

	if _, _, err := Select("bitbucket"); err == nil {
		t.Error("Select(bitbucket) should refuse a forge it does not have")
	}
}

// TestSelectMockNeedsNothing is the property the gate leans on: the fake is
// reachable with no CLI on PATH, no credential and no network.
func TestSelectMockNeedsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client, _, err := Select(KindMock)
	if err != nil {
		t.Fatalf("Select(mock) with an empty PATH: %v", err)
	}
	if _, ok := client.(*MockForge); !ok {
		t.Fatalf("Select(mock) returned %T, want *MockForge", client)
	}
	if _, _, err := Select(""); err == nil {
		t.Error("Select(\"\") with an empty PATH should report that there is no forge")
	}
}

// TestMockForgeIsAForge walks the whole interface against the fake, which is
// what the gate drives through the API.
func TestMockForgeIsAForge(t *testing.T) {
	ctx := context.Background()
	mock := NewMockForge()

	number, issueURL, err := mock.FileIssue(ctx, "o/r", "the gearbox whines", "under load")
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if number != 1 {
		t.Errorf("first issue is #%d, want #1", number)
	}
	if !strings.HasSuffix(issueURL, "/o/r/issues/1") {
		t.Errorf("issue url is %q", issueURL)
	}

	// Numbering is per repo.
	if n, _, err := mock.FileIssue(ctx, "o/other", "another", ""); err != nil || n != 1 {
		t.Errorf("first issue in a second repo is #%d (%v), want #1", n, err)
	}

	state, err := mock.GetState(ctx, "o/r", 1)
	if err != nil || state != StateOpen {
		t.Fatalf("state is %q (%v), want open", state, err)
	}

	first, err := mock.AddComment("o/r", 1, "reviewer", "does it flimberwock?")
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if err := mock.Comment(ctx, "o/r", 1, "it does"); err != nil {
		t.Fatalf("comment: %v", err)
	}

	all, err := mock.ListComments(ctx, "o/r", 1, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d comments, want 2", len(all))
	}
	if all[0].Author != "reviewer" || all[1].Author != SelfAuthor {
		t.Errorf("authors are %q and %q, want reviewer and %s", all[0].Author, all[1].Author, SelfAuthor)
	}
	if !all[1].At.After(all[0].At) {
		t.Error("comment times must strictly increase, or a cursor cannot page them")
	}

	// since is what the sync cursor becomes: everything strictly older is gone.
	after, err := mock.ListComments(ctx, "o/r", 1, all[0].At.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(after) != 1 || after[0].ID == first.ID {
		t.Errorf("since dropped %d comments, want the first one only", 2-len(after))
	}

	if _, err := mock.SetState("o/r", 1, "CLOSED"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if state, _ := mock.GetState(ctx, "o/r", 1); state != StateClosed {
		t.Errorf("state after closing is %q, want closed", state)
	}

	// A copy, not the map's own issue.
	issue, err := mock.Issue("o/r", 1)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	issue.Comments = nil
	if again, _ := mock.Issue("o/r", 1); len(again.Comments) != 2 {
		t.Error("Issue handed out a pointer into the forge")
	}

	if _, err := mock.GetState(ctx, "o/r", 99); err == nil {
		t.Error("an issue that was never filed should not have a state")
	}
}

// fakeCLI records what a client would have run and answers with a canned
// stdout, so the gh and glab paths can be checked without a binary, a network
// or a credential - which is all of them that can be checked in here.
type fakeCLI struct {
	calls [][]string
	out   string
	err   error
}

func (f *fakeCLI) runner() runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, append([]string{name}, args...))
		return []byte(f.out), f.err
	}
}

func (f *fakeCLI) last() string {
	if len(f.calls) == 0 {
		return ""
	}
	return strings.Join(f.calls[len(f.calls)-1], " ")
}

// TestGhClientArgv pins the GitHub command lines and the parsing of what they
// print. The real path is exercised on a host that has gh and a login; what is
// testable in here is that the node asks for the right thing and reads the
// answer correctly.
func TestGhClientArgv(t *testing.T) {
	ctx := context.Background()
	fake := &fakeCLI{out: "https://github.com/o/r/issues/17\n"}
	client := &GhClient{kind: KindGh, run: fake.runner()}

	number, issueURL, err := client.FileIssue(ctx, "o/r", "title", "body")
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if number != 17 || issueURL != "https://github.com/o/r/issues/17" {
		t.Errorf("filed #%d at %q, want #17", number, issueURL)
	}
	if want := "gh issue create --repo o/r --title title --body body"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}

	fake.out = `{"state":"CLOSED","url":"https://github.com/o/r/issues/17"}`
	state, err := client.GetState(ctx, "o/r", 17)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != StateClosed {
		t.Errorf("state is %q, want closed", state)
	}
	if want := "gh issue view 17 --repo o/r --json state,url"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}

	fake.out = ""
	if err := client.Comment(ctx, "o/r", 17, "hello"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if want := "gh issue comment 17 --repo o/r --body hello"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}
	if err := client.Comment(ctx, "o/r", 17, "   "); err == nil {
		t.Error("an empty comment should not reach the forge")
	}

	fake.out = `[{"id":991,"body":"does it flimberwock?","created_at":"2026-01-02T03:04:05Z",
	             "user":{"login":"reviewer"}}]`
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	comments, err := client.ListComments(ctx, "o/r", 17, since)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("%d comments, want 1", len(comments))
	}
	if comments[0].ID != "991" || comments[0].Author != "reviewer" {
		t.Errorf("comment is %+v", comments[0])
	}
	if want := "gh api repos/o/r/issues/17/comments?since=2026-01-01T00:00:00Z"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}
}

// TestGlabClientArgv is the same for GitLab, whose argv and JSON differ.
func TestGlabClientArgv(t *testing.T) {
	ctx := context.Background()
	fake := &fakeCLI{out: "#8 the gearbox https://gitlab.com/g/sub/r/-/issues/8\n"}
	client := &GhClient{kind: KindGlab, run: fake.runner()}

	number, _, err := client.FileIssue(ctx, "g/sub/r", "title", "body")
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if number != 8 {
		t.Errorf("filed #%d, want #8", number)
	}
	if want := "glab issue create --repo g/sub/r --title title --description body --yes"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}

	fake.out = `{"state":"opened","web_url":"https://gitlab.com/g/sub/r/-/issues/8"}`
	state, err := client.GetState(ctx, "g/sub/r", 8)
	if err != nil || state != StateOpen {
		t.Fatalf("state is %q (%v), want open", state, err)
	}
	if want := "glab issue view 8 --repo g/sub/r -F json"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}

	fake.out = ""
	if err := client.Comment(ctx, "g/sub/r", 8, "hello"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if want := "glab issue note 8 --repo g/sub/r --message hello"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}

	// A system note is the forge narrating itself, and is not a review comment.
	fake.out = `[{"id":3,"body":"changed the description","created_at":"2026-01-02T03:04:05Z",
	              "system":true,"author":{"username":"reviewer"}},
	             {"id":4,"body":"looks right","created_at":"2026-01-02T03:05:05Z",
	              "author":{"username":"reviewer"}}]`
	comments, err := client.ListComments(ctx, "g/sub/r", 8, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "4" || comments[0].Author != "reviewer" {
		t.Errorf("comments are %+v, want the one that is not a system note", comments)
	}
	if want := "glab api projects/g%2Fsub%2Fr/issues/8/notes"; fake.last() != want {
		t.Errorf("argv is %q, want %q", fake.last(), want)
	}
}

func TestValidRepo(t *testing.T) {
	for _, repo := range []string{"o/r", "g/sub/r"} {
		if !ValidRepo(repo) {
			t.Errorf("%q should be a repo", repo)
		}
	}
	for _, repo := range []string{"", "r", "o/", "/r", "o r/x", "o//r"} {
		if ValidRepo(repo) {
			t.Errorf("%q should not be a repo", repo)
		}
	}
}

func TestNormaliseState(t *testing.T) {
	for raw, want := range map[string]string{
		"OPEN": StateOpen, "open": StateOpen, "opened": StateOpen,
		"CLOSED": StateClosed, "closed": StateClosed,
		"MERGED": StateMerged, "merged": StateMerged,
		"locked": StateOpen, "": StateOpen,
	} {
		if got := normaliseState(raw); got != want {
			t.Errorf("normaliseState(%q) = %q, want %q", raw, got, want)
		}
	}
	if !Terminal(StateClosed) || !Terminal(StateMerged) || Terminal(StateOpen) {
		t.Error("closed and merged are terminal, open is not")
	}
}

// TestFileIssueNeedsAURL: the number is only ever in the URL the CLI prints, so
// a CLI that prints nothing has to be an error rather than issue #0.
func TestFileIssueNeedsAURL(t *testing.T) {
	fake := &fakeCLI{out: "created\n"}
	client := &GhClient{kind: KindGh, run: fake.runner()}
	if _, _, err := client.FileIssue(context.Background(), "o/r", "t", "b"); err == nil {
		t.Error("a create with no URL in its output should be an error")
	}
	if _, _, err := client.FileIssue(context.Background(), "not-a-repo", "t", "b"); err == nil {
		t.Error("a repo that is not owner/name should not reach the CLI")
	}
}
