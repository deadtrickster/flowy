package flowy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// EVERY VM DOOR IS THE OPERATOR'S, INCLUDING THE ONES ADDED LATER.
//
// Spawning is starting a process on the host with a copy of a project tree in
// it - the widest thing this node can be asked to do. The gate is the wrapper
// each route is registered with, so the assertion is about the registration
// rather than about a request: posting as a non-operator would prove the
// wrapper works today and say nothing about a seventh route being added next
// week without it.
//
// So this scans for /api/vm routes rather than checking a list of six. A test
// that names the routes it knows about cannot fail on the one nobody told it
// about, which is the only failure that matters here.
func TestEveryVMDoorIsOperatorOnly(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	found := 0
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "api.HandleFunc(") || !strings.Contains(line, "/api/vm/") {
			continue
		}
		found++
		if !strings.Contains(line, "s.operatorOnly(") {
			t.Errorf("a VM door is registered without operatorOnly:\n  %s",
				strings.TrimSpace(line))
		}
	}
	if found == 0 {
		t.Fatal("no /api/vm routes are registered at all")
	}
	if found < 6 {
		t.Errorf("only %d VM doors registered, expected the six in api_vm.go", found)
	}
}

// NOTHING IN THIS FILE MAY REACH A SHELL.
//
// Every call is an argument vector, so a prompt an operator typed arrives at
// the guest as one argument. A project name containing a backtick is a project
// name. This asserts the property at the source, because the day somebody
// reaches for exec.Command("sh", "-c", ...) to add a pipe is the day a console
// text box becomes a command line on the host.
func TestTheVMDoorNeverBuildsAShellCommand(t *testing.T) {
	src, err := os.ReadFile("api_vm.go")
	if err != nil {
		t.Fatalf("read api_vm.go: %v", err)
	}
	for _, bad := range []string{`"sh"`, `"bash"`, `"-c"`, "exec.Command(\"sh"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("api_vm.go mentions %s - a shell turns a typed prompt into a command line", bad)
		}
	}
}

// ABSENT AND EMPTY ARE DIFFERENT ANSWERS.
//
// "No VMs are running" and "this node cannot run VMs" are different facts, and
// a door that reports both as an empty list tells the operator the second is
// the first. That collapse is this project's most repeated defect, and the
// repro panel is the live example: built, correct, and inert, because the
// address was missing and the page had no way to say so.
//
// Two arms, one call each, and the assertion is that they DIFFER. A single
// reading could never tell a working door from one that answers the same thing
// to every question.
func TestMissingFirecodeIsNotAnEmptyList(t *testing.T) {
	missing := httptest.NewRecorder()
	writeFirecodeFailure(missing, errNoFirecode, nil)

	refused := httptest.NewRecorder()
	writeFirecodeFailure(refused, errors.New("exit status 1"), []byte("no such VM"))

	if missing.Code == refused.Code {
		t.Errorf("a missing firecode and a refusing one both answered %d - "+
			"a caller cannot tell 'nothing works' from 'nothing to show'", missing.Code)
	}
	if missing.Code != http.StatusServiceUnavailable {
		t.Errorf("missing firecode answered %d, want 503", missing.Code)
	}
	if refused.Code != http.StatusBadGateway {
		t.Errorf("a refusing firecode answered %d, want 502", refused.Code)
	}
	// The 503 has to carry the fix, not just the diagnosis. A sentence that
	// says only "unavailable" sends the reader to ask somebody.
	if !strings.Contains(missing.Body.String(), "PATH") {
		t.Errorf("the 503 does not say what is missing or how to fix it: %s",
			missing.Body.String())
	}
}

// A TOOL THAT PRINTED A WARNING IS NOT A BODY THE CONSOLE CAN PARSE.
//
// firecode's json verbs are passed through rather than re-parsed here, so that
// a field it learns to report is not silently dropped by a struct in this file.
// The cost of passing through is that anything on stdout becomes the body - so
// it is validated as JSON first, and non-JSON becomes a named 502 instead of a
// parse error in a browser with no explanation.
func TestNonJSONFromFirecodeIsRefusedRatherThanForwarded(t *testing.T) {
	good := httptest.NewRecorder()
	passThroughJSON(good, []byte(`{"vms":[]}`))
	if good.Code != http.StatusOK {
		t.Errorf("valid json answered %d, want 200", good.Code)
	}
	if good.Body.String() != `{"vms":[]}` {
		t.Errorf("valid json was rewritten: %s", good.Body.String())
	}

	bad := httptest.NewRecorder()
	passThroughJSON(bad, []byte("warning: something\n"))
	if bad.Code != http.StatusBadGateway {
		t.Errorf("non-json answered %d, want 502", bad.Code)
	}
	if !strings.Contains(bad.Body.String(), "warning: something") {
		t.Errorf("the 502 hides what firecode actually printed: %s", bad.Body.String())
	}
}
