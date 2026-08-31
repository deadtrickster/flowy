# shellcheck shell=bash
#
# THE URL A DEV SERVER PRINTED.
#
# 01M1558DPM1HRGZNJGMVW24DHF item 6. Start vite or next in the panel and it
# prints a loopback URL the terminal can only draw as text.
#
# FINDING IT IS THE EASY PART. What this asserts is the three ways it goes
# wrong. Whose loopback it is: 127.0.0.1 from a shell on this host is the
# browser's own machine, the same string from inside a microVM is the GUEST's,
# and opening that from here reaches the person's own machine on that port - at
# best nothing, at worst something else of theirs. A link that quietly goes
# somewhere else is worse than no link, so a guest URL is said and not linked.
#
# Split writes, because a pty flushes when it flushes and the banner is
# colourised, so the escape sequences sit in the seam. And reporting it once,
# because a buffer that keeps its tail wholesale rediscovers a URL it already
# reported on every later write - a server stopped ten minutes ago would keep
# offering its address for as long as anything else printed.
#
# No browser: this is a scanner over a byte stream, so the check feeds it bytes.

a_dev_server_url_is_read_correctly() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/devurl-check.mjs
}

check "a dev server's URL is found, made visitable, and not offered when it is the guest's" \
	a_dev_server_url_is_read_correctly
