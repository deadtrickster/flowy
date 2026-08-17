// Package repro builds and versions the self-contained reproduction packages
// a finding hands off: what git commit and image a version string like
// "latest", a branch, a bare sha or a release tag actually names (this file),
// and the staged tree that runs it (packager.go).
//
// Ported from hands-off's tools/handoff-service/{runner,packager}.py, which
// hardcoded one project (SereneDB: its checkout path, its base image, its
// registry, its binary name) as package-level constants. Every one of those
// is ProjectConfig here instead - a node runs findings from more than one
// project, and a resolver that only knew how to resolve SereneDB versions
// would silently mis-resolve, or crash on, anything else.
package repro

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectConfig is what a project supplies so its findings can be versioned
// and packaged: nothing here is a repro-tree property (that lives in the
// finding's own ReproManifest), it is all "which checkout, which image,
// which cache" - nailed down once per project rather than repeated, and
// wrong, on every call.
type ProjectConfig struct {
	// Source is the local clone version resolution reads commits from and a
	// source build (row 09) builds from. Empty disables every git-based
	// resolution path (latest/main/branch/sha) - a project with no local
	// checkout can still resolve release tags, nothing else.
	Source string
	// DefaultBranch is what "latest"/"main"/"head" fetches the tip of.
	// Defaults to "main" when empty.
	DefaultBranch string
	// BaseImage is the pulled runtime a source-built or mounted binary runs
	// on top of - the package's FROM for a source-build package, and the
	// image a release's own binary is extracted out of at CacheDir.
	BaseImage string
	// Registry is the "org/repo" a release tag like "1.2.3" is pulled from -
	// e.g. "serenedb/serenedb". Empty disables release-tag resolution.
	Registry string
	// BinaryPath is where the project's binary lives INSIDE its image, e.g.
	// "/usr/bin/serened" - what gets `docker cp`'d out of a release image
	// into CacheDir, and what a dind package's entrypoint extracts from the
	// SUT tag at run time when no binary is baked in.
	BinaryPath string
	// CacheDir holds release binaries extracted via BinaryPath, keyed by
	// image digest.
	CacheDir string
	// BuildCacheDir holds sha-keyed source builds. This package never builds
	// one (that is row 09's build queue) - it only checks whether one is
	// already there.
	BuildCacheDir string
	// PrebuiltBinary is a local build output reused as-is when it happens to
	// already be at the commit being resolved - e.g. a dev's own
	// build_perf/bin/<binary>, kept warm between resolves. Empty means
	// nothing is ever reused this way; every source commit either hits
	// BuildCacheDir or needs a build.
	PrebuiltBinary string
	// DefaultIsolation is what a finding runs under when its own manifest
	// does not say: "dind" (spins its own containers, so it runs wrapped in
	// a privileged docker:dind service) or "plain" (the repro command runs
	// directly in the image). Empty means "plain".
	DefaultIsolation string
	// DindPackages are extra Alpine packages a dind package's Dockerfile
	// installs beyond the harness's own baseline (bash, python3, py3-pip,
	// coreutils, procps, grep) - whatever client the project's own wire
	// protocol needs, e.g. []string{"postgresql-client"} for a Postgres-wire
	// server. Hardcoding this was exactly the SereneDB-specific leak
	// packager.py had (postgresql-client baked into every project's image),
	// so it is per-project here too.
	DindPackages []string
	// PythonClientPackages are extra Alpine packages added only when a
	// repro's interpreter is python3 - e.g. []string{"py3-psycopg",
	// "py3-psycopg2"} for a project whose Python repros need a DB client.
	// Installed via apk (not pip): see packager.go's dockerfile comment on
	// why.
	PythonClientPackages []string
}

// branch is DefaultBranch, defaulted.
func (c ProjectConfig) branch() string {
	if c.DefaultBranch == "" {
		return "main"
	}
	return c.DefaultBranch
}

// isolation is DefaultIsolation, defaulted.
func (c ProjectConfig) isolation() string {
	if c.DefaultIsolation == "" {
		return "plain"
	}
	return c.DefaultIsolation
}

// Version is what a version string resolves to: the commit it names and how
// to run it. It is never an error on its own - an unresolvable ref comes
// back with Buildable false and Note saying why, the same way runner.py's
// resolve_version always returned a dict rather than raising, because the
// caller (a run, a package) needs to report the failure to a human rather
// than crash on it.
type Version struct {
	// SHA is the git commit this version names. For a release tag it comes
	// off the image's org.opencontainers.image.revision label, not off git -
	// the image IS the commit's identity once published.
	SHA string
	// Image is what a package's Dockerfile/compose FROM or pulls: a pinned
	// release digest, or the project's BaseImage for a source build.
	Image string
	// Binary is a local path to the already-built binary for this commit -
	// prebuilt, cached, or extracted from a release - or "" when there is
	// none yet.
	Binary string
	// Buildable is whether a build could ever produce Binary for this
	// version (true for anything git-resolvable; false when even the ref
	// itself could not be resolved, or a release image could not be pulled).
	Buildable bool
	// SourceBuild is whether this version's binary is (or would be) built
	// from Source, rather than shipped in a published release image. It
	// decides how a package bakes/mounts the binary - see packager.go.
	SourceBuild bool
	// Note is a one-line human explanation of what was resolved and how -
	// "latest @ bc07c51d4b8d (built, cached)", "release 26.07.5 @ commit
	// bc07c51d4b8d", "could not resolve git ref foo". It is the only place
	// a failure is visible; there is no separate error.
	Note string
}

// VersionKind is what a version string names, decided from its shape alone -
// no git or docker touched yet. Split out from Resolve so the classification
// itself - the part with no daemon or checkout to fake - is directly
// unit-testable.
type VersionKind int

const (
	// KindRelease is an X.Y.Z published release tag.
	KindRelease VersionKind = iota
	// KindLatest is latest/main/head - the default branch's live tip,
	// re-resolved on every call rather than cached, because the whole point
	// of "latest" is that it moves.
	KindLatest
	// KindSHA is a bare commit sha, 7 to 40 hex characters.
	KindSHA
	// KindRef is anything else: a branch or other ref name, resolved by
	// fetching it and reading its tip.
	KindRef
)

var (
	releaseTagRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	commitSHARE  = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// ClassifyVersion says what kind of ref a version string is, before any git
// or docker command runs. Resolve uses it to pick a path; it is exported
// because "does this string look like a release/sha/ref" is useful on its
// own (a console validating input, say) without pulling in a Resolver.
func ClassifyVersion(version string) VersionKind {
	v := strings.TrimSpace(version)
	switch {
	case releaseTagRE.MatchString(v):
		return KindRelease
	case isLatestWord(v):
		return KindLatest
	case commitSHARE.MatchString(v):
		return KindSHA
	default:
		return KindRef
	}
}

func isLatestWord(v string) bool {
	switch strings.ToLower(v) {
	case "latest", "main", "head":
		return true
	default:
		return false
	}
}

// runFunc runs one command and returns what it wrote to stdout, trimmed -
// nothing more. Errors are swallowed here on purpose, the same way
// runner.py's `_sh` only ever looked at `.stdout.strip()`: a `git rev-parse`
// of a ref that does not exist and a `docker pull` that fails both just
// produce empty/absent output, and every caller below already treats "got
// nothing" as the failure signal, so a second thing to check would only be
// two ways to express the one condition that matters.
//
// It is a field (see forge.GhClient.run in internal/forge/gh.go for the same
// pattern) so tests can drive git/docker's answers without a checkout, a
// daemon or a network.
type runFunc func(ctx context.Context, name string, args ...string) string

// runCommand is the real runFunc.
func runCommand(ctx context.Context, name string, args ...string) string {
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(out))
}

// Resolver resolves version strings to commits, shelling to git and docker
// through its run field.
type Resolver struct {
	run runFunc
}

// NewResolver returns a Resolver that shells to the real git and docker on
// PATH.
func NewResolver() *Resolver { return &Resolver{run: runCommand} }

// shortSHA is a commit's first 12 characters - what every Note below prints
// a sha as - safe on a string shorter than that (a caller-supplied ref that
// never turned out to be a real commit).
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Resolve turns a version string - "latest", a branch, a bare sha, or an
// X.Y.Z release - into the commit and image it names.
//
//   - latest/main/head ALWAYS re-fetches and re-reads the default branch's
//     live tip - it is not cached, because a cached "latest" is a lie by
//     the next commit.
//   - an X.Y.Z release is pulled by tag, pinned to its registry digest, and
//     its commit is read off the org.opencontainers.image.revision label -
//     that commit, not the tag, is what the finding actually reproduced
//     against.
//   - a bare sha or another ref is resolved as-is (a ref is fetched first,
//     to pick up a tip pushed since the last resolve).
func (r *Resolver) Resolve(ctx context.Context, cfg ProjectConfig, version string) Version {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "latest"
	}
	switch ClassifyVersion(version) {
	case KindRelease:
		return r.resolveRelease(ctx, cfg, version)
	case KindLatest:
		return r.resolveLatest(ctx, cfg, version)
	case KindSHA:
		return r.resolveSHA(ctx, cfg, version)
	default:
		return r.resolveRef(ctx, cfg, version)
	}
}

func (r *Resolver) resolveLatest(ctx context.Context, cfg ProjectConfig, version string) Version {
	if cfg.Source == "" {
		return Version{SHA: version, Image: cfg.BaseImage, SourceBuild: true,
			Note: "project has no source checkout configured; cannot resolve " + version}
	}
	branch := cfg.branch()
	r.run(ctx, "git", "-C", cfg.Source, "fetch", "origin", branch, "--quiet")
	commit := r.run(ctx, "git", "-C", cfg.Source, "rev-parse", "origin/"+branch)
	if commit == "" {
		return Version{SHA: version, Image: cfg.BaseImage, SourceBuild: true,
			Note: "could not resolve origin/" + branch}
	}
	label := "latest"
	if strings.ToLower(version) != "latest" {
		label = branch
	}
	return r.fromCommit(ctx, cfg, commit, label)
}

func (r *Resolver) resolveSHA(ctx context.Context, cfg ProjectConfig, version string) Version {
	if cfg.Source == "" {
		return Version{SHA: version, Image: cfg.BaseImage, SourceBuild: true,
			Note: "project has no source checkout configured; cannot resolve " + version}
	}
	commit := r.run(ctx, "git", "-C", cfg.Source, "rev-parse", version)
	if commit == "" {
		commit = version
	}
	return r.fromCommit(ctx, cfg, commit, shortSHA(commit))
}

func (r *Resolver) resolveRef(ctx context.Context, cfg ProjectConfig, version string) Version {
	if cfg.Source == "" {
		return Version{SHA: version, Image: cfg.BaseImage, SourceBuild: true,
			Note: "project has no source checkout configured; cannot resolve " + version}
	}
	r.run(ctx, "git", "-C", cfg.Source, "fetch", "origin", version, "--quiet")
	commit := r.run(ctx, "git", "-C", cfg.Source, "rev-parse", "origin/"+version)
	if commit == "" {
		commit = r.run(ctx, "git", "-C", cfg.Source, "rev-parse", version)
	}
	if commit == "" {
		return Version{SHA: version, Image: cfg.BaseImage, SourceBuild: true,
			Note: "could not resolve git ref " + version}
	}
	return r.fromCommit(ctx, cfg, commit, version)
}

// fromCommit resolves a git commit to the binary built from it: the local
// prebuilt if it happens to already be at that commit, else the sha-keyed
// build cache, else "needs a build" - never silently running the wrong
// binary. A package bakes whatever comes back onto BaseImage (see
// packager.go), so SourceBuild is always true here.
func (r *Resolver) fromCommit(ctx context.Context, cfg ProjectConfig, commit, label string) Version {
	short := shortSHA(commit)
	if cfg.PrebuiltBinary != "" && fileExists(cfg.PrebuiltBinary) && cfg.Source != "" {
		built := r.run(ctx, "git", "-C", cfg.Source, "rev-parse", "HEAD")
		if built == commit {
			return Version{SHA: commit, Image: cfg.BaseImage, Binary: cfg.PrebuiltBinary,
				Buildable: true, SourceBuild: true,
				Note: fmt.Sprintf("%s @ %s (prebuilt, current)", label, short)}
		}
	}
	if cfg.BuildCacheDir != "" {
		cached := filepath.Join(cfg.BuildCacheDir, cacheFileName(cfg, commit))
		if fileExists(cached) {
			return Version{SHA: commit, Image: cfg.BaseImage, Binary: cached,
				Buildable: true, SourceBuild: true,
				Note: fmt.Sprintf("%s @ %s (built, cached)", label, short)}
		}
	}
	return Version{SHA: commit, Image: cfg.BaseImage, Buildable: true, SourceBuild: true,
		Note: fmt.Sprintf("%s @ %s - builds from source (cached after first)", label, short)}
}

// resolveRelease resolves a published release: pull it, pin it to its
// registry digest (a tag can move; a digest cannot), and read the commit it
// was built from off its own OCI label - that commit is the version's
// identity, the tag is only how a human spelled it.
func (r *Resolver) resolveRelease(ctx context.Context, cfg ProjectConfig, version string) Version {
	if cfg.Registry == "" {
		return Version{SHA: version, Buildable: false, SourceBuild: false,
			Note: "project has no configured registry; cannot resolve release " + version}
	}
	tag := cfg.Registry + ":" + version
	r.run(ctx, "docker", "pull", tag)
	digest := repoDigest(r.run(ctx, "docker", "image", "inspect", "-f", "{{index .RepoDigests 0}}", tag))
	if digest == "" {
		return Version{SHA: version, Image: tag, Buildable: false, SourceBuild: false,
			Note: "could not pull " + tag}
	}
	pinned := cfg.Registry + "@sha256:" + digest
	rev := r.label(ctx, pinned, "org.opencontainers.image.revision")
	created := r.label(ctx, pinned, "org.opencontainers.image.created")
	if len(created) > 10 {
		created = created[:10]
	}
	sha := rev
	if sha == "" {
		sha = shortSHA(digest)
	}
	binary := ""
	if cfg.CacheDir != "" && cfg.BinaryPath != "" {
		cached := filepath.Join(cfg.CacheDir, cacheFileName(cfg, shortSHA(digest)))
		if !fileExists(cached) {
			cid := r.run(ctx, "docker", "create", pinned)
			if cid != "" {
				r.run(ctx, "docker", "cp", cid+":"+cfg.BinaryPath, cached)
				r.run(ctx, "docker", "rm", cid)
			}
		}
		if fileExists(cached) {
			binary = cached
		}
	}
	note := fmt.Sprintf("release %s @ commit %s", version, shortSHA(sha))
	if created != "" {
		note += fmt.Sprintf(" (built %s)", created)
	}
	return Version{SHA: sha, Image: pinned, Binary: binary, Buildable: false, SourceBuild: false, Note: note}
}

// label reads one OCI label off an image, "" if it is absent or docker has
// no answer (unpulled image, no such label, docker not present).
func (r *Resolver) label(ctx context.Context, ref, key string) string {
	v := r.run(ctx, "docker", "image", "inspect", "-f",
		fmt.Sprintf(`{{index .Config.Labels "%s"}}`, key), ref)
	if v == "<no value>" {
		return ""
	}
	return v
}

var repoDigestRE = regexp.MustCompile(`@sha256:([0-9a-f]+)`)

// repoDigest pulls the sha256 hex out of a `docker image inspect
// RepoDigests` answer like "serenedb/serenedb@sha256:abcd...".
func repoDigest(s string) string {
	m := repoDigestRE.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// cacheFileName is the cache-file name for one binary at one commit/digest:
// the binary's own name (BinaryPath's base, e.g. "serened"), so two projects
// sharing a CacheDir - or one project's binary being renamed across
// versions - never collide.
func cacheFileName(cfg ProjectConfig, key string) string {
	name := filepath.Base(cfg.BinaryPath)
	if name == "" || name == "." || name == "/" {
		name = "bin"
	}
	return name + "-" + key
}

// fileExists is a plain existence check - not "is readable", not "is a
// binary that runs": the same shallow test runner.py's os.path.exists made
// throughout, which is all a cache lookup needs.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
