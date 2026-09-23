# shellcheck shell=bash
#
# AN ATTACHMENT ROW'S OWN PAGE SHOWS THE FILE, AND THE FILE CAN BE TAKEN AWAY.
#
# The operator, on five attachments posted to a room within one hour: "the
# attachment rows they did do not render images nor do they have a download
# button". Two causes. ArtifactView built its file list from what a row CARRIES,
# and an attachment row carries nothing - it IS the file - so the list was empty
# and the card was never drawn. And there was no download control anywhere in
# the console for any attachment: the door answers JSON with base64, so there
# was no URL to link to either, and anything that was not an image could not be
# reached at all.
#
# THE FIXTURE CARRIES NO MESSAGE, which is the whole reason it catches this.
# `flowy attach` without --message makes exactly this bare row, and --message is
# the flag three seats could not use. A check that posted a message first would
# exercise the transcript card, which already worked.
#
# THE ASSERTIONS ARE THE BYTES AND THE NAME - sha256 over the saved file, and
# the file's own filename rather than its ULID or the sentence in its title.
# A control that saves a truncated or wrongly-named copy passes anything that
# only looks for the element. Negative-controlled both ways: drop the row's own
# id from the list and flow 1 reports a page with no card; drop the control and
# flow 2 reports a reader with no way to reach the bytes.
#
# Its own room, because it writes an attachment and a counting fixture does not
# share a room.

an_attachment_row_shows_and_yields_its_file() {
	cd "$ROOT/web" || return 1
	node scripts/attachment-download-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" attachdownload
}

check "an attachment row draws its own file and hands the bytes over under their own name" \
	an_attachment_row_shows_and_yields_its_file
