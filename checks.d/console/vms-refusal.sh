# shellcheck shell=bash
#
# A REFUSED /vms IS A PAGE, NOT A HEADLINE ON A BLACK FIELD.
#
# The operator, on a screenshot of the live console: "/vms for a non-operator is
# a headline and one sentence on a full black page. The refusal is right and the
# emptiness is not - 95% of the viewport says nothing."
#
# So the refusal has to survive AND the page has to stop being mostly nothing,
# and the second is measured as covered area rather than as the presence of the
# element that covers it. Counting the markup that produces a visual property is
# a different claim from measuring the property: that substitution shipped a
# broken message list here with a green check on it earlier the same day.
#
# It reads the page with the NON-operator token, and refuses to pass if that
# token turns out to open the page - a check that never saw a refusal would
# otherwise report green on the one case it exists for.

a_refused_vms_page_is_not_mostly_nothing() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vms-refusal-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "a refused /vms says why and is not a headline on a black page" \
	a_refused_vms_page_is_not_mostly_nothing
