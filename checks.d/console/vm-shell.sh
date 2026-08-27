# shellcheck shell=bash
#
# THE SHELL PANEL: A REFUSAL, AN EMPTY STATE, AND A SOCKET THAT IS OPERATOR-ONLY.
#
# 01M12M5PMY. The operator: "add agents panel to the flow, and a run button
# which will bring fcvm with the shell relayed to the panel... we will test the
# wiring."
#
# WHAT IS ASSERTED HERE, and it is the half a gate can honestly answer:
#
#   the panel says what it is before anything runs, rather than drawing an
#   empty black rectangle - which is what a terminal that failed to connect
#   also looks like
#
#   /api/agent/socket is operator-only, asserted as a DIFFERENCE: the same
#   handshake with a non-operator token is refused and with an operator token
#   is not. One reading cannot tell a rule being enforced from a rule that does
#   not exist, and this door hands out a shell
#
#   the wasm ghostty needs is actually served. init() takes no URL and probes
#   /ghostty-vt.wasm, vite emits no asset nothing imports, and the first build
#   of this panel was GREEN with the file absent - a dead button arriving
#   through the build. A 404 here is that failure, caught by the gate rather
#   than by somebody pressing Run.
#
# WHAT IS NOT ASSERTED, said plainly so nobody reads a green as more than it is,
# and on the same grounds vm-panel.sh states for spawn: driving the relay end to
# end boots a real firecracker VM on the machine running the suite, on every
# pass, and leaves one behind if the run dies. Worse here than there - THIS
# SUITE ITSELF RUNS INSIDE A FIRECODE VM in the normal case, and firecode inside
# a firecode guest is nested virtualisation this host does not offer.
#
# So the arms that need a guest - that Run brings a VM up, that what is typed
# reaches the shell, that the shell is the GUEST'S and not this machine's, and
# that closing the panel stops the VM - are proven by hand on the host and the
# measurement is recorded on the row. The gap is real and is named rather than
# papered over with a check that would pass without ever having seen a VM.

the_shell_panel_refuses_and_says_what_it_is() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vm-shell-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "the shell panel is operator-only, says what it is, and its wasm is served" \
	the_shell_panel_refuses_and_says_what_it_is
