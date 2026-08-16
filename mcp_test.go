package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
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
	// And the pointer, which is the whole reason the short text is short. An
	// edit that drops it leaves the detail unreachable by anyone who only ever
	// reads what initialize handed them.
	if !strings.Contains(text, "guide") {
		t.Error("the instructions never name the guide, so nothing points at the detail")
	}
}

// The instructions have to survive the client that carries them.
//
// Claude Code truncates server instructions at about 2 KB and opencode does not,
// so a document over the limit is delivered whole to one half of a fleet and cut
// off mid-sentence to the other, with nothing said on either side. That is how
// this document spent its first weeks: 5,835 bytes, of which Claude Code saw the
// scopes and none of the tools.
//
// The margin is deliberate. "About 2 KB" is a described limit rather than a
// measured one, so this asserts against 1,800 and leaves room to be wrong about
// where exactly the knife falls.
func TestInstructionsFitTheTruncationLimit(t *testing.T) {
	const limit = 1800
	if len(instructions) > limit {
		t.Fatalf("instructions are %d bytes, want at most %d - anything past that is "+
			"silently discarded by clients that truncate, so it must move into guide.md",
			len(instructions), limit)
	}
	// The other half of the split: the guide is where the detail actually went.
	// A guide that is no longer than the instructions means somebody trimmed
	// the document rather than moving what they trimmed.
	if len(guide) <= len(instructions) {
		t.Fatalf("the guide is %d bytes against instructions of %d: the detail is gone, "+
			"not relocated", len(guide), len(instructions))
	}
	for _, want := range []string{"mem_write", "report_write", "personal", "kind", "tags"} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide never mentions %q", want)
		}
	}
}

// The resource serves the guide, not the short text. A client reaching for it
// has either already had the short version or ignored it; either way what it is
// missing is the detail.
func TestInstructionsResourceServesTheGuide(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"`+instructionsURI+`"}}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("resources/read failed: %+v", resp)
	}
	contents := resp.Result.(map[string]any)["contents"].([]any)
	first := contents[0].(map[string]any)
	if first["uri"] != instructionsURI {
		t.Errorf("uri is %v, want %s", first["uri"], instructionsURI)
	}
	if first["text"] != guide {
		t.Error("the resource does not serve the guide")
	}
}

// And the tool, which is the copy that does not depend on the client reading
// instructions or resources at all - the one an agent asks for on purpose.
func TestTheGuideToolReturnsTheGuide(t *testing.T) {
	out, err := guideTool(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("the guide tool failed: %v", err)
	}
	got, ok := out.(map[string]any)["guide"].(string)
	if !ok {
		t.Fatalf("the guide tool returned %T, want the text", out)
	}
	if got != guide {
		t.Error("the guide tool does not return the guide")
	}
	if _, listed := toolByName("guide"); !listed {
		t.Error("the guide tool is not in the tool table, so nothing can call it")
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

// The report surface rides the same registration the memory tools do, and the
// instructions an agent reads must name it - an unmentioned tool is an unused
// tool. The write itself is exercised against a real store by the gate.
func TestReportSurfaceIsListedAndDocumented(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	listed := map[string]tool{}
	for _, tl := range resp.Result.(map[string]any)["tools"].([]tool) {
		listed[tl.Name] = tl
	}
	for _, want := range []string{"report_write", "report_read", "report_search", "report_list"} {
		got, ok := listed[want]
		if !ok {
			t.Errorf("tools/list does not offer %s", want)
			continue
		}
		if _, has := got.InputSchema["properties"]; !has {
			t.Errorf("tool %s has no properties in its schema", want)
		}
	}
	// A report is born at scope=project, so the write schema has to say so:
	// an agent that cannot see the default will publish drafts by accident.
	// And it must name as_of - a report without a point-in-time is a claim
	// with no expiry.
	w, ok := listed["report_write"]
	if !ok {
		t.Fatal("report_write is not listed")
	}
	desc := w.Description
	if !strings.Contains(desc, "project") {
		t.Errorf("report_write description never states the project default: %q", desc)
	}
	props := w.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"as_of", "supersedes"} {
		if _, has := props[field]; !has {
			t.Errorf("report_write schema has no %s field", field)
		}
	}
	for _, want := range []string{"report_write", "report_search", "as_of"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("the instructions never mention %q", want)
		}
	}
}

// The worklog rides the same registration, and an agent that never reads the
// guide still has to be able to find it: the tool is listed, the write says
// what it is for, and the short instructions name it. The writes themselves are
// exercised against a real store and two principals by the gate.
func TestWorklogSurfaceIsListedAndDocumented(t *testing.T) {
	resp := dispatch(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	listed := map[string]tool{}
	for _, tl := range resp.Result.(map[string]any)["tools"].([]tool) {
		listed[tl.Name] = tl
	}
	for _, want := range []string{"worklog_append", "worklog_read"} {
		if _, ok := listed[want]; !ok {
			t.Errorf("tools/list does not offer %s", want)
		}
	}
	// The two invariants, as far as a schema can carry them: an entry says what
	// changed, and it names the work by id rather than describing it.
	appendTool, ok := listed["worklog_append"]
	if !ok {
		t.Fatal("worklog_append is not listed")
	}
	props := appendTool.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"what", "next", "as_of", "refs"} {
		if _, has := props[field]; !has {
			t.Errorf("worklog_append schema has no %s field", field)
		}
	}
	required, _ := appendTool.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "what" {
		t.Errorf("worklog_append requires %v, want what alone", required)
	}
	if refs, _ := props["refs"].(map[string]any); refs["type"] != "array" {
		t.Errorf("refs is %v, want a list of ids", refs["type"])
	}
	for _, want := range []string{"worklog_append", "worklog_read", "refs"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("the instructions never mention %q", want)
		}
	}
	// And the detail, which is where the detail goes.
	for _, want := range []string{"worklog_read", "worklog_append", "chronology"} {
		if !strings.Contains(guide, want) {
			t.Errorf("the guide never mentions %q", want)
		}
	}
}

// What worklog_append refuses before it ever reaches the store. Each of these
// is one of the surface's rules, and each answers rather than writing something
// half-formed: the server here has no database at all, so a check that got past
// them would panic instead of passing.
func TestWorklogAppendRefusesWhatItCannotStamp(t *testing.T) {
	m := &mcpServer{node: "test"}
	ctx := context.Background()
	seat := &store.Principal{UserID: "ua", AgentID: "aa", Project: "pa"}

	// Every entry carries an actor, so a token that resolves to nobody has no
	// entry to write.
	if _, err := worklogAppend(ctx, m, &store.Principal{Project: "pa"},
		json.RawMessage(`{"what":"did a thing"}`)); err == nil {
		t.Error("a token with no user wrote a worklog entry")
	}
	if _, err := worklogAppend(ctx, m, seat, json.RawMessage(`{"next":"pick it up"}`)); err == nil {
		t.Error("an entry with nothing to say about what changed was accepted")
	}
	if _, err := worklogAppend(ctx, m, seat, json.RawMessage(`{"what":"   "}`)); err == nil {
		t.Error("an entry whose what is whitespace was accepted")
	}

	// An entry indexes what happened; a document belongs in a report and a fact
	// in memory, referenced from here.
	long, err := json.Marshal(map[string]string{"what": strings.Repeat("x", maxWorklogField+1)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worklogAppend(ctx, m, seat, long)
	if err == nil || !strings.Contains(err.Error(), "report_write") {
		t.Errorf("an oversize entry answered %v, want a refusal that says where it belongs", err)
	}

	// And the ceiling on refs, which is checked before the store is asked about
	// any of them.
	ids := make([]string, maxWorklogRefs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("01H%027d", i)
	}
	many, err := json.Marshal(map[string]any{"what": "touched everything", "refs": ids})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worklogAppend(ctx, m, seat, many); err == nil {
		t.Errorf("an entry referencing %d artifacts was accepted", len(ids))
	}
}

// An entry reads back as what was written, and an entry from a build that did
// not write meta still reads as something rather than as a hole in the
// chronology.
func TestWorklogEntryReadsBackWhatWasWritten(t *testing.T) {
	written := &store.Event{
		ID: "01H", Type: worklogEventType, Actor: "aa", Body: "the body",
		Meta: json.RawMessage(`{"actor_kind":"agent","actor_user":"ua",` +
			`"what":"wired the gate","next":"run it from clean","as_of":"0e3b7f6",` +
			`"refs":["01HREPORT"]}`),
	}
	got := entryOf(written)
	if got.What != "wired the gate" || got.Next != "run it from clean" || got.AsOf != "0e3b7f6" {
		t.Errorf("entry read back as %+v", got)
	}
	if len(got.Refs) != 1 || got.Refs[0] != "01HREPORT" {
		t.Errorf("refs read back as %v", got.Refs)
	}
	if got.Actor != "aa" {
		t.Errorf("the entry came back with actor %q, want the seat that wrote it", got.Actor)
	}

	bare := entryOf(&store.Event{ID: "01J", Type: worklogEventType, Actor: "ub", Body: "said only this"})
	if bare.What != "said only this" {
		t.Errorf("an entry with no meta read back as %q, want its body", bare.What)
	}
	if bare.Refs == nil {
		t.Error("refs came back null, which a client has to special-case")
	}
}

// The speaker survives an event whose meta is not all strings.
//
// A worklog entry carries its refs in meta as a list, and the timeline used to
// decode meta into map[string]string and ignore the error - so one non-string
// value dropped actor_kind and actor_user off the item, silently, and the
// console showed the entry as if nobody had written it.
func TestActivityItemKeepsTheSpeakerBesideAList(t *testing.T) {
	item := itemOf(&store.Event{
		ID: "01H", Type: worklogEventType, Actor: "aa",
		Meta: json.RawMessage(`{"actor_kind":"agent","actor_user":"ua","refs":["01HBUG"]}`),
	})
	if item.ActorKind != "agent" || item.ActorUser != "ua" {
		t.Errorf("the timeline lost the speaker: %+v", item)
	}
	if item.Kind != activityWorklog {
		t.Errorf("a worklog entry shows on the timeline as %q, want %q", item.Kind, activityWorklog)
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
