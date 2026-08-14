// Package forge is the bridge to an issue tracker.
//
// A node holds bugs of its own, and the world holds the issue tracker everybody
// else reads. Phase 6 joins the two: an artifact can be filed as an issue on a
// forge, its state can be read back, and the conversation on the issue and the
// conversation in the node's own thread are kept as one conversation.
//
// There are three implementations of one interface:
//
//   - GhClient, which shells out to `gh` for GitHub and - the same client with
//     another argv table - to `glab` for GitLab. It is the real path, and it
//     needs a CLI on PATH and a credential behind it.
//   - MockForge, which is a map in this process. It is what the gate runs
//     against: no network, no credential, no CLI, and the same interface, so
//     what the gate exercises is the node's logic rather than GitHub's.
//
// Which one a node uses is decided once, at startup, by Select.
package forge

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Issue states, normalised. Every forge has its own vocabulary - OPEN, opened,
// CLOSED, merged - and nothing above this package ever sees it: a client maps
// what its CLI printed onto one of these three before returning it.
const (
	StateOpen   = "open"
	StateClosed = "closed"
	StateMerged = "merged"
)

// Kinds. The value of FLOWY_FORGE, and the "forge" of an external ref.
const (
	KindGh   = "gh"
	KindGlab = "glab"
	KindMock = "mock"
)

// Terminal reports whether a state means the issue is finished on the forge.
// Both ways out - closed and merged - move the artifact to done.
func Terminal(state string) bool {
	return state == StateClosed || state == StateMerged
}

// Comment is one comment on an issue. At is what the sync cursor is kept in, so
// a client that cannot report a comment's time reports the zero time and the
// sync falls back to the ids it has already seen.
type Comment struct {
	ID     string    `json:"id"`
	Author string    `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

// ForgeClient is everything the node needs from an issue tracker. It is four
// calls: file one, read its state, say something on it, read what was said.
//
// Every method takes a context because the real implementation shells out to a
// CLI that talks to the network, and a request that hangs there has to be
// cancellable from the request that started it.
type ForgeClient interface {
	// Kind is gh|glab|mock, and is what an external ref records as its forge.
	Kind() string
	// FileIssue opens an issue and returns its number and its URL.
	FileIssue(ctx context.Context, repo, title, body string) (int, string, error)
	// GetState reads an issue's state, normalised to open|closed|merged.
	GetState(ctx context.Context, repo string, number int) (string, error)
	// Comment says something on an issue.
	Comment(ctx context.Context, repo string, number int, body string) error
	// ListComments reads the comments on an issue that are not older than
	// since. A zero since means all of them.
	ListComments(ctx context.Context, repo string, number int, since time.Time) ([]Comment, error)
}

// SelfLoginer is a forge that can say which login it posts as.
//
// It is not part of ForgeClient because it is not part of the bridge: the node
// asks once, when it files, so that the link records the name its own comments
// will arrive under and the sync can tell them from a reviewer's. A client that
// does not implement it is one whose name the caller already knows.
type SelfLoginer interface {
	// SelfLogin is the login the credential behind this client posts as.
	SelfLogin(ctx context.Context) (string, error)
}

// ErrNoForge is what Select returns when nothing is configured and no CLI is on
// PATH. It is not a startup failure: a node with no forge is a perfectly good
// node, it just answers 503 on /api/forge/*.
var ErrNoForge = errors.New("forge: no forge configured and no gh or glab on PATH")

// Available reports which forge CLIs are on PATH. It only looks the binaries
// up - it does not run them, so a node that starts with `gh` installed and no
// credential has still not touched GitHub.
func Available() map[string]bool {
	out := make(map[string]bool, 2)
	for _, kind := range []string{KindGh, KindGlab} {
		_, err := exec.LookPath(kind)
		out[kind] = err == nil
	}
	return out
}

// Select decides which forge this node speaks to, and says why.
//
// want is FLOWY_FORGE. Naming a forge is honoured whether or not its CLI is
// there - a typo should be an error at startup and not a surprise at the first
// file - except for mock, which needs nothing. An empty want is capability
// detection: gh if it is on PATH, else glab, else nothing.
//
// The reason is returned rather than logged here so that the caller can log it
// once and answer with it on /api/forge, which is how anybody finds out what
// this node would actually do.
func Select(want string) (ForgeClient, string, error) {
	switch strings.TrimSpace(want) {
	case KindMock:
		return NewMockForge(), "FLOWY_FORGE=mock", nil
	case KindGh:
		return NewGhClient(), "FLOWY_FORGE=gh", nil
	case KindGlab:
		return NewGlabClient(), "FLOWY_FORGE=glab", nil
	case "":
		// Nothing asked for: take what is installed.
		avail := Available()
		switch {
		case avail[KindGh]:
			return NewGhClient(), "gh found on PATH", nil
		case avail[KindGlab]:
			return NewGlabClient(), "glab found on PATH", nil
		}
		return nil, "no gh or glab on PATH", ErrNoForge
	default:
		return nil, "unknown forge " + want, errors.New("forge: unknown FLOWY_FORGE " + want +
			" (want one of gh, glab, mock)")
	}
}

// ValidRepo reports whether repo looks like one a forge would accept:
// owner/name for GitHub, group/subgroup/name for GitLab. It is a shape check
// and nothing more - whether the repo is there is the forge's answer to give.
func ValidRepo(repo string) bool {
	if repo == "" || strings.ContainsAny(repo, " \t\n") {
		return false
	}
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
	}
	return true
}

// normaliseState maps what a CLI printed onto open|closed|merged. Anything a
// forge invents that is neither closed nor merged is open as far as the
// lifecycle is concerned: the artifact only moves to done on a state that
// certainly means the issue is finished.
func normaliseState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "closed", "close":
		return StateClosed
	case "merged":
		return StateMerged
	default:
		return StateOpen
	}
}
