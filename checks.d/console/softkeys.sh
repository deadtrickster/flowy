# shellcheck shell=bash
#
# A PHONE'S BACKSPACE.
#
# 01M1558DPM1HRGZNJGMVW24DHF item 5. Android soft keyboards report keydown 229
# for keys that are not composition, and ghostty-web's handleKeyDown returns
# early on 229 while its own beforeinput listener calls preventDefault. So
# backspace on a phone reaches nothing at all - which is the case item 3 just
# made reachable, since "I started it on the laptop and I am on the phone" ends
# at a terminal you cannot correct a typo in.
#
# THE DESKTOP HALF IS WHY THIS IS CHECKED AT ALL. A real keystroke fires both a
# keydown ghostty already sent AND a beforeinput for the same character, so the
# naive fix types everything twice for every user on a real keyboard. That
# regression is asserted here beside the phone case, because it is the more
# likely failure and the more damaging one.
#
# NO BROWSER AND NO DEVICE. What is under test is the decision the module makes
# between two events, with an injected clock - so it is exact and takes
# milliseconds, where emulating an Android keydown 229 in a real browser is not
# something playwright can do at all.

soft_keyboard_keys_reach_the_shell() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/softkeys-check.mjs
}

check "a soft keyboard's backspace and enter reach the shell, and a real keyboard still sends once" \
	soft_keyboard_keys_reach_the_shell
