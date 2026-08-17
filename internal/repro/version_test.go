package repro

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyVersion is the pure part explicitly called out for a unit
// test: what a version string names, decided from its shape alone, no git or
// docker touched.
func TestClassifyVersion(t *testing.T) {
	cases := []struct {
		in   string
		want VersionKind
	}{
		{"26.07.5", KindRelease},
		{"1.0.0", KindRelease},
		{"latest", KindLatest},
		{"LATEST", KindLatest},
		{"main", KindLatest},
		{"Main", KindLatest},
		{"head", KindLatest},
		{"", KindLatest}, // Resolve defaults "" to "latest" before classifying; ClassifyVersion("") itself falls to KindRef
		{"bc07c51", KindSHA},
		{"bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032", KindSHA},
		{"BC07C51", KindSHA},
		{"my-feature-branch", KindRef},
		{"26.07", KindRef},       // not X.Y.Z
		{"26.07.5-rc1", KindRef}, // not a bare release
		{"g1t", KindRef},
	}
	for _, tc := range cases {
		if tc.in == "" {
			continue // covered by TestResolveDefaultsEmptyToLatest instead
		}
		if got := ClassifyVersion(tc.in); got != tc.want {
			t.Errorf("ClassifyVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// scriptedRunner drives Resolve's git/docker calls off a queue keyed by the
// joined argv, so a test can hand back canned output without a checkout, a
// daemon or a network - the same reason forge.GhClient's run field is a
// swappable func.
type scriptedRunner struct {
	t       *testing.T
	answers map[string][]string // argv-key -> FIFO of answers
	calls   []string
}

func newScriptedRunner(t *testing.T) *scriptedRunner {
	return &scriptedRunner{t: t, answers: map[string][]string{}}
}

func (s *scriptedRunner) on(answer string, name string, args ...string) {
	key := argvKey(name, args)
	s.answers[key] = append(s.answers[key], answer)
}

func (s *scriptedRunner) run(_ context.Context, name string, args ...string) string {
	key := argvKey(name, args)
	s.calls = append(s.calls, key)
	q := s.answers[key]
	if len(q) == 0 {
		return ""
	}
	s.answers[key] = q[1:]
	return q[0]
}

func argvKey(name string, args []string) string {
	return name + " " + strings.Join(args, "\x1f")
}

// TestResolveLatestAlwaysRefetches is the property called out explicitly:
// latest/main re-resolves the default branch's live tip on EVERY call - it
// is not cached. Two resolves of "latest" against a fake git that answers
// differently each time must come back with two different commits.
func TestResolveLatestAlwaysRefetches(t *testing.T) {
	sr := newScriptedRunner(t)
	cfg := ProjectConfig{Source: "/src", BaseImage: "proj/base:runtime"}
	sr.on("", "git", "-C", cfg.Source, "fetch", "origin", "main", "--quiet")
	sr.on("", "git", "-C", cfg.Source, "fetch", "origin", "main", "--quiet")
	sr.on("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "git", "-C", cfg.Source, "rev-parse", "origin/main")
	sr.on("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "git", "-C", cfg.Source, "rev-parse", "origin/main")
	sr.on("", "git", "-C", cfg.Source, "rev-parse", "HEAD")
	sr.on("", "git", "-C", cfg.Source, "rev-parse", "HEAD")

	r := &Resolver{run: sr.run}
	v1 := r.Resolve(context.Background(), cfg, "latest")
	v2 := r.Resolve(context.Background(), cfg, "latest")

	if v1.SHA == v2.SHA {
		t.Fatalf("latest resolved to the same sha twice (%s) - it must re-fetch every call", v1.SHA)
	}
	if v1.SHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || v2.SHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("got shas %s, %s", v1.SHA, v2.SHA)
	}
	if !v1.SourceBuild || !v1.Buildable {
		t.Errorf("latest resolves to a source build: %+v", v1)
	}
}

// TestResolveReleaseReadsCommitOffOCILabel: an X.Y.Z release is pulled by
// registry digest, and its commit is read off the OCI revision label - not
// off git, since the release may have been built by CI that never pushed
// that commit anywhere this checkout can see.
func TestResolveReleaseReadsCommitOffOCILabel(t *testing.T) {
	cfg := ProjectConfig{Registry: "proj/proj", CacheDir: t.TempDir(), BinaryPath: "/usr/bin/thebin"}
	tag := "proj/proj:26.07.5"
	pinned := "proj/proj@sha256:deadbeefcafe0000000000000000000000000000000000000000000000beef"

	sr := newScriptedRunner(t)
	sr.on("", "docker", "pull", tag)
	sr.on("proj/proj@sha256:deadbeefcafe0000000000000000000000000000000000000000000000beef",
		"docker", "image", "inspect", "-f", "{{index .RepoDigests 0}}", tag)
	sr.on("bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032", "docker", "image", "inspect", "-f",
		`{{index .Config.Labels "org.opencontainers.image.revision"}}`, pinned)
	sr.on("2026-08-11T00:00:00Z", "docker", "image", "inspect", "-f",
		`{{index .Config.Labels "org.opencontainers.image.created"}}`, pinned)
	sr.on("cid123", "docker", "create", pinned)
	sr.on("", "docker", "cp", "cid123:/usr/bin/thebin", filepath.Join(cfg.CacheDir, "thebin-deadbeefcafe"))
	sr.on("", "docker", "rm", "cid123")

	r := &Resolver{run: sr.run}
	v := r.Resolve(context.Background(), cfg, "26.07.5")

	if v.SHA != "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032" {
		t.Errorf("sha = %q, want the OCI revision label's value", v.SHA)
	}
	if v.Image != pinned {
		t.Errorf("image = %q, want the digest-pinned ref %q", v.Image, pinned)
	}
	if v.SourceBuild {
		t.Error("a release image is never a source build")
	}
	if v.Buildable {
		t.Error("a release image is never independently buildable")
	}
	if !strings.Contains(v.Note, "26.07.5") || !strings.Contains(v.Note, "bc07c51d4b8d") {
		t.Errorf("note = %q, missing the version or the commit", v.Note)
	}
}

// TestResolveReleaseNoRegistryConfigured is the per-project-config guard:
// a project with no Registry cannot resolve a release tag, and says so
// instead of building a malformed "<empty>:X.Y.Z" ref.
func TestResolveReleaseNoRegistryConfigured(t *testing.T) {
	r := &Resolver{run: func(context.Context, string, ...string) string {
		t.Fatal("no registry configured - should never shell out")
		return ""
	}}
	v := r.Resolve(context.Background(), ProjectConfig{}, "1.2.3")
	if v.Buildable || v.SHA != "1.2.3" || !strings.Contains(v.Note, "no configured registry") {
		t.Errorf("got %+v", v)
	}
}

// TestResolveSHAUsedAsIs: a bare sha is resolved via rev-parse (to catch a
// short/ambiguous prefix) but falls back to the literal string if git has
// nothing to say - never silently dropped.
func TestResolveSHAUsedAsIs(t *testing.T) {
	cfg := ProjectConfig{Source: "/src", BaseImage: "proj/base:runtime", BuildCacheDir: t.TempDir()}
	sr := newScriptedRunner(t)
	sr.on("deadbeefcafe0000000000000000000000000000", "git", "-C", cfg.Source, "rev-parse", "deadbeef")
	sr.on("", "git", "-C", cfg.Source, "rev-parse", "HEAD")

	r := &Resolver{run: sr.run}
	v := r.Resolve(context.Background(), cfg, "deadbeef")
	if v.SHA != "deadbeefcafe0000000000000000000000000000" {
		t.Errorf("sha = %q, want the rev-parsed full sha", v.SHA)
	}
	if !v.Buildable || !v.SourceBuild {
		t.Errorf("a resolved commit is always buildable/source-build: %+v", v)
	}
}

// TestResolveUnresolvableRefReportsWhy: a ref git cannot find comes back
// Buildable:false with a Note naming what was asked, not a Go error - a
// caller (a UI, a run) has one place to look for what went wrong.
func TestResolveUnresolvableRefReportsWhy(t *testing.T) {
	cfg := ProjectConfig{Source: "/src", BaseImage: "proj/base:runtime"}
	sr := newScriptedRunner(t) // every git call answers "" (no such ref)
	r := &Resolver{run: sr.run}
	v := r.Resolve(context.Background(), cfg, "no-such-branch")
	if v.Buildable {
		t.Error("an unresolvable ref must not claim to be buildable")
	}
	if !strings.Contains(v.Note, "no-such-branch") {
		t.Errorf("note = %q, should name the ref that could not be resolved", v.Note)
	}
}

// TestResolveNoSourceConfigured covers the genericisation the task calls
// out: a project with no Source checkout can still resolve nothing
// git-based, and says why instead of running git against an empty path.
func TestResolveNoSourceConfigured(t *testing.T) {
	r := &Resolver{run: func(context.Context, string, ...string) string {
		t.Fatal("no source configured - should never shell out to git")
		return ""
	}}
	for _, version := range []string{"latest", "main", "deadbeef", "some-branch"} {
		v := r.Resolve(context.Background(), ProjectConfig{}, version)
		if v.Buildable {
			t.Errorf("Resolve(%q) with no Source claims Buildable", version)
		}
		if !strings.Contains(v.Note, "no source checkout") {
			t.Errorf("Resolve(%q) note = %q, want it to say there is no source checkout", version, v.Note)
		}
	}
}

// TestFromCommitPrebuiltReusedOnlyWhenCurrent: the local prebuilt is reused
// only when it is actually AT the resolved commit - a stale prebuilt from a
// previous checkout state must not be handed back as if it were fresh.
func TestFromCommitPrebuiltReusedOnlyWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	prebuilt := filepath.Join(dir, "thebin")
	if err := os.WriteFile(prebuilt, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ProjectConfig{Source: "/src", BaseImage: "img", PrebuiltBinary: prebuilt}

	sr := newScriptedRunner(t)
	sr.on("current-commit", "git", "-C", cfg.Source, "rev-parse", "HEAD")
	sr.on("current-commit", "git", "-C", cfg.Source, "rev-parse", "current-commit")

	r := &Resolver{run: sr.run}
	v := r.Resolve(context.Background(), cfg, "current-commit")
	if v.Binary != prebuilt {
		t.Errorf("Binary = %q, want the prebuilt reused since it is at the current commit", v.Binary)
	}
	if !strings.Contains(v.Note, "prebuilt, current") {
		t.Errorf("note = %q", v.Note)
	}

	// A different commit: the same prebuilt is NOT at that commit, so it
	// must not be reused.
	sr2 := newScriptedRunner(t)
	sr2.on("stale-head", "git", "-C", cfg.Source, "rev-parse", "HEAD")
	sr2.on("other-commit", "git", "-C", cfg.Source, "rev-parse", "other-commit")
	r2 := &Resolver{run: sr2.run}
	v2 := r2.Resolve(context.Background(), cfg, "other-commit")
	if v2.Binary != "" {
		t.Errorf("Binary = %q, want empty - the prebuilt is not at this commit", v2.Binary)
	}
	if !strings.Contains(v2.Note, "builds from source") {
		t.Errorf("note = %q, want it to say a build is needed", v2.Note)
	}
}

// TestFromCommitBuildCacheHit: a sha-keyed local build cache is reused
// without needing the prebuilt to match.
func TestFromCommitBuildCacheHit(t *testing.T) {
	buildCache := t.TempDir()
	cfg := ProjectConfig{Source: "/src", BaseImage: "img", BuildCacheDir: buildCache, BinaryPath: "/usr/bin/thebin"}
	commit := "cafefeed00000000000000000000000000000000"
	cached := filepath.Join(buildCache, "thebin-"+commit)
	if err := os.WriteFile(cached, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	sr := newScriptedRunner(t)
	sr.on(commit, "git", "-C", cfg.Source, "rev-parse", commit)

	r := &Resolver{run: sr.run}
	v := r.Resolve(context.Background(), cfg, commit)
	if v.Binary != cached {
		t.Errorf("Binary = %q, want the build-cache hit %q", v.Binary, cached)
	}
	if !strings.Contains(v.Note, "built, cached") {
		t.Errorf("note = %q", v.Note)
	}
}
