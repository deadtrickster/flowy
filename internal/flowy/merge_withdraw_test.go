package flowy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// WITHDRAWING IS A NORMAL PART OF RUNNING A QUEUE and had no verb, so three
// withdrawals on the live node used three different mechanisms and one of them
// was a raw curl whose exit code said nothing. See 01M0G4FMK4.
//
// Every case here is about a REFUSAL rather than the happy path, because the
// happy path is one POST and the refusals are the whole reason this verb is not
// an alias for `todo done`.

// withdrawNode is a fake node that answers the three doors this verb touches and
// records whether the status write happened. The write is the thing being
// guarded, so "was it posted" is what every refusal test asserts on - a refusal
// that returns an error AND writes anyway is the failure mode a message check
// would miss entirely.
type withdrawNode struct {
	kind, status string
	lockItem     string // the row the target lock was taken for, "" for no lock
	posted       map[string]string
	queueFails   bool
}

func (n *withdrawNode) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			if err := json.Unmarshal(body, &n.posted); err != nil {
				t.Errorf("the CLI posted something that is not json: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/merge-queue":
			if n.queueFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"no"}`))
				return
			}
			answer := mergeQueueAnswer{Target: "master"}
			if n.lockItem != "" {
				answer.Lock = &mergeQueueLock{
					Held: true, Item: n.lockItem, Until: time.Now().Add(15 * time.Minute),
				}
			}
			_ = json.NewEncoder(w).Encode(answer)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": "01ROW", "kind": n.kind, "status": n.status, "title": "land feat/x",
			})
		}
	}))
}

func (n *withdrawNode) withdraw(t *testing.T, extra ...string) error {
	t.Helper()
	srv := n.serve(t)
	defer srv.Close()
	args := append([]string{"--id", "01ROW", "--url", srv.URL, "--token", "t-1"}, extra...)
	return mergeWithdraw(args)
}

// A WITHDRAWAL LEAVES AN ABSENCE, which is the one queue event with no artifact
// of its own: a landing leaves a sha, a red leaves a verdict. Without a note,
// a row that was withdrawn and a row that landed look identical a week later.
func TestAWithdrawalHasToSayWhy(t *testing.T) {
	n := &withdrawNode{kind: "merge", status: "todo"}
	err := n.withdraw(t)
	if err == nil {
		t.Fatal("withdrew a row with no reason on it")
	}
	if !strings.Contains(err.Error(), "--note") {
		t.Errorf("the refusal does not name the flag that fixes it: %v", err)
	}
	if n.posted != nil {
		t.Errorf("refused and wrote anyway: %v", n.posted)
	}
}

// THE WRONG NOUN, ACCEPTED SILENTLY is how the abandon confusion started. This
// verb and `todo done` write through the SAME door, so without a kind check
// `merge withdraw` on a todo id closes the todo and reports a withdrawal.
func TestWithdrawRefusesARowThatIsNotAMergeRequest(t *testing.T) {
	n := &withdrawNode{kind: "todo", status: "todo"}
	err := n.withdraw(t, "--note", "not this one")
	if err == nil {
		t.Fatal("withdrew a todo through the merge verb")
	}
	if !strings.Contains(err.Error(), "todo done") {
		t.Errorf("the refusal does not name the verb that does close a todo: %v", err)
	}
	if n.posted != nil {
		t.Errorf("refused and wrote anyway: %v", n.posted)
	}
}

// CLOSING A ROW DOES NOT RELEASE ITS LOCK - measured: nothing in the status
// path calls MergeLockOf. So withdrawing a row that is holding the target
// leaves master reserved for the full window with no row left to explain why,
// found by whoever declares next.
func TestWithdrawRefusesARowThatIsHoldingTheTarget(t *testing.T) {
	n := &withdrawNode{kind: "merge", status: "todo", lockItem: "01ROW"}
	err := n.withdraw(t, "--note", "changed my mind")
	if err == nil {
		t.Fatal("withdrew the row that holds the target")
	}
	if !strings.Contains(err.Error(), "abandon") {
		t.Errorf("the refusal does not name the door that gives the lock back: %v", err)
	}
	if n.posted != nil {
		t.Errorf("refused and wrote anyway - the lock is now stranded: %v", n.posted)
	}
}

// SOMEBODY ELSE'S LOCK IS NOT THIS ROW'S PROBLEM. The guard keys on the row the
// lock was taken FOR, not on the lock existing: keying on held alone would
// refuse every withdrawal for fifteen minutes whenever anyone gated anything.
func TestWithdrawProceedsWhenTheLockBelongsToAnotherRow(t *testing.T) {
	n := &withdrawNode{kind: "merge", status: "todo", lockItem: "01OTHER"}
	if err := n.withdraw(t, "--note", "superseded"); err != nil {
		t.Fatalf("refused a withdrawal over somebody else's lock: %v", err)
	}
	if n.posted["status"] != "done" {
		t.Errorf("the queue filters on status, so it has to be done: %v", n.posted)
	}
	if !strings.Contains(n.posted["note"], "superseded") {
		t.Errorf("the reason did not reach the row: %v", n.posted)
	}
}

// A QUEUE THAT COULD NOT BE READ IS NOT A QUEUE WITH NO LOCK. The lock read is
// a guard, not the operation - a node that will not answer it must not block a
// legitimate withdrawal, and must not be reported as a clean check either.
func TestAQueueThatCannotBeReadDoesNotBlockAWithdrawal(t *testing.T) {
	n := &withdrawNode{kind: "merge", status: "todo", queueFails: true}
	if err := n.withdraw(t, "--note", "node was down"); err != nil {
		t.Fatalf("a failed guard read refused the operation: %v", err)
	}
	if n.posted["status"] != "done" {
		t.Errorf("did not write: %v", n.posted)
	}
}

// WITHDRAWING TWICE is not an error, but it is not a silent success either: a
// caller that reads "withdrew" a second time has learnt nothing about whether
// the first call worked, which is exactly the question that produced this row.
func TestWithdrawingAnAlreadyDoneRowWritesNothing(t *testing.T) {
	n := &withdrawNode{kind: "merge", status: "done"}
	if err := n.withdraw(t, "--note", "again"); err != nil {
		t.Fatalf("repeating a withdrawal is not an error: %v", err)
	}
	if n.posted != nil {
		t.Errorf("wrote a status onto a row that was already out: %v", n.posted)
	}
}
