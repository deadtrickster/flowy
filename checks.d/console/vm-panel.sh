# shellcheck shell=bash
#
# THE VMs PANEL TELLS FOUR ANSWERS APART.
#
# 01M0G0KT52, the operator's ask (3): "I want to be able to spawn agent right
# from flow - inside fc VM". Every door existed on the node before this - see
# api_vm.go - and `grep -rn 'api/vm' web/src` returned nothing, so the panel is
# the console catching up rather than a new capability.
#
# WHAT IS ASSERTED is the error half, because that is where this class of panel
# fails. api_vm.go answers 503 rather than an empty list when the host has no
# firecode, deliberately, because "no VMs are running" and "this node cannot run
# VMs" are different facts. A console that catches every failure into a blank
# page undoes that at the last layer. So the same page is read with an operator
# token and a non-operator token and the two must differ.
#
# WHAT IS NOT ASSERTED, said here so nobody reads a green as more than it is:
# spawn, say and down are not driven end to end. Doing so boots a real
# firecracker VM on the machine running the suite, on every pass, and leaves one
# behind if the run dies between spawn and down. The row carries that gap.

the_vms_panel_tells_four_answers_apart() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vm-panel-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "the vms panel tells a refusal from an answer" \
	the_vms_panel_tells_four_answers_apart
