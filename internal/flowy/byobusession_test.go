package flowy

import "testing"

// TestTheSessionNameIsTheOperatorsOwn pins the naming against the rule in their
// init.el, because the whole value of this is landing in the SAME session their
// editor and an ssh attach use. A name we merely think is reasonable is a
// second session that looks right and nobody else can reach.
//
// THE DOT CASE IS THE ONE THAT MATTERS. tmux silently turns a dot in a session
// name into an underscore, so `-t flowy.dogfood` addresses flowy_dogfood - and
// a client that did not sanitise would either attach to something it did not
// name or create a duplicate. That is why their helper does it and why this
// copies it rather than approximating it.
func TestTheSessionNameIsTheOperatorsOwn(t *testing.T) {
	for _, c := range []struct{ project, want string }{
		{"flowy", "projectile/flowy"},
		{"flowy.dogfood", "projectile/flowy_dogfood"},
		{"a:b", "projectile/a_b"},
		{"two words", "projectile/two_words"},
		{"all.three: here", "projectile/all_three__here"},
		// Their rule replaces THREE characters and no others. A dash and an
		// underscore survive, and so does a slash - projectile/orioledb-ik is a
		// live session on this host, so a slug that ate the dash would miss it.
		{"orioledb-ik-primary", "projectile/orioledb-ik-primary"},
		{"already_under", "projectile/already_under"},
	} {
		if got := byobuSessionFor(c.project); got != c.want {
			t.Errorf("byobuSessionFor(%q) = %q, want %q", c.project, got, c.want)
		}
	}

	// EMPTY IS NOT A SESSION. A blank project would otherwise address
	// "projectile/", which is a name tmux would accept and nobody meant - and
	// every project with no name would share it.
	if got := byobuSessionFor("  "); got != "" {
		t.Errorf("a project with no name got session %q; it must get none", got)
	}
}
