package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE ARM THE DOOR-ONLY SETTER STANDS ON, kept true by walking the source
// rather than by somebody remembering to update a list.
//
// openspecStateReach says what each route may do to a change's lifecycle
// state. The invariant it protects is the one the operator asked for: the
// transition door is the only setter. Every other write preserves the held
// row's state at the store funnel, and a route that would break that - a new
// door that rewrites fields, a second lifecycle vocabulary wired somewhere -
// fails HERE by existing, before it can be quietly wrong.
//
// It walks serve.go the way serve_routes_test.go does, with the same two
// exclusions by name, and it walks BOTH directions: a registered route with
// no declared reach is a door nobody answered for, and a declared route that
// is no longer registered is a contract about nothing.

// registeredRoutes reads serve.go for every pattern the guarded mux holds,
// minus the two non-doors serve_routes_test.go already excludes by name.
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	registered := regexp.MustCompile(`api\.HandleFunc\("([^"]+)"`)
	var routes []string
	for _, m := range registered.FindAllStringSubmatch(string(src), -1) {
		pattern := m[1]
		if pattern == "/api/" || pattern == "GET /api/ready" {
			continue
		}
		routes = append(routes, pattern)
	}
	return routes
}

func TestEveryRegisteredRouteDeclaresItsReach(t *testing.T) {
	var missing []string
	for _, pattern := range registeredRoutes(t) {
		if _, ok := openspecStateReach[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d route(s) have no declared reach to a change's lifecycle state. "+
			"Say which they are - preserves, transitions, replicates, tombstones, or na - "+
			"rather than let a new door write a state nobody answered for:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func TestOpenspecStateReachDeclaresOnlyRegisteredRoutes(t *testing.T) {
	registered := map[string]bool{}
	for _, pattern := range registeredRoutes(t) {
		registered[pattern] = true
	}
	var stale []string
	for pattern := range openspecStateReach {
		if !registered[pattern] {
			stale = append(stale, pattern)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%d declared reach(es) name routes that are no longer registered - "+
			"a contract about nothing is a lie with a coat on:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

func TestOpenspecStateReachUsesKnownWords(t *testing.T) {
	known := map[string]bool{
		"transitions": true, "preserves": true, "replicates": true,
		"tombstones": true, "na": true,
	}
	var bad []string
	for pattern, word := range openspecStateReach {
		if !known[word] {
			bad = append(bad, pattern+" = "+word)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d declared reach(es) use a word the vocabulary does not have:\n  %s",
			len(bad), strings.Join(bad, "\n  "))
	}
}
