package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// dispatch runs one request against a server with no database. Everything
// tested here answers before a row is read; the tools themselves are tested by
// the gate, against a real store and two real principals.
func dispatch(t *testing.T, msg string) *rpcResponse {
	t.Helper()
	m := &mcpServer{node: "test"}
	return m.dispatchRaw(context.Background(), "", []byte(msg))
}

func TestInitializeCarriesInstructions(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("initialize failed: %+v", resp)
	}
	got, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("initialize returned %T, want an object", resp.Result)
	}
	if got["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion is %v, want %s", got["protocolVersion"], protocolVersion)
	}
	info := got["serverInfo"].(map[string]any)
	if info["name"] != "flowy" || info["version"] != version {
		t.Errorf("serverInfo is %v, want flowy %s", info, version)
	}
	text, _ := got["instructions"].(string)
	if len(text) < 500 {
		t.Fatalf("instructions are %d bytes, want the document", len(text))
	}
	// The instructions are what an agent reads instead of guessing. If they
	// stop naming the scopes and the tools they have stopped being that.
	for _, want := range []string{"personal", "project", "shared", "mem_write", "mem_search", "todos"} {
		if !strings.Contains(text, want) {
			t.Errorf("the instructions never mention %q", want)
		}
	}
}

func TestInstructionsResourceIsTheSameText(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"`+instructionsURI+`"}}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("resources/read failed: %+v", resp)
	}
	contents := resp.Result.(map[string]any)["contents"].([]any)
	first := contents[0].(map[string]any)
	if first["uri"] != instructionsURI {
		t.Errorf("uri is %v, want %s", first["uri"], instructionsURI)
	}
	if first["text"] != instructions {
		t.Error("the resource does not serve the same bytes as initialize.instructions")
	}
}

func TestUnknownResourceIsRefused(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"flowy://secrets"}}`)
	if resp == nil || resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("resources/read of an unknown uri answered %+v, want an invalid params error", resp)
	}
}

func TestToolsListIsUsable(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	listed := map[string]bool{}
	for _, tl := range resp.Result.(map[string]any)["tools"].([]tool) {
		if tl.Description == "" {
			t.Errorf("tool %s has no description", tl.Name)
		}
		if tl.InputSchema["type"] != "object" {
			t.Errorf("tool %s has no object input schema", tl.Name)
		}
		if _, err := json.Marshal(tl.InputSchema); err != nil {
			t.Errorf("tool %s has an unmarshalable schema: %v", tl.Name, err)
		}
		listed[tl.Name] = true
	}
	for _, want := range []string{"mem_write", "mem_read", "mem_search", "mem_list", "todos"} {
		if !listed[want] {
			t.Errorf("tools/list does not offer %s", want)
		}
	}
}

// A tools/call with no credential must not reach the store - which is also why
// this test can run without one.
func TestToolsCallWithoutAPrincipalIsRefused(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_list","arguments":{}}}`)
	if resp == nil || resp.Error == nil {
		t.Fatalf("tools/call answered %+v, want an error", resp)
	}
	if resp.Error.Code != codeUnauthorized {
		t.Errorf("error code is %d, want %d", resp.Error.Code, codeUnauthorized)
	}
	if !strings.Contains(resp.Error.Message, "unauthenticated") {
		t.Errorf("error message is %q, which does not say it was unauthenticated", resp.Error.Message)
	}
}

func TestUnknownMethodAndBadJSON(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"no/such"}`)
	if resp == nil || resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method answered %+v, want method not found", resp)
	}
	resp = dispatch(t, `{"jsonrpc":"2.0",`)
	if resp == nil || resp.Error == nil || resp.Error.Code != codeParse {
		t.Fatalf("broken JSON answered %+v, want a parse error", resp)
	}
}

// A notification has no id and gets no answer at all, even when the method
// behind it would have returned something.
func TestNotificationsAreNotAnswered(t *testing.T) {
	for _, msg := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"initialize","params":{}}`,
	} {
		if resp := dispatch(t, msg); resp != nil {
			t.Errorf("%s was answered with %+v, want silence", msg, resp)
		}
	}
}

func TestStdioFramesOneResponsePerLine(t *testing.T) {
	m := &mcpServer{node: "test"}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			"\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out strings.Builder
	if err := m.serveStdio(context.Background(), in, &out, ""); err != nil {
		t.Fatalf("serveStdio: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("response %d is not JSON: %v", i, err)
		}
		if resp.JSONRPC != "2.0" {
			t.Errorf("response %d has jsonrpc %q", i, resp.JSONRPC)
		}
	}
}

// oneOf is what keeps a misspelled scope from being written as the default one,
// which is the one mistake on this surface that cannot be taken back.
func TestOneOfRefusesRatherThanDefaults(t *testing.T) {
	if got, err := oneOf("scope", "", memScopes, "personal"); err != nil || got != "personal" {
		t.Errorf("absent scope resolved to (%q, %v), want personal", got, err)
	}
	if _, err := oneOf("scope", "porject", memScopes, "personal"); err == nil {
		t.Error("a misspelled scope was accepted")
	}
	if got, err := oneOf("kind", "handoff", memKinds, "note"); err != nil || got != "handoff" {
		t.Errorf("kind handoff resolved to (%q, %v)", got, err)
	}
}

// TestStdioStopsOnCancellation is what SIGTERM has to do to `flowy mcp`.
//
// The serve loop used to be `for scanner.Scan()`, which is not interruptible: a
// blocked read of stdin returns when the other end closes the pipe and at no
// other time. The signal handler cancelled the context, dispatch would have seen
// it, and the process sat in the read - so a client that terminates its server
// and waits for it waited for its own timeout instead, and the orphans piled up.
//
// The reader here never produces a line and is never closed, which is exactly
// what an idle client's stdin looks like.
func TestStdioStopsOnCancellation(t *testing.T) {
	m := &mcpServer{node: "test"}
	in, hold := io.Pipe()
	t.Cleanup(func() { hold.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.serveStdio(ctx, in, io.Discard, "") }()

	// Nothing is coming down that pipe, so the loop is in its read by now.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveStdio returned %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveStdio is still reading stdin five seconds after the context was cancelled")
	}
}
