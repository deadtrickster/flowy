package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GhClient is the real bridge: it shells out to a forge's own CLI, which is
// where the credential lives. There is no HTTP client and no token in this
// process - `gh` and `glab` are already logged in on the machine, and being
// logged in twice is how a node ends up with a credential it cannot rotate.
//
// One type serves both forges. gh and glab differ in argv and in the JSON they
// print and in nothing else that matters here, so the difference is a switch on
// kind rather than a second implementation with the same shape.
type GhClient struct {
	kind string
	// timeout caps one CLI invocation. A forge that is slow is a forge that
	// makes an API request hang, and the request that started it has its own
	// deadline to keep.
	timeout time.Duration
	// run executes the CLI. It is a field so the tests can drive the argv and
	// the parsing without a binary, a network or a credential - which is the
	// only part of this file the gate can honestly exercise.
	run runner
}

// runner is what actually runs the CLI: it takes the program and its arguments
// and gives back what the program wrote to stdout.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// cliTimeout is how long one gh/glab invocation may take.
const cliTimeout = 60 * time.Second

// NewGhClient returns the GitHub client, which shells out to `gh`.
func NewGhClient() *GhClient {
	return &GhClient{kind: KindGh, timeout: cliTimeout, run: runCommand}
}

// NewGlabClient returns the GitLab client: the same shell-out client with
// glab's argv and glab's JSON.
func NewGlabClient() *GhClient {
	return &GhClient{kind: KindGlab, timeout: cliTimeout, run: runCommand}
}

// Kind is gh or glab.
func (c *GhClient) Kind() string { return c.kind }

// runCommand is the real runner. Stderr is folded into the error rather than
// dropped: when `gh` refuses, what it printed is the whole of the diagnosis.
//
// What the argv carried is not folded in. A failure here becomes an HTTP
// response and a line in the node's log, and the argv of a filing holds the
// artifact's whole body - so an issue that could not be opened used to publish
// the thing it could not publish, to whoever asked and to the log. The
// invocation is named by what identifies it instead: the program, what it was
// asked to do, and which issue.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		what := name + " " + describeArgs(args)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", what, err,
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// describeArgs names an invocation without repeating what it carried: the
// leading run of subcommands and positionals - `issue create`, `issue view 17`,
// `api repos/o/r/issues/17/comments` - and the repository, which is where the
// call went rather than what it said. Everything a flag introduces is a value:
// a title, a body, a comment, a jq expression. None of it is described.
func describeArgs(args []string) string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			break
		}
		out = append(out, args[i])
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--repo" || args[i] == "-R" {
			out = append(out, args[i], args[i+1])
			break
		}
	}
	return strings.Join(out, " ")
}

// exec runs the CLI under the client's own timeout, on top of whatever deadline
// the caller already has.
func (c *GhClient) exec(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = cliTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.run(ctx, c.kind, args...)
}

// FileIssue opens an issue and reads its number out of the URL the CLI printed.
//
// Both CLIs answer a create with the URL of what they created and nothing else
// worth parsing, and the number is the last segment of it. That is also the
// only place the number is: `gh issue create` has no --json.
func (c *GhClient) FileIssue(ctx context.Context, repo, title, body string) (int, string, error) {
	if !ValidRepo(repo) {
		return 0, "", fmt.Errorf("forge: %q is not a repo", repo)
	}
	var args []string
	switch c.kind {
	case KindGlab:
		args = []string{"issue", "create", "--repo", repo, "--title", title,
			"--description", body, "--yes"}
	default:
		args = []string{"issue", "create", "--repo", repo, "--title", title, "--body", body}
	}
	out, err := c.exec(ctx, args...)
	if err != nil {
		return 0, "", err
	}
	issueURL := lastURL(string(out))
	if issueURL == "" {
		return 0, "", fmt.Errorf("forge: %s printed no issue URL: %q", c.kind, strings.TrimSpace(string(out)))
	}
	number, err := numberFromURL(issueURL)
	if err != nil {
		return 0, issueURL, err
	}
	return number, issueURL, nil
}

// SelfLogin is who the CLI is logged in as: the login this node's comments
// arrive under on the forge.
//
// It is one call to the forge's own "who am I", made once when an artifact is
// filed. The node needs it to tell its own replies from a reviewer's, and it
// cannot be guessed: the credential belongs to whoever set the machine up, and
// on GitHub it is as likely to be a bot account as a person.
func (c *GhClient) SelfLogin(ctx context.Context) (string, error) {
	var args []string
	switch c.kind {
	case KindGlab:
		// glab has no --jq, so the JSON is parsed here.
		args = []string{"api", "user"}
	default:
		args = []string{"api", "user", "--jq", ".login"}
	}
	out, err := c.exec(ctx, args...)
	if err != nil {
		return "", err
	}
	login := strings.Trim(strings.TrimSpace(string(out)), `"`)
	if c.kind == KindGlab {
		var user struct {
			Username string `json:"username"`
		}
		if err := json.Unmarshal(out, &user); err != nil {
			return "", fmt.Errorf("forge: %s api user: %w", c.kind, err)
		}
		login = user.Username
	}
	if login == "" {
		return "", fmt.Errorf("forge: %s api user: no login in the answer", c.kind)
	}
	return login, nil
}

// ghIssue is the shape of a viewed issue, in both dialects: gh answers
// {"state":"OPEN","url":...} and glab {"state":"opened","web_url":...}.
type ghIssue struct {
	State  string `json:"state"`
	URL    string `json:"url"`
	WebURL string `json:"web_url"`
}

// GetState reads an issue's state, normalised.
func (c *GhClient) GetState(ctx context.Context, repo string, number int) (string, error) {
	if !ValidRepo(repo) {
		return "", fmt.Errorf("forge: %q is not a repo", repo)
	}
	var args []string
	switch c.kind {
	case KindGlab:
		args = []string{"issue", "view", strconv.Itoa(number), "--repo", repo, "-F", "json"}
	default:
		args = []string{"issue", "view", strconv.Itoa(number), "--repo", repo,
			"--json", "state,url"}
	}
	out, err := c.exec(ctx, args...)
	if err != nil {
		return "", err
	}
	var issue ghIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return "", fmt.Errorf("forge: %s issue view: %w", c.kind, err)
	}
	if issue.State == "" {
		return "", fmt.Errorf("forge: %s issue view %s#%d: no state in the answer", c.kind, repo, number)
	}
	return normaliseState(issue.State), nil
}

// Comment says something on an issue.
func (c *GhClient) Comment(ctx context.Context, repo string, number int, body string) error {
	if !ValidRepo(repo) {
		return fmt.Errorf("forge: %q is not a repo", repo)
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("forge: refusing to post an empty comment")
	}
	var args []string
	switch c.kind {
	case KindGlab:
		args = []string{"issue", "note", strconv.Itoa(number), "--repo", repo, "--message", body}
	default:
		args = []string{"issue", "comment", strconv.Itoa(number), "--repo", repo, "--body", body}
	}
	_, err := c.exec(ctx, args...)
	return err
}

// apiComment is a comment as either forge's REST API returns it. The two shapes
// differ in one field - GitHub's user.login against GitLab's author.username -
// so one struct with both reads both.
type apiComment struct {
	ID        json.Number `json:"id"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	System    bool        `json:"system"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

// ListComments reads an issue's comments through the CLI's raw API passthrough,
// because neither `gh issue view` nor `glab issue view` returns comment bodies
// with their authors and times in a shape worth relying on.
//
// GitHub's endpoint takes since and honours it; GitLab's does not, so the
// filter is applied here as well as asked for. A caller that says since gets
// nothing older either way.
func (c *GhClient) ListComments(
	ctx context.Context, repo string, number int, since time.Time,
) ([]Comment, error) {
	if !ValidRepo(repo) {
		return nil, fmt.Errorf("forge: %q is not a repo", repo)
	}
	var path string
	switch c.kind {
	case KindGlab:
		// GitLab addresses a project by its URL-encoded full path.
		path = "projects/" + url.PathEscape(repo) + "/issues/" + strconv.Itoa(number) + "/notes"
	default:
		path = "repos/" + repo + "/issues/" + strconv.Itoa(number) + "/comments"
		if !since.IsZero() {
			path += "?since=" + since.UTC().Format(time.RFC3339)
		}
	}
	out, err := c.exec(ctx, "api", path)
	if err != nil {
		return nil, err
	}

	var raw []apiComment
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("forge: %s api %s: %w", c.kind, path, err)
	}
	comments := make([]Comment, 0, len(raw))
	for _, r := range raw {
		if r.System {
			// GitLab records "changed the description" as a note. It is the
			// forge talking to itself, not somebody reviewing the issue.
			continue
		}
		if !since.IsZero() && !r.CreatedAt.IsZero() && r.CreatedAt.Before(since) {
			continue
		}
		author := r.User.Login
		if author == "" {
			author = r.Author.Username
		}
		comments = append(comments, Comment{
			ID:     r.ID.String(),
			Author: author,
			Body:   r.Body,
			At:     r.CreatedAt,
		})
	}
	return comments, nil
}

// lastURL picks the URL out of what a CLI printed. Both of them print a line or
// two of noise around it and the URL is the last thing on the last line that
// has one.
func lastURL(out string) string {
	found := ""
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			found = strings.Trim(field, ".,)")
		}
	}
	return found
}

// numberFromURL reads the issue number off the end of an issue URL:
// .../owner/repo/issues/17 on GitHub, .../group/repo/-/issues/17 on GitLab.
func numberFromURL(issueURL string) (int, error) {
	trimmed := strings.TrimRight(issueURL, "/")
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return 0, fmt.Errorf("forge: no issue number in %q", issueURL)
	}
	number, err := strconv.Atoi(trimmed[slash+1:])
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("forge: no issue number in %q", issueURL)
	}
	return number, nil
}
