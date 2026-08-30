package flowy

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

// A request that is already over is not a server fault.
//
// 01M17K62JD1JGTQM2BRRTND18H, the part the first fix did not reach. 4750189
// taught pollUntil that a cancellation during its query is the client going.
// The work AFTER the poll still reported one: measured on the very deploy that
// shipped it, "store: list artifacts: context canceled" logged as a 500 in
// 14.7ms, from the citations call that runs once the poll returns.
//
// The 500 cannot even be delivered - the connection is gone - so it is read by
// nobody except the log, where it looks like the node breaking.
func TestACancelledRequestIsNotAServerFault(t *testing.T) {
	t.Run("no 500 is written to a request that is over", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequest("GET", "/api/inbox/wait", nil).WithContext(ctx)
		w := httptest.NewRecorder()

		serverError(w, r, errors.New("store: list artifacts: context canceled"))

		if w.Code == 500 {
			t.Fatal("a cancelled request was answered with 500 - written to a closed connection, and counted as a fault the node did not commit")
		}
		if w.Body.Len() != 0 {
			t.Fatalf("a body was written to a request nobody is reading: %q", w.Body.String())
		}
	})

	// THE HALF THAT KEEPS THIS HONEST. If a live request stopped reporting its
	// failures, this would hide every real fault in the node - which is far
	// worse than the misreporting it fixes.
	t.Run("a live request still gets its 500", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/inbox/wait", nil)
		w := httptest.NewRecorder()

		serverError(w, r, errors.New("the store is on fire"))

		if w.Code != 500 {
			t.Fatalf("a genuine failure on a live request answered %d - a node that hides its faults is worse than one that miscounts them", w.Code)
		}
		if w.Body.Len() == 0 {
			t.Fatal("a live 500 carried no body, so the caller has nothing to report")
		}
	})
}
