#!/usr/bin/env bash
#
# WHO HOLDS THE GATE LOCK, and does that process still exist.
#
# 01M0HN5GA0: a suite died badly, the gate stayed locked, and three agents
# diagnosed it three ways in twenty minutes - two of them wrong - because every
# tool that is easy to reach answers a different question:
#
#   lsof         lists file descriptors, and an fd is a handle, not the lock
#   ps / pgrep   the holder was not running, so nothing named it
#   flock -n     says HELD and cannot say by whom
#   fuser -k     kills every process holding the FILE, which includes the live
#                drainer waiting for the lock it is about to get
#
# /proc/locks is the only authority, and it has a trap of its own: its inode
# column is DECIMAL. Grepping it for a hex inode reports the lock free.
#
# This prints the holder, whether that pid still exists, and every process
# holding an fd on the lock file - separated by working directory, which is what
# told the dead run's postgres apart from the drainer's live one on the day.
# Names could not: all of them are called postgres.
#
# It kills nothing. The judgement about which inheritors are garbage belongs to
# whoever reads this, and the one that is not garbage is somebody's running gate.

set -uo pipefail

lock=${1:-${FLOWY_GATE_LOCK:-${TMPDIR:-/tmp}/flowy-gate.lock}}

if [ ! -e "$lock" ]; then
	printf 'no lock file at %s - nothing has taken the gate on this box\n' "$lock"
	exit 0
fi

ino=$(stat -c %i "$lock" 2>/dev/null) || {
	printf 'cannot stat %s\n' "$lock" >&2
	exit 2
}
dev=$(stat -c %D "$lock" 2>/dev/null)
printf 'lock   %s\n' "$lock"
printf 'inode  %s (decimal, which is how /proc/locks writes it) on device %s\n' "$ino" "$dev"

# The lock line names its inode in the third field of the id, decimal.
holder=""
queued=()
while read -r line; do
	# FLOCK lines look like: 141: FLOCK ADVISORY WRITE 2319478 00:2d:14179069 0 EOF
	# and a waiter is the same with "-> " after the number.
	case "$line" in
	*FLOCK*:"$ino"*) ;;
	*) continue ;;
	esac
	pid=$(printf '%s\n' "$line" | awk '{for (i=1;i<=NF;i++) if ($i ~ /^[0-9]+$/ && i>2) {print $i; exit}}')
	if printf '%s\n' "$line" | grep -q -- '->'; then
		queued+=("$pid")
	else
		holder=$pid
	fi
done </proc/locks

if [ -z "$holder" ]; then
	printf 'holder none - the lock is FREE\n'
else
	if kill -0 "$holder" 2>/dev/null; then
		printf 'holder %s, ALIVE: %s\n' "$holder" \
			"$( (tr '\0' ' ' </proc/"$holder"/cmdline) 2>/dev/null | cut -c1-80)"
	else
		# A GONE HOLDER IS THE ORDINARY STATE HERE, and reading it as a fault is
		# a mistake I made with this script open in front of me.
		#
		# The suite takes the lock by running `flock -w N 9` as a CHILD on an
		# inherited fd. That child exits the moment it has the lock, so the pid
		# the kernel recorded is nearly always gone - while the lock is held,
		# correctly, by the suite through the same open file description.
		#
		# So this line does not mean anything is wrong. What tells a healthy
		# gate from a wedged one is the list below: if a LIVE run-tests.sh is
		# among the holders, the gate is somebody's running suite and must be
		# left alone. If the only holders are a dead run temporary directory,
		# the lock is being held by garbage.
		printf 'holder %s, GONE - normal: the taker is a short-lived flock child.\n' "$holder"
		printf '       what matters is whether a LIVE suite is in the list below.\n'
	fi
fi
for q in "${queued[@]:-}"; do
	[ -n "$q" ] || continue
	printf 'queued %s: %s\n' "$q" \
		"$( (tr '\0' ' ' </proc/"$q"/cmdline) 2>/dev/null | cut -c1-80)"
done

printf '\nprocesses holding an fd on that file, by working directory:\n'
found=0
for p in /proc/[0-9]*; do
	pid=${p#/proc/}
	for fd in "$p"/fd/*; do
		target=$(readlink "$fd" 2>/dev/null) || continue
		[ "$target" = "$lock" ] || continue
		found=1
		printf '  %-8s cwd=%-42s %s\n' "$pid" \
			"$(readlink "$p/cwd" 2>/dev/null)" \
			"$( (tr '\0' ' ' <"$p"/cmdline) 2>/dev/null | cut -c1-46)"
		break
	done
done
[ "$found" = 1 ] || printf '  (none)\n'

# AND THE ONE QUESTION THAT DECIDES IT, answered rather than left to the reader:
# is any live suite holding this lock?
live=""
for p in /proc/[0-9]*; do
	pid=${p#/proc/}
	# 2>/dev/null on the READ as well as the readlink: /proc entries disappear
	# between the glob and the open, and a process that exited while this was
	# looking at it is not an error - it is the answer, which is that it is not
	# holding anything.
	args=$( (tr '\0' ' ' <"$p"/cmdline) 2>/dev/null || true)
	case "$args" in
	*run-tests.sh*) ;;
	*) continue ;;
	esac
	for fd in "$p"/fd/*; do
		[ "$(readlink "$fd" 2>/dev/null)" = "$lock" ] || continue
		live="$live $pid"
		break
	done
done

printf '\n'
if [ -n "$live" ]; then
	printf 'A LIVE SUITE HOLDS THIS LOCK:%s. Leave it alone - it is somebody gate.\n' "$live"
else
	printf 'NO LIVE SUITE HOLDS THIS LOCK. Every holder above is a leftover, and the\n'
	printf 'gate is blocked on garbage. They are separable by working directory, not\n'
	printf 'by name - all of them are called postgres.\n'
fi
