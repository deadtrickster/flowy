package main

import "testing"

// The node's answer to "which console am I serving" is the hashed bundle name
// out of its own index.html, and a page open across a deploy reloads itself
// when that stops matching what it is running. So a wrong answer here is not a
// cosmetic bug: an empty one leaves every stale tab stale, and a WRONG one
// reloads every healthy tab in a loop.
//
// The cases that matter are the ones where something else in the file also
// looks like an asset - a stylesheet, a preload, a favicon - because the naive
// version of this returns the first /assets/ it sees and the css usually comes
// first.
func TestTheBundleIsTheHashedScriptAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		name  string
		index string
		want  string
	}{{
		name:  "what vite actually emits, css before js",
		index: `<link rel="stylesheet" href="/assets/index-ABC123.css"><script type="module" crossorigin src="/assets/index-XYZ789.js"></script>`,
		want:  "index-XYZ789.js",
	}, {
		name:  "single quotes are quotes too",
		index: `<script type='module' src='/assets/main-DEF456.js'></script>`,
		want:  "main-DEF456.js",
	}, {
		name:  "a preload of a chunk still resolves to a script",
		index: `<link rel="modulepreload" href="/assets/vendor-111.js"><script src="/assets/index-222.js"></script>`,
		want:  "vendor-111.js",
	}, {
		name:  "an index with no bundle claims none",
		index: `<div id="root"></div>`,
		want:  "",
	}, {
		name: "and neither does one with only styles - a node that cannot name " +
			"its console must not name something else instead",
		index: `<link rel="stylesheet" href="/assets/index-ABC123.css">`,
		want:  "",
	}} {
		t.Run(c.name, func(t *testing.T) {
			if got := bundleOf([]byte(c.index)); got != c.want {
				t.Fatalf("bundleOf(%q) = %q, want %q", c.index, got, c.want)
			}
		})
	}
}
