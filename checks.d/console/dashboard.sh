# shellcheck shell=bash
#
# A DASHBOARD ROW RENDERS DECLARED TILES OVER PUSHED METRIC ROWS, AND NOTHING
# ELSE.
#
# The operator, 01M0WY7F5: agents author dashboards for their activity, "start
# stop pause and monitor", asap. The contract, answered on the row: a dashboard
# is a memory row of kind `dashboard` whose fields declare tiles - a fixed
# vocabulary (number, table) over a named metric - and the console renders the
# declaration. It RUNS nothing: producers push metric rows through the ordinary
# artifact door, the dashboard declares queries over them, and every number
# shows its age from the row it reads. Past a tile's threshold the datum is
# styled stale, not silently live - the operator reading prose somebody typed
# is exactly the failure this exists to fix. Scope decides who reads it: a
# principal outside the rows' projects is refused.
#
# THREE ARMS, of which the second is the one a component test would miss:
#
#   1. an agent authors a dashboard and metric rows through the API; the page
#      lists the dashboard and renders each declared number with its age;
#   2. a principal from another project opens it and is refused - and their
#      metrics read comes back empty;
#   3. a newer metric row shows on reload with a fresh age, and a stale tile
#      is styled stale, not silently current.
#
# TWO TOKENS, AND THAT IS THE POINT. The author writes the rows; the outsider
# proves the scope arm, because a check with one token could not prove
# "readable by me, refused for everybody else". A declared tile whose metric
# was never pushed says so - the honest third state, instead of a plausible
# zero.

a_dashboard_renders_declared_tiles_from_pushed_rows() {
	cd "$ROOT/web" || return 1
	node scripts/dashboard-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_A_PC"
}

check "a dashboard renders declared tiles over pushed metric rows, refuses outsiders, and shows ages" \
	a_dashboard_renders_declared_tiles_from_pushed_rows
