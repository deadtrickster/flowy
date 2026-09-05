# shellcheck shell=bash
#
# AN OPERATOR CAN OPEN A ROW OUTSIDE THE PROJECT THEY ARE STANDING IN.
#
# The operator, 2026-09-05, on a link to claude-host's Oracle join request:
# "interesting what you have raised linked as .../p/_/_/<id> which is 404".
# Row 01M1S84RSYEFNZWVXE1S9JZ2SP.
#
# The row was real and had a page of its own. What failed was the CONSOLE's
# single-row fetch, which never sent ?scope=all although the door has honoured
# it for an operator all along - api.go:899 passes scopeAll(r, p), auth.go:263
# defines it as the query plus p.Operator - and the same api.ts already sends it
# for metrics and traces. Unable to read the row, the console then drew the link
# with "_" for the fields it could not see, which is what the operator saw.
#
# THE PREMISE IS ASSERTED RATHER THAN ASSUMED. The first two arms are API calls:
# the door must still refuse this row to the operator by default, and must
# answer it with scope=all. If the operator could already read it the browser
# arms would pass for the wrong reason and prove nothing.
#
# ITS OWN ROWS, not the suite's shared fixtures. $KEPT would have done today and
# is not safe to lean on: a later check inserts token_projects for B, and the
# suite grants USER_A the operator role further down, so both controls move
# depending on where this check sits in the file.

an_operator_can_open_a_row_outside_their_project() {
	recall
	local outside inside outside_project inside_project

	# A row in pc, which the operator does not act in.
	api POST "$TOKEN_A_PC" /api/artifacts '{
		"type": "note", "title": "outside the operator project",
		"discovery": "a row the operator must widen to read"
	}' || return 1
	want_eq "the pc row was created" "$API_STATUS" 200 || return 1
	outside="$(jqv .id)"
	outside_project="$(jqv .project)"

	# And one in whatever project the operator token itself acts in, read off
	# the answer rather than assumed - "you write where you are".
	api POST "$TOKEN_OP" /api/artifacts '{
		"type": "note", "title": "inside the operator project",
		"discovery": "a row that needs no widening at all"
	}' || return 1
	want_eq "the operator's own row was created" "$API_STATUS" 200 || return 1
	inside="$(jqv .id)"
	inside_project="$(jqv .project)"

	if [ "$outside_project" = "$inside_project" ]; then
		printf 'the operator acts in %s, the same project as the row meant to be outside it - this check proves nothing as written\n' \
			"$inside_project" >&2
		return 1
	fi

	# THE PREMISE: the door refuses by default and answers when asked. If the
	# first of these ever returns 200, the fix under test is not what makes the
	# page work and the browser arms below are measuring something else.
	want_status 404 GET "$TOKEN_OP" "/api/artifact/$outside" || return 1
	want_status 200 GET "$TOKEN_OP" "/api/artifact/$outside?scope=all" || return 1

	cd "$ROOT/web" || return 1
	node scripts/as-node-badge-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" \
		"$outside" "$outside_project" "$inside" "$inside_project"
}

check "an operator can open a row outside the project they are standing in, and the page says why" \
	an_operator_can_open_a_row_outside_their_project
