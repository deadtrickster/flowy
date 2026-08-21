#!/usr/bin/env bash
#
# THE GATE LOCK DIES WITH THE SUITE THAT TOOK IT.
#
# 01M0HN5GA0. A suite wedged, was SIGKILLed, and the gate stayed locked for
# thirteen minutes with the drainer queued behind a pid that no longer existed.
# The lock had been taken on a plain fd, a flock lives on the OPEN FILE
# DESCRIPTION, and every child inherits it - so the postgres and the browser
# check the run left behind kept the gate shut after the run was gone.
#
# THIS ASSERTS THE DIFFERENCE, not the fix in isolation. One reading cannot tell
# "the lock was released" from "nothing ever held it", so both shapes are run
# against scratch lock files of their own and the two answers must differ:
#
#   an inherited fd     an orphan still holds the lock after the shell is killed
#   flock --close       it does not
#
# The first arm is the positive control. If it ever stops holding - a kernel
# change, a different flock - then the second arm proves nothing and this says
# so rather than passing.
#
# Nothing here touches the real gate lock: both arms work in $TMPDIR under names
# of their own, and neither takes /tmp/flowy-gate.lock.

set -uo pipefail

work=$(mktemp -d "${TMPDIR:-/tmp}/gate-lock-check.XXXXXX") || exit 2
cleanup() {
	# The orphans this check makes ON PURPOSE. They are the whole experiment, and
	# a check that leaves them behind is a check that becomes the bug it tests.
	for pid in ${orphans:-}; do
		kill -9 "$pid" 2>/dev/null || true
	done
	rm -rf "$work"
}
trap cleanup EXIT
orphans=""

fail() {
	printf '%s\n' "$1" >&2
	exit 1
}

# held reports whether a lock file is locked, without taking it for longer than
# the question. `flock -n ... -c true` is the whole test: it succeeds only when
# the lock was free, and releases immediately.
held() {
	if flock -n "$1" -c true 2>/dev/null; then
		return 1
	fi
	return 0
}

# ---- arm one: an inherited fd. The shape run-tests.sh used to have. ----
lock_a="$work/inherited.lock"
: >"$lock_a"
bash -c '
	exec 9>"$1"
	flock -n 9 || exit 3
	sleep 300 &
	echo $! > "$2"
	sleep 300
' _ "$lock_a" "$work/a.pid" &
shell_a=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
	[ -s "$work/a.pid" ] && break
	sleep 0.2
done
orphan_a=$(cat "$work/a.pid" 2>/dev/null || true)
[ -n "$orphan_a" ] || fail "the control never started a child - nothing below is measured"
orphans="$orphans $orphan_a"
held "$lock_a" || fail "the control did not take its own lock, so this check cannot measure anything"

# Killed the way the wedged suite was killed: no trap gets to run.
kill -9 "$shell_a" 2>/dev/null || true
wait "$shell_a" 2>/dev/null
sleep 0.3
kill -0 "$orphan_a" 2>/dev/null || fail "the control's orphan died with its parent, so the arm proves nothing"
if ! held "$lock_a"; then
	fail "THE POSITIVE CONTROL FAILED: an orphan of a killed shell does not hold an
inherited flock on this machine. Either the kernel or flock has changed, and the
--close arm below no longer proves anything. Do not weaken this - find out why."
fi

# ---- arm two: --close. The shape it has now. ----
lock_b="$work/closed.lock"
: >"$lock_b"
flock -w 5 -o "$lock_b" -c '
	sleep 300 &
	echo $! > "'"$work"'/b.pid"
	sleep 300
' &
outer_b=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
	[ -s "$work/b.pid" ] && break
	sleep 0.2
done
orphan_b=$(cat "$work/b.pid" 2>/dev/null || true)
[ -n "$orphan_b" ] || fail "the --close arm never started a child"
orphans="$orphans $orphan_b"
held "$lock_b" || fail "flock -o did not hold the lock while its command ran"

pkill -9 -P "$outer_b" 2>/dev/null || true
kill -9 "$outer_b" 2>/dev/null || true
wait "$outer_b" 2>/dev/null
sleep 0.3
kill -0 "$orphan_b" 2>/dev/null || fail "the --close arm's orphan died too, so the two arms are not comparable"
if held "$lock_b"; then
	fail "an orphan of a --close run STILL HOLDS the gate lock. That is the defect
01M0HN5GA0 records: a suite that dies badly leaves the gate shut, the drainer
queues behind a dead pid, and the queue reports itself as gating."
fi

# ---- and the suite itself uses the second shape ----
suite=${1:-run-tests.sh}
[ -f "$suite" ] || fail "no $suite to read"
# shellcheck disable=SC2016  # the $ is grep's, not the shell's: this looks for
# the literal string `exec 9>"$GATE_LOCK"` in the suite's source.
if grep -qE '^[[:space:]]*exec 9>"\$GATE_LOCK"' "$suite"; then
	fail "$suite still takes the gate lock on an inherited fd (exec 9>\$GATE_LOCK).
The two arms above say what that costs."
fi
grep -q 'flock .*-o' "$suite" ||
	fail "$suite does not take the gate lock with flock --close"

printf 'an orphan holds an inherited flock and does not hold a --close one; %s uses --close\n' "$suite"
