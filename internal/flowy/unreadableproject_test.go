package flowy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A list door refuses a project it cannot read instead of answering empty.
//
// 01M17RVV777776424HGXJZC46M. GET /api/artifacts?project=flowy on a credential
// that reads only pa answered 200 with zero artifacts, which is what a project
// with nothing in it also answers. The operator's shell token reads a fixture
// project, so every list they asked for came back empty and correct-looking.
//
// AND THE ORACLE QUESTION, which is why this is a 403 where the row-by-id case
// stayed a 404: the answer here depends only on the REQUEST and the caller's
// own reach. The tests below pin that - a project that exists and one that never
// has are refused identically.
func TestAListRefusesAProjectItCannotRead(t *testing.T) {
	s := &server{}

	ask := func(p *store.Principal, project string) (int, string) {
		r := httptest.NewRequest("GET", "/api/artifacts?project="+project, nil)
		w := httptest.NewRecorder()
		stopped := s.refuseUnreadableProject(w, withPrincipal(r, p), project)
		if !stopped {
			return 0, ""
		}
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body["error"]
	}

	t.Run("a project outside reach is refused, not emptied", func(t *testing.T) {
		code, why := ask(&store.Principal{UserID: "u", Project: "pa"}, "flowy")
		if code != 403 {
			t.Fatalf("asking for a project this credential cannot read was allowed through as %d - it will answer 200 with an empty list, which is what an empty project looks like", code)
		}
		if !strings.Contains(why, "flowy") || !strings.Contains(why, "pa") {
			t.Fatalf("the refusal names neither what was asked for nor what is readable: %q", why)
		}
	})

	t.Run("a project in reach is allowed through", func(t *testing.T) {
		if code, _ := ask(&store.Principal{UserID: "u", Project: "flowy"}, "flowy"); code != 0 {
			t.Fatalf("a credential was refused its own project, answering %d", code)
		}
	})

	t.Run("a second project in reach is allowed through", func(t *testing.T) {
		p := &store.Principal{UserID: "u", Project: "flowy", Projects: []string{"Lab"}}
		if code, _ := ask(p, "Lab"); code != 0 {
			t.Fatalf("a credential reaching two projects was refused the second, answering %d", code)
		}
	})

	t.Run("no project named is not a refusal", func(t *testing.T) {
		if code, _ := ask(&store.Principal{UserID: "u", Project: "pa"}, ""); code != 0 {
			t.Fatalf("a list with no project filter was refused, answering %d", code)
		}
	})

	// THE GUARD ON THE ORACLE. If a project that exists were refused differently
	// from one that does not, this door would tell an unauthorised caller which
	// project names are real. It cannot, because it never looks: the answer is
	// computed from the caller's reach alone.
	t.Run("an unreadable project and an invented one are refused identically", func(t *testing.T) {
		p := &store.Principal{UserID: "u", Project: "pa"}
		_, real := ask(p, "flowy")
		_, invented := ask(p, "no-such-project-anywhere")
		if strings.Replace(real, "flowy", "X", 1) != strings.Replace(invented, "no-such-project-anywhere", "X", 1) {
			t.Fatalf("the refusal differs between a real project and an invented one, which names which projects exist:\n  %q\n  %q", real, invented)
		}
	})
}
