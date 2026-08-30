# shellcheck shell=bash
#
# SENDING WHAT THE TERMINAL SHOWED.
#
# 01M1558DPM1HRGZNJGMVW24DHF item 4, which that row calls the highest-value
# small feature on its list: telling another agent what happened meant retyping
# the screen or describing it, and both lose the exact bytes and which lines
# they were.
#
# IT DOES NOT DRIVE A BROWSER, deliberately. The obvious check - open /vms, drag
# over the terminal, press send - cannot run here: /api/agent/socket is
# operator-only and refused for a token-only console, so the terminal has no
# output and there is nothing to select. Dragging over an empty black rectangle
# would assert that zero lines format correctly, which is true of a function
# that does nothing.
#
# So the pure part is driven directly, through esbuild, the way clock-check.mjs
# does - the real module rather than a copy of it.

a_terminal_selection_reads_as_output_in_a_room() {
	cd "$ROOT/web" || return 1
	node scripts/termselect-check.mjs
}

check "a selected range of terminal becomes a numbered, fenced message" \
	a_terminal_selection_reads_as_output_in_a_room
