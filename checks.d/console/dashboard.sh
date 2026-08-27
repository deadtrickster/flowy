# shellcheck shell=bash
#
# A DASHBOARD ROW RENDERS DECLARED TILES OVER PUSHED METRIC ROWS, AND NOTHING
# ELSE.
#
# The operator, 01M0WY7F5: agents author dashboards for their activity, "start
# stop pause and monitor", asap. The contract, answered on the row: a dashboard
# is a memory row of kind `dashboard` whose fields declare tiles - a fixed
# vocabulary (number, table, grid, frame, gauge, series, log, report) over a named
# metric - and the console renders the declaration. It RUNS nothing: producers
# push metric rows through the ordinary artifact door, the dashboard declares
# queries over them, and every number shows its age from the row it reads. A
# series tile draws its metric's newest readings as a sparkline - oldest left,
# newest pinned, the window's own truncated flag - and says so when the points
# are not numbers. A gauge tile draws a value WITH ITS BOUNDS - the scale and
# thresholds travel beside the reading, never on the tile - and reads its
# direction off the threshold order: crit above warn means high is bad, crit
# below warn means low is bad. A report tile draws the document style - the
# reading is the whole page, header plus sections of the closed vocabulary -
# with tone words mapped to the palette, an unknown word drawn as no tone,
# and card sparks as metric refs asked at their own window. A log tile draws
# its stream's last lines oldest-first - each level a tag in its severity
# colour, a level-less line as plain text - and the counts the door computed
# over the window; a stream never pushed says so. Past a tile's
# threshold the datum is styled stale, not silently live - the operator
# reading prose somebody typed is exactly the failure this exists to fix.
# Scope decides who reads it: a principal outside the rows' projects is
# refused.
#
# NINE ARMS, of which the second is the one a component test would miss:
#
#   1. an agent authors a dashboard and metric rows through the API; the page
#      lists the dashboard and renders each declared number with its age;
#   2. a principal from another project opens it and is refused - and their
#      metrics read comes back empty;
#   3. a newer metric row shows on reload with a fresh age, and a stale tile
#      is styled stale, not silently current;
#   4. a frame tile draws a pushed frame reading exactly - every run pinned
#      to its column, fill bars as rects, angle-bracket text as text - and
#      answers the pointer from the frame's own legend while j/k, pgup/pgdn,
#      home/end and esc drive a visible cursor row;
#   5. a series tile draws its window oldest-first - rising values read as a
#      rising line in coordinates - pins the newest point, flags a truncated
#      window, refuses non-numeric points, and a series tile over a
#      never-pushed metric says so;
#   6. a gauge tile draws its value with the pushed bounds - fill min to
#      value, warn and crit bands where the thresholds say, direction off the
#      order, severity colouring the fill off the palette - and a reading
#      that cannot be placed says so;
#   7. a report tile draws its pushed document - header and all four section
#      kinds, segment widths over the pushed total, tone words mapped to the
#      palette with an unknown word drawn as no tone, a card spark answered
#      at its own window - and a reading that is not a document says so;
#   8. a row pushed after load wins its tile without a reload - the page
#      re-fetches on its own beat, so a dashboard left open follows the
#      producers instead of freezing at page load;
#   9. a log tile draws its stream's last lines oldest-first - each level a
#      tag in its severity colour, a level-less line drawn as plain text -
#      and the counts the door computed over the window; a stream never
#      pushed says so, rather than drawing an empty list that reads as
#      silence.
#
# TWO TOKENS, AND THAT IS THE POINT. The author writes the rows; the outsider
# proves the scope arm, because a check with one token could not prove
# "readable by me, refused for everybody else". A declared tile whose metric
# was never pushed says so - the honest third state, instead of a plausible
# zero.

a_dashboard_renders_declared_tiles_from_pushed_rows() {
	cd "$ROOT/web" || return 1
	node scripts/dashboard-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_A_PC" || return 1
	# The outsider's browser session mounts the console shell like any visit,
	# and the shell declares its own console:<room> readers - one row per
	# token, keyed by principal, so this check leaves A-in-pc's reader behind.
	# The unread reader-row check queries A's rows without naming a project,
	# so a second row reads as one two-line mark and breaks its arithmetic.
	# A check leaves the shared database as it found it: delete what it made.
	scalar "DELETE FROM inbox_readers
	         WHERE reader LIKE 'console:%'
	           AND principal = '$USER_A' || chr(31) || chr(31) || 'pc'" \
		>/dev/null || return 1
}

check "a dashboard renders declared tiles over pushed metric rows, refuses outsiders, and shows ages" \
	a_dashboard_renders_declared_tiles_from_pushed_rows
