package forge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MockForge is a forge in this process: a map of repo -> issues, each with the
// comments on it. It implements the same interface the real client does and it
// touches nothing outside this program - no network, no credential, no CLI.
//
// It exists because the thing worth testing is the node's half of the bridge:
// that filing stores an external ref, that a closed issue moves an artifact to
// done, that a reviewer's comment becomes an event and a reply becomes a
// comment, and that running the sync twice does it once. None of that is a
// question about GitHub, and asking GitHub would make the gate need a token and
// a network and somebody's real repository to leave issues in.
//
// Everything on it is safe for concurrent use: a node serves requests from
// several connections and the gate drives it from more than one.
type MockForge struct {
	mu     sync.Mutex
	issues map[string]*MockIssue
	// nextNumber is per repo, so the first issue in each repo is #1.
	nextNumber map[string]int
	// seq numbers comments across the whole forge, which is what makes a
	// comment id unique without a clock.
	seq int
	// last is the timestamp handed to the previous comment. Time is what the
	// sync cursor is kept in, so two comments made in the same millisecond
	// still order - a fake that hands out the same instant twice would make a
	// cursor test pass or fail on how fast the machine is.
	last time.Time
	// failArmed and failAfter are how a test makes the forge refuse one
	// comment: the next failAfter comments are accepted and the one after that
	// is not. A forge that is up is easy to test against and tells you nothing
	// about what a node does when a push dies halfway through a conversation,
	// which is the case where a cursor that moved too far posts the same reply
	// twice.
	failArmed bool
	failAfter int
	// self is the login the mock posts under and answers SelfLogin with. It is
	// SelfAuthor unless a test says otherwise: a real gh posts as whoever it is
	// logged in as, and a fake that can only ever be called one thing cannot
	// show that the node asked.
	self string
	// onFileAuthor and onFileBody are a comment the next FileIssue records as
	// part of opening the issue - a reviewer who answered while the issue was
	// being created. It is the window between reading the comment cursor and
	// the forge finishing, made deterministic: a comment stamped inside it is
	// dropped for good by a cursor read afterwards.
	onFileAuthor string
	onFileBody   string
}

// MockIssue is one issue on the mock forge.
type MockIssue struct {
	Repo     string    `json:"repo"`
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	State    string    `json:"state"`
	URL      string    `json:"url"`
	Comments []Comment `json:"comments"`
}

// NewMockForge returns an empty mock forge.
func NewMockForge() *MockForge {
	return &MockForge{issues: map[string]*MockIssue{}, nextNumber: map[string]int{}}
}

// Kind is mock, and it is what lands in an artifact's external ref - so a row
// written against the fake says so, and is never mistaken for one filed on
// GitHub.
func (m *MockForge) Kind() string { return KindMock }

// key addresses one issue.
func key(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

// stamp hands out a strictly increasing timestamp.
func (m *MockForge) stamp() time.Time {
	now := time.Now().UTC()
	if !now.After(m.last) {
		now = m.last.Add(time.Millisecond)
	}
	m.last = now
	return now
}

// FileIssue opens an issue and numbers it from one, per repo.
func (m *MockForge) FileIssue(_ context.Context, repo, title, body string) (int, string, error) {
	if !ValidRepo(repo) {
		return 0, "", fmt.Errorf("forge: %q is not a repo", repo)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextNumber[repo]++
	number := m.nextNumber[repo]
	issue := &MockIssue{
		Repo:     repo,
		Number:   number,
		Title:    title,
		Body:     body,
		State:    StateOpen,
		URL:      fmt.Sprintf("https://mock.forge/%s/issues/%d", repo, number),
		Comments: []Comment{},
	}
	m.issues[key(repo, number)] = issue

	// Somebody answered while the issue was being opened.
	if m.onFileBody != "" {
		m.seq++
		issue.Comments = append(issue.Comments, Comment{
			ID:     "c" + strconv.Itoa(m.seq),
			Author: m.onFileAuthor,
			Body:   m.onFileBody,
			At:     m.stamp(),
		})
		m.onFileAuthor, m.onFileBody = "", ""
	}
	return number, issue.URL, nil
}

// GetState reads an issue's state.
func (m *MockForge) GetState(_ context.Context, repo string, number int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[key(repo, number)]
	if !ok {
		return "", fmt.Errorf("forge: no such issue %s", key(repo, number))
	}
	return issue.State, nil
}

// Comment says something on an issue as the node itself. This is the receiving
// end of the reviewer loop: what the gate asserts is that a reply typed into
// the node's chat arrives here.
//
// It refuses when FailNext has armed a refusal, and nothing is recorded when it
// does: a comment that was refused is a comment that is not on the issue.
func (m *MockForge) Comment(_ context.Context, repo string, number int, body string) error {
	if err := m.takeFailure(); err != nil {
		return err
	}
	_, err := m.AddComment(repo, number, m.SelfAuthor(), body)
	return err
}

// SelfLogin is who this forge says the node posts as, which is what a real gh
// answers `api user` with. It is what the node writes onto the link when it
// files, and what the sync skips comments by.
func (m *MockForge) SelfLogin(_ context.Context) (string, error) { return m.SelfAuthor(), nil }

// SelfAuthor is the login the mock posts under.
func (m *MockForge) SelfAuthor() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.self == "" {
		return SelfAuthor
	}
	return m.self
}

// SetSelfAuthor changes the login the mock posts under, so a test can show
// that the node asked the forge for it rather than assuming the mock's name.
func (m *MockForge) SetSelfAuthor(login string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.self = strings.TrimSpace(login)
}

// CommentOnFile arms a comment for the next FileIssue to record as part of
// opening the issue: the reviewer who answered inside the filing window.
func (m *MockForge) CommentOnFile(author, body string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if author == "" {
		author = "reviewer"
	}
	m.onFileAuthor, m.onFileBody = author, body
}

// takeFailure reports the armed refusal when this call is the one to refuse,
// and disarms it: one refusal per arming, so a test says where the wire breaks
// rather than breaking it for good.
func (m *MockForge) takeFailure() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.failArmed {
		return nil
	}
	if m.failAfter > 0 {
		m.failAfter--
		return nil
	}
	m.failArmed = false
	return fmt.Errorf("forge: the mock was told to refuse this comment")
}

// FailNext arms one refusal: the next after comments are accepted and the one
// after them is not.
func (m *MockForge) FailNext(after int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if after < 0 {
		after = 0
	}
	m.failArmed, m.failAfter = true, after
}

// ListComments reads an issue's comments, dropping anything older than since.
func (m *MockForge) ListComments(
	_ context.Context, repo string, number int, since time.Time,
) ([]Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[key(repo, number)]
	if !ok {
		return nil, fmt.Errorf("forge: no such issue %s", key(repo, number))
	}
	out := make([]Comment, 0, len(issue.Comments))
	for _, c := range issue.Comments {
		if !since.IsZero() && c.At.Before(since) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// SelfAuthor is the login the node's own comments are attributed to on the mock
// forge. The sync skips comments written under it, which is what stops the
// reviewer loop echoing: a reply pushed out must not come back in as a comment
// to thread.
const SelfAuthor = "flowy"

// ---------------------------------------------------------------- control
//
// The three methods below are the mock's control surface: they are how a test
// plays the other side of the conversation - the reviewer who closes an issue
// and the reviewer who comments on it - and how it reads back what the node
// pushed. The node exposes them over HTTP, but only when the mock is the
// selected forge, so this surface exists exactly when the fake does.

// SetState moves an issue to a state, which is what a reviewer closing it does.
func (m *MockForge) SetState(repo string, number int, state string) (*MockIssue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[key(repo, number)]
	if !ok {
		return nil, fmt.Errorf("forge: no such issue %s", key(repo, number))
	}
	issue.State = normaliseState(state)
	return issue.clone(), nil
}

// AddComment appends a comment by author. This is the reviewer talking.
func (m *MockForge) AddComment(repo string, number int, author, body string) (Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[key(repo, number)]
	if !ok {
		return Comment{}, fmt.Errorf("forge: no such issue %s", key(repo, number))
	}
	m.seq++
	c := Comment{
		ID:     "c" + strconv.Itoa(m.seq),
		Author: author,
		Body:   body,
		At:     m.stamp(),
	}
	issue.Comments = append(issue.Comments, c)
	return c, nil
}

// Issue returns a copy of one issue, comments and all.
func (m *MockForge) Issue(repo string, number int) (*MockIssue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	issue, ok := m.issues[key(repo, number)]
	if !ok {
		return nil, fmt.Errorf("forge: no such issue %s", key(repo, number))
	}
	return issue.clone(), nil
}

// clone copies an issue so a caller cannot edit the forge by holding a pointer
// it was handed.
func (i *MockIssue) clone() *MockIssue {
	out := *i
	out.Comments = append([]Comment{}, i.Comments...)
	return &out
}
