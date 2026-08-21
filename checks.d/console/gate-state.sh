# shellcheck shell=bash
#
# THE MERGES PANE SAYS WHETHER A GATE IS RUNNING.
#
# The operator, twice in one morning: "i have no idea what is going on, and i
# have no idea about gate and merger status", then "I want to see gate status
# and mergr status too here". The pane counted rows and never said what the
# machine was doing.
#
# WHY A COUNT CANNOT ANSWER IT: one gate at a time, about twelve minutes a pass,
# so the queue is correctly unlandable nearly all of its life. A working drainer
# and a dead one drew the same pane. The lock was on the wire the whole time and
# api.ts did not declare the field, so it never reached the component.
#
# THE ASSERTION IS A DIFFERENCE: the same page read twice, free and then with a
# run declared, and the two must not say the same thing. Master draws no line at
# all, so it fails on the first arm rather than on a phrase somebody reworded.
#
# It also pins the zero-time trap: lock.until arrives as 0001-01-01T00:00:00Z
# when nothing is held, because omitempty does not omit a time.Time, and that
# string parses happily into a deadline from the first century.

the_merges_pane_says_whether_a_gate_is_running() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/gate-state-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the merges pane says whether a gate is running" \
	the_merges_pane_says_whether_a_gate_is_running
