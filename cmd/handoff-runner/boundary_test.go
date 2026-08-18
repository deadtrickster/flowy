package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reproPackage is the package this binary is built around and the Flowy node
// must not be.
const reproPackage = "github.com/deadtrickster/flowy/internal/repro"

// storePackage is a package the node certainly does import - the witness
// that the scan below actually read anything. Without it, a scan that found
// no files, or a parser that failed on every one of them, would report the
// boundary intact by having looked at nothing.
const storePackage = "github.com/deadtrickster/flowy/internal/store"

// TestTheNodeDoesNotLinkTheRunner is the row's headline constraint, checked
// rather than described.
//
// This binary runs where the repro executes: it needs a live Docker daemon,
// a source checkout and room to compile a database. The Flowy node is meant
// to run with none of those - the deployment the Python service shipped
// drops every capability, sets no-new-privileges, mounts its root read-only
// and is handed no Docker socket. One migration, two deployables.
//
// The way that stops being true is not a decision anybody announces: it is
// one import added to the node's package because a handler wanted to resolve
// a version, and then `flowy serve` needs Docker to build. So the check is
// on the import, which is the thing that would actually change.
func TestTheNodeDoesNotLinkTheRunner(t *testing.T) {
	root := ".."
	root = filepath.Join(root, "..")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read repository root: %v", err)
	}

	sawStore, files := false, 0
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(root, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files++
		for _, imp := range f.Imports {
			switch strings.Trim(imp.Path.Value, `"`) {
			case reproPackage:
				t.Errorf("%s imports %s: the node and the runner are one process again, "+
					"and `flowy serve` now needs a Docker daemon to build", e.Name(), reproPackage)
			case storePackage:
				sawStore = true
			}
		}
	}
	if files == 0 || !sawStore {
		t.Fatalf("the scan read %d files and did not find %s among their imports, "+
			"so it proved nothing", files, storePackage)
	}
}

// TestTheRunnerDoesNotLinkTheNode is the same boundary from the other side.
// This package is `main` in cmd/handoff-runner and the node is `main` in the
// repository root, so Go already makes the import impossible - what this
// checks is that nothing here reaches into the node's files by copying a
// path, and that the four helpers this binary does duplicate (bearerToken,
// writeJSON, logRequests, errorRef) stay small enough to be duplicates
// rather than a reason to merge the two.
func TestTheRunnerReadsNoConfigurationOutsideItsOwn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// The environment variables this binary reads, and nothing else: a
	// variable read here that the node also reads is a shared configuration
	// surface between two deployments that are meant to be configured apart.
	allowed := map[string]bool{
		"HANDOFF_RUNNER_CONFIG": true, "HANDOFF_RUNNER_ADDR": true,
		"HANDOFF_RUNNER_CACHE": true, "HANDOFF_RUNNER_WORKERS": true,
		"HANDOFF_RUNNER_BUILD_SCRIPT": true,
		// Which browser origins may read this runner's answers - see cors.go.
		// It names the CONSOLE's origin, which is the node's, and is still
		// this binary's own configuration: the node has no idea it is being
		// fetched from and could not set this for it.
		"HANDOFF_RUNNER_CONSOLE_ORIGINS": true,
		// The two it shares on purpose: it is the SAME database and the same
		// node identity, which is the whole reason a verdict written here
		// lands on the finding's own log.
		"DATABASE_URL": true, "FLOWY_NODE": true,
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range envNames(string(body)) {
			read++
			if !allowed[name] {
				t.Errorf("%s reads $%s, which is not this binary's to read", e.Name(), name)
			}
		}
	}
	if read == 0 {
		t.Fatal("no environment read was found at all, so this check proved nothing")
	}
}

// envNames pulls the argument out of every env("X") / os.Getenv("X") call in
// a source file. It is deliberately textual: what it is guarding against is
// somebody adding a variable, and a variable added is a literal in the
// source whichever way it is reached.
func envNames(src string) []string {
	var out []string
	for _, call := range []string{"env(\"", "os.Getenv(\"", "Getenv(\""} {
		rest := src
		for {
			i := strings.Index(rest, call)
			if i < 0 {
				break
			}
			rest = rest[i+len(call):]
			j := strings.Index(rest, "\"")
			if j < 0 {
				break
			}
			out = append(out, rest[:j])
			rest = rest[j:]
		}
	}
	return out
}
