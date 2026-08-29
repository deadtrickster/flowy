# shellcheck shell=bash
#
# THE BOTTOM ROW OF A FULL-SCREEN PROGRAM.
#
# 01M15735PA. The operator: "started htop - bottom truncated".
#
# The row carried two candidates and no measurement, which is why it sat: the
# FitAddon measuring an element that sizes to its own content, or our own
# resize path telling the pty a number taken before the box had its final
# height. Both were guesses, and guessing twice on this panel had already been
# wrong twice.
#
# What is asserted here is neither mechanism but the thing a person sees: the
# screen box does not contain more terminal than it displays. Either candidate
# violates it, and no arrangement of them passes while a row is missing.
#
# IT NEEDS THE OPERATOR'S TOKEN AND CANNOT BE TAKEN WITHOUT ONE. /api/vm/* is
# operatorOnly, so with an agent seat the panel is not rendered at all -
# [data-vm-shell] resolves to nothing, and there is no box to measure. That is
# why this lives in the suite, which holds TOKEN_OP, rather than in a script an
# agent could run against the live node.
#
# IT BOOTS NOTHING. The selector is set to "this host" before Run, and stop is
# pressed before the verdict - VmShell says plainly that closing the socket is
# not a stop and leaves the VM running, so a check that pressed Run on a
# microVM would strand a guest on every pass.

the_terminal_fits_the_box_that_shows_it() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vm-rows-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP"
}

check "the terminal fits its box, so a full-screen program keeps its bottom row" \
	the_terminal_fits_the_box_that_shows_it
