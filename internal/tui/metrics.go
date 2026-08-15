package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The metrics view: GET /api/metrics as compact text panels.
//
// Two things about it are the node's and not this client's. The scope is the
// token's - a is a request for the node's own view and comes back as your own
// numbers unless you are the operator, and the scope block in the response says
// which happened, so that is what the header prints. And a group that could not
// be measured says why: this pane renders the reason where it would otherwise
// render a zero, because a dashboard that shows "0 artifacts" for "we could not
// read the artifacts" is a dashboard that says everything is fine when nothing
// was looked at.

func (m *Model) metricsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a":
		m.scopeAll = !m.scopeAll
		m.scroll = 0
		return m, m.metricsCmd(m.scopeAll)
	}
	if scroll, moved := moveSel(msg.String(), m.scroll, m.scrollMax+1, m.height/2); moved {
		m.scroll = scroll
	}
	return m, nil
}

func (m *Model) metricsView(height int) []string {
	if m.metrics == nil {
		return []string{m.theme.Dim.Render(m.theme.clip("reading metrics...", m.width))}
	}
	mx := m.metrics
	scope := mx.Scope.Key
	if scope == "" {
		scope = "self"
	}
	asked := ""
	if m.scopeAll && !mx.Scope.All {
		// Asked for the node's view, given your own. Saying so is the whole
		// difference between "the node is quiet" and "these are not the node's
		// numbers".
		asked = " (asked for all; the token is not the operator's)"
	}
	lines := []string{
		m.theme.Title.Render(m.theme.clip(fmt.Sprintf("%s %s - scope %s%s",
			mx.Node, mx.Version, scope, asked), m.width)),
	}

	lines = append(lines, m.nodePanel()...)
	lines = append(lines, m.corpusPanel()...)
	lines = append(lines, m.collabPanel()...)
	lines = append(lines, m.syncPanel()...)
	lines = append(lines, m.permsPanel()...)
	lines = append(lines, m.anomaliesPanel()...)

	return m.scrolled(lines, height)
}

// panelHead is a group's title and, when the group could not be measured, the
// reason on the line under it.
//
// On its own line because the reasons this node gives are sentences - "the
// replication cursors are the operator's view of this node" - and a sentence
// appended to a heading is a heading nobody can pick out of the page.
func (m *Model) panelHead(name string, a Availability) ([]string, bool) {
	head := m.theme.Label.Render(m.theme.clip(name, m.width))
	if a.Available {
		return []string{head}, true
	}
	reason := a.Reason
	if reason == "" {
		reason = "not measured"
	}
	return []string{head, m.theme.Err.Render(m.theme.clip("  "+reason, m.width))}, false
}

func (m *Model) nodePanel() []string {
	g := m.metrics.Groups.Node
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("node", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	lines = append(lines, m.theme.clip(fmt.Sprintf("  up %s   build %s",
		duration(g.UptimeS), g.Build), m.width))
	if g.DB != nil {
		state := "down"
		if g.DB.Up {
			state = "up"
		}
		lines = append(lines, m.theme.clip(fmt.Sprintf("  db %s %s  %.1fms  hlc %d",
			g.DB.Engine, state, g.DB.LatencyMS, g.DB.HLC), m.width))
	}
	if g.Pool != nil && g.Pool.MaxOpen > 0 {
		lines = append(lines, m.theme.clip(fmt.Sprintf("  pool %s %d/%d in use",
			m.theme.bar(float64(g.Pool.InUse), float64(g.Pool.MaxOpen), 16),
			g.Pool.InUse, g.Pool.MaxOpen), m.width))
	}
	if g.CPU != nil && g.CPU.Available {
		lines = append(lines, m.theme.clip(fmt.Sprintf("  cpu  %s %.2f of %d cores",
			m.theme.bar(g.CPU.CoreShare, float64(max(1, g.CPU.Cores)), 16),
			g.CPU.CoreShare, g.CPU.Cores), m.width))
	}
	if g.Memory != nil && g.Memory.Available {
		lines = append(lines, m.theme.clip(fmt.Sprintf("  rss  %s", bytesOf(g.Memory.RSSBytes)), m.width))
	}
	if g.Traces != nil && g.Traces.Available {
		lines = append(lines, m.theme.clip(fmt.Sprintf("  traces kept %d dropped %d",
			g.Traces.Kept, g.Traces.Dropped), m.width))
	}
	return lines
}

func (m *Model) corpusPanel() []string {
	g := m.metrics.Groups.Corpus
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("corpus", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	lines = append(lines, m.theme.clip(fmt.Sprintf("  %d artifacts, %d events  (+%d artifacts / 24h, +%d / 7d)",
		g.Artifacts, g.Events, g.Growth.Artifacts24h, g.Growth.Artifacts7d), m.width))
	if by := topCounts(g.ByType, 6); by != "" {
		lines = append(lines, m.theme.clip("  by type   "+by, m.width))
	}
	if by := topCounts(g.ByScope, 6); by != "" {
		lines = append(lines, m.theme.clip("  by scope  "+by, m.width))
	}
	if g.Storage.Available && g.Storage.TotalBytes > 0 {
		lines = append(lines, m.theme.clip("  storage   "+bytesOf(g.Storage.TotalBytes), m.width))
	}
	return lines
}

func (m *Model) collabPanel() []string {
	g := m.metrics.Groups.Collab
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("collaboration", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	series := make([]float64, 0, len(g.MessagesByDay))
	for _, day := range g.MessagesByDay {
		series = append(series, float64(day.Count))
	}
	spark := m.theme.sparkline(series, min(28, max(8, m.width-40)))
	lines = append(lines, m.theme.clip(fmt.Sprintf("  %d messages / 24h  %s", g.Messages24h, spark), m.width))
	lines = append(lines, m.theme.clip(fmt.Sprintf(
		"  rooms %d  people %d  agents %d  handoffs in flight %d  open todos %d",
		g.ActiveRooms24h, g.ActiveUsers24h, g.ActiveAgents24h,
		g.HandoffsInFlight, g.OpenTodos), m.width))
	if by := topCounts(g.TasksByState, 5); by != "" {
		lines = append(lines, m.theme.clip("  tasks     "+by, m.width))
	}
	return lines
}

func (m *Model) syncPanel() []string {
	g := m.metrics.Groups.Sync
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("sync", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	lines = append(lines, m.theme.clip(fmt.Sprintf(
		"  local hwm %d  offline queue %d  conflicts %d  peers %d",
		g.LocalHWM, g.OfflineQueue, g.ConflictsTotal, len(g.Peers)), m.width))
	for _, peer := range g.Peers {
		lines = append(lines, m.theme.clip(fmt.Sprintf(
			"  %-18s pull %d push %d pending %d applied %d refused %d",
			m.theme.clip(peer.Peer, 18), peer.PullCursor, peer.PushedCursor,
			peer.PendingPush, peer.Applied, peer.Refused), m.width))
	}
	return lines
}

func (m *Model) permsPanel() []string {
	g := m.metrics.Groups.Permissions
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("permissions", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	lines = append(lines, m.theme.clip(fmt.Sprintf(
		"  grants %d  artifact shares %d  cross-project %d  tombstoned %d",
		g.Grants, g.ArtifactShares, g.CrossProjectGrants, g.TombstonedGrants), m.width))
	line := fmt.Sprintf("  refused in 24h %d", g.Denied24h)
	if by := topCounts(g.DeniedByStatus, 4); by != "" {
		line += "  " + by
	}
	lines = append(lines, m.theme.clip(line, m.width))
	return lines
}

// anomaliesPanel flags what the node called unusual, and shows what it refused
// to call anything: a verdict below the minimum sample count is not "normal".
func (m *Model) anomaliesPanel() []string {
	g := m.metrics.Groups.Anomalies
	if g == nil {
		return nil
	}
	head, ok := m.panelHead("anomalies", g.Availability)
	lines := append([]string{""}, head...)
	if !ok {
		return lines
	}
	lines = append(lines, m.theme.clip(fmt.Sprintf(
		"  %d unusual, %d refused a verdict (needs %d samples)",
		g.Unusual, g.Insufficient, g.MinSamples), m.width))
	for _, series := range g.Series {
		switch series.Verdict {
		case "unusual":
			lines = append(lines, m.theme.Err.Render(m.theme.clip(fmt.Sprintf(
				"  ! %-24s %.0f vs %.1f baseline (z %.1f)",
				series.Series, series.Latest, series.Baseline, series.Z), m.width)))
		case "insufficient samples":
			lines = append(lines, m.theme.Dim.Render(m.theme.clip(fmt.Sprintf(
				"  ? %-24s no verdict: %d of %d samples",
				series.Series, series.Samples, series.Required), m.width)))
		}
	}
	return lines
}

// topCounts renders a map as "a 3  b 2", biggest first, capped.
func topCounts(counts map[string]int, limit int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	return strings.Join(parts, "  ")
}

// duration is seconds as something a person reads at a glance.
func duration(seconds float64) string {
	switch {
	case seconds >= 86400:
		return fmt.Sprintf("%.1fd", seconds/86400)
	case seconds >= 3600:
		return fmt.Sprintf("%.1fh", seconds/3600)
	case seconds >= 60:
		return fmt.Sprintf("%.0fm", seconds/60)
	default:
		return fmt.Sprintf("%.0fs", seconds)
	}
}

// bytesOf is a byte count in the unit it belongs in.
func bytesOf(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 3; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGT"[exp])
}
