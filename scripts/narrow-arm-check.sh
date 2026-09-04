#!/usr/bin/env bash
# How many browser checks ever look at a narrow viewport, and is it going down?
#
#   scripts/narrow-arm-check.sh [web/scripts]
#
# 01M1PBFA0P479XMRGRT5VZJZQ5. Counted on 2026-09-04: of 140 check scripts, 127
# open a viewport of 1200px or wider and 4 open one under 1000. The operator
# uses a Fold 8. Three of their reports in recent days were narrow-only defects
# that the full suite called green for the whole time they existed - the thread
# drawer that drew nothing below lg, the separator that lost every touch drag,
# and the rail items from the same screen. A suite that only ever looks at a
# desk cannot see any of them.
#
# WHAT THIS IS AND IS NOT. It does not require any particular check to grow an
# arm - which checks have a width-dependent claim is a judgement, and forcing
# 140 of them to run twice would double an hour-long suite to re-measure things
# that do not vary with width. It is a RATCHET: the number that exists today is
# written down, and removing a narrow arm has to be deliberate rather than a
# side effect of rewriting a check.
#
# COUNTED BY THE SMALLEST VIEWPORT IN THE FILE, not by a pattern matched against
# how the line happens to be written. That distinction cost a real disagreement
# on the day this was filed: one count said 126 and another 127, and the
# difference was vms-refusal-check.mjs writing `const VIEWPORT = { width: 1400
# ... }` instead of putting it inline in newPage. A grep narrower than the thing
# it claims to count is the same defect as reading a limit as a set.
set -uo pipefail

DIR="${1:-web/scripts}"

# Under this is "narrow": a folded Fold 8 is 360 and an unfolded one is 880, so
# the bar has to sit above both. 1000 also keeps the tablet-ish widths out of
# the count, which would otherwise let a 1024 arm pass as phone coverage.
NARROW_BELOW=1000

# THE FLOOR, and the only line to edit when an arm is added or deliberately
# removed. Raising it is the point of the row; lowering it should require
# somebody to type the smaller number and say why in the commit.
FLOOR=7

die() {
	printf 'narrow-arm: %s\n' "$*" >&2
	exit 1
}

[ -d "$DIR" ] || die "no directory at $DIR"

# The smallest viewport width in the file, however the line is written.
#
# ONLY LINES THAT ACTUALLY SET A VIEWPORT. The first cut took the smallest
# `width:` anywhere and reported measure-check at 72px - which is `max-width:
# 72ch` in a PROSE COMMENT about typography. That check does have a narrow arm,
# at 900, so the count was right and the number beside it was nonsense; a
# reported width nobody can act on is how a counter stops being read. Comment
# lines are dropped for the same reason.
smallest_width() {
	{
		# a. `viewport: { width: N }` and `setViewportSize({ width: N })`.
		grep -E 'viewport|setViewportSize' "$1" 2>/dev/null |
			grep -vE '^[[:space:]]*(\*|//)' |
			grep -oE 'width:[[:space:]]*[0-9]+' |
			grep -oE '[0-9]+'
		# b. THE TUPLE FORM, which the two checks that matter most both use:
		#    for (const [name, width, height] of [["a folded phone", 360, 780], ...])
		# The first cut missed exactly these, and reported thread-on-a-phone at
		# 1600 and separator-keeps-the-gesture at none - the two checks written
		# BECAUSE of narrow defects. A counter blind to the thing it was built
		# to count is worse than no counter, because the number looks measured.
		grep -oE '\[[[:space:]]*"[^"]*"[[:space:]]*,[[:space:]]*[0-9]+[[:space:]]*,[[:space:]]*[0-9]+' "$1" 2>/dev/null |
			grep -oE ',[[:space:]]*[0-9]+' | head -100 |
			awk -F'[, ]+' '{print $2}'
	} | sort -n | head -1
}

total=0
narrow=0
narrow_names=""
for f in "$DIR"/*-check.mjs; do
	[ -e "$f" ] || continue
	total=$((total + 1))
	w=$(smallest_width "$f")
	[ -n "$w" ] || continue
	if [ "$w" -lt "$NARROW_BELOW" ]; then
		narrow=$((narrow + 1))
		narrow_names="$narrow_names $(basename "$f" .mjs)@${w}"
	fi
done

[ "$total" -gt 0 ] || die "no *-check.mjs under $DIR, so this counted nothing and must not report a pass"

printf 'narrow-arm: %d of %d browser checks open a viewport under %dpx:%s\n' \
	"$narrow" "$total" "$NARROW_BELOW" "$narrow_names"

if [ "$narrow" -lt "$FLOOR" ]; then
	die "narrow coverage fell to $narrow, from a floor of $FLOOR.
A check that only ever opens a desk-width window cannot see a drawer that never
opens, a control off the edge, or a gesture a touch browser takes - which is
every phone defect the operator has reported. If an arm was removed on purpose,
lower FLOOR in this file and say why in the commit; if it was not, put it back."
fi
