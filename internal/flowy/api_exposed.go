package flowy

// WHO THIS NODE WOULD TAKE A ROW FOR FROM ANYBODY AT ALL, over HTTP.
//
// `flowy principal exposed` has answered this since the finding that named it,
// and it answers it AGAINST A DSN. So the one question the security row
// 01M0AG9HVGCXGGMK70Y9JHCVPN turns on - which principals have no key, and
// therefore how much is left of the exposure - can be asked only by somebody
// sitting on the box with the database password. Every seat that could do the
// work about it, and the console the operator reads, cannot see its size.
//
// A finding whose closing criterion is unmeasurable from where the fleet works
// is a finding that stays open by default. This is the same door onto the same
// store function, so the two answers cannot drift.
//
// OPERATOR ONLY, and not because the contents are secret - UnkeyedPrincipals'
// own comment says an id, a handle and a count leak nothing a roster does not
// already say. It is that this is a list of the names a pinned peer could
// author under, which is a map of where to push if you ever get pinned, and the
// operator is the principal whose question it is.

import "net/http"

// handleExposedPrincipals answers every principal this node has rows from and
// holds no key for, most rows first, each with the command that closes it.
//
// GET /api/principals/exposed
//
// It answers 200 with an empty list rather than refusing when nothing is
// exposed, for the reason the CLI does: nothing exposed is a real answer and a
// reader must be able to tell it from a door that could not look. An empty
// `principals` with `exposed: 0` is that answer; a failure is a 500.
func (s *server) handleExposedPrincipals(w http.ResponseWriter, r *http.Request) {
	open, err := s.db.UnkeyedPrincipals(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, exposedPayload(s.db.Node(), open))
}
