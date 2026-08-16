package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// The observability tools an agent gets.
//
// The names are serenedash's - status, activity, storage, anomalies - and that
// is the whole point of choosing them: an agent that has used serenedash
// already knows what these four do and what to expect back, so the fabric does
// not cost it a second vocabulary for the same four questions.
//
// They are the same measurements GET /api/metrics answers with, through the
// same code, behind the same filter. An agent asking "what is here" gets what
// its token may read, and an agent asking "is anything unusual" gets a refusal
// where there is not enough history - the same refusal, with the same words -
// rather than a number that would read as a finding.

// observabilityTools are appended to the tool list in mcp_tools.go.
var observabilityTools = []tool{
	{
		Name: "status",
		Description: "How this node and your corner of it are doing: uptime and " +
			"database health (the operator's view), the conversation and the work you " +
			"are party to, and replication. Numbers you cannot see say so rather than " +
			"reading as zero.",
		InputSchema: object(props{
			"scope": enum("Whose view. 'mine' is what your token may read; 'all' is the "+
				"whole node and is answered only for this node's operator.",
				[]string{"mine", "all"}),
		}, nil),
		call: statusTool,
	},
	{
		Name: "activity",
		Description: "The timeline: turns, run log lines, chat messages, steers and " +
			"worklog entries you may read, oldest first. Search it with q, narrow it " +
			"with kind, room or thread. This is where to look before asking what happened.",
		InputSchema: object(props{
			"q":      str("Plain substring of what was said. Not a query language."),
			"kind":   enum("Narrow to one kind.", []string{"chat", "turn", "log", "steer", "worklog"}),
			"room":   str("Narrow to one room."),
			"thread": str("Narrow to one thread - one run, or one subagent branch of it."),
			"since":  integer("Packed hlc cursor; the answer carries the next one."),
			"limit":  integer("Most items to return. Default 200."),
			"scope":  enum("Whose view.", []string{"mine", "all"}),
		}, nil),
		call: activityTool,
	},
	{
		Name: "storage",
		Description: "What the store holds that you may read: artifacts by type, scope, " +
			"project and owner, how much of it is indexed for search, and how fast it " +
			"is growing. Bytes on disk are the operator's view and are reported as " +
			"unavailable to anybody else.",
		InputSchema: object(props{
			"scope": enum("Whose view.", []string{"mine", "all"}),
		}, nil),
		call: storageTool,
	},
	{
		Name: "anomalies",
		Description: "Whether anything in your view is unusual, judged against this " +
			"node's own recorded history and never against a fixed threshold. Below " +
			"the minimum number of readings it answers 'insufficient samples' with the " +
			"count, rather than a verdict it cannot support.",
		InputSchema: object(props{
			"scope": enum("Whose view.", []string{"mine", "all"}),
		}, nil),
		call: anomaliesTool,
	},
}

// scopeArg reads the scope argument. 'all' is the operator's, exactly as
// ?scope=all is over HTTP: anybody else asking for it gets their own view and
// is told so in the answer's scope block rather than refused.
func scopeArg(p *store.Principal, asked string) bool {
	return asked == "all" && p != nil && p.Operator
}

// metricsSource is what the node half of the metrics needs. The MCP server has
// its own start time and its own tracer, so `status` over stdio reports the
// process the agent is actually talking to rather than some other one.
func (m *mcpServer) metricsSource() metricsSource {
	return metricsSource{node: m.node, started: m.started, tracer: m.tracer, cpu: &m.cpu}
}

func statusTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	all := scopeArg(p, a.Scope)
	return buildMetrics(ctx, m.db, p, all, m.metricsSource(),
		[]string{groupNode, groupCollab, groupSync})
}

func storageTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	all := scopeArg(p, a.Scope)
	return buildMetrics(ctx, m.db, p, all, m.metricsSource(),
		[]string{groupCorpus, groupPerms})
}

func anomaliesTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	all := scopeArg(p, a.Scope)
	return buildMetrics(ctx, m.db, p, all, m.metricsSource(), []string{groupAnomalies})
}

// activityTool is the timeline, as an agent reads it.
func activityTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Q      string `json:"q"`
		Kind   string `json:"kind"`
		Room   string `json:"room"`
		Thread string `json:"thread"`
		Since  int64  `json:"since"`
		Limit  int    `json:"limit"`
		Scope  string `json:"scope"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	var types []string
	if a.Kind != "" {
		kind, err := oneOf("kind", a.Kind, sortedReadKinds(), "")
		if err != nil {
			return nil, err
		}
		types = append(types, readableKinds[kind])
	}

	list, err := m.db.ListEvents(ctx, p, store.EventQuery{
		Thread:   a.Thread,
		Room:     a.Room,
		Types:    types,
		Contains: a.Q,
		Since:    a.Since,
		ScopeAll: scopeArg(p, a.Scope),
		Limit:    a.Limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]activityItem, 0, len(list))
	for _, e := range list {
		items = append(items, itemOf(e))
	}
	cursor := a.Since
	if len(list) > 0 {
		cursor = list[len(list)-1].SeqHLC
	}
	return map[string]any{
		"count": len(items), "items": items, "since": a.Since, "cursor": cursor,
		"query": a.Q,
	}, nil
}

// mcpOperator is the user id this MCP server treats as the operator, from the
// same environment variable `flowy serve` reads.
//
// It is local configuration and never a row, for the reason it is everywhere
// else here: operator-ness is a fact about the machine, and a fact about the
// machine must not be something that can replicate onto it.
func mcpOperator() string { return strings.TrimSpace(envOr("FLOWY_OPERATOR", "")) }

// startedNow is the moment an MCP server came up, which is what `status`
// reports as uptime.
func startedNow() time.Time { return time.Now() }
