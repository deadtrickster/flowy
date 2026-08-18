package repro

// Ported from hands-off's tools/handoff-service/packager.py.
//
// A repro tree is a script that reads a SUT image/binary pair and spins its
// own containers via `docker run`. Wrapping it in a single privileged
// container running its OWN Docker daemon (dind) makes that portable: the
// script's `docker run` talks to that inner daemon, and every bind path
// resolves inside the container filesystem the script created it in - no
// host socket, no host paths, no cross-container path mismatch. The exact
// binary that reproduced is what a source build bakes in, so the package
// pins the precise bits regardless of whether the version was a release tag
// or a source build. `docker compose up --build --abort-on-container-exit`
// exiting 0 means it still reproduces.
//
// Two entry points, same rendering:
//
//	BuildPackage  -> a downloadable .tgz, text only, never a binary.
//	StageForRun   -> a persistent directory for a LOCAL run: additionally
//	                 carries the resolved binary and a .staged marker.
//
// emit_ci (packager.py's CI path, given an already-present tag with no
// docker/version-resolution touched at all) is deliberately not ported: the
// task list calls it out as explicitly deferred, and there is no CI runner
// on this side yet for it to feed.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"github.com/deadtrickster/flowy/internal/store"
)

// Finding is what the packager needs about a finding beyond its repro tree.
// Unlike hands-off's file-based findings ("serenedb-0007", a number pulled
// out of a directory name), a flowy finding's ID is an opaque ULID - there is
// no digit to parse a package name out of. So Num is not derived here: it is
// whatever short, filename-safe token the caller wants baked into the
// package name (a display sequence number, a short id, anything stable) -
// the packager only prints it.
type Finding struct {
	// ID is the finding's artifact id - what MANIFEST.json's "finding" names.
	ID string
	// Project is the project name, used in the package's own name
	// (repro-<Project>-<Num>-...) and its README/Dockerfile image name.
	Project string
	// Num is the caller-supplied naming token described above.
	Num string
	// Title is a human title; falls back to ID when empty.
	Title string
	// Report is the README's body - the finding's write-up. A caller with
	// nothing written yet should pass a placeholder rather than "", so the
	// package still explains what it is.
	Report string
	// Polarity is MANIFEST.json's "polarity" - defaults to "present" (the
	// finding IS present when the repro exits 0) when empty, matching
	// packager.py's frontmatter default.
	Polarity string
}

// RenderInput is everything one package render needs.
type RenderInput struct {
	Finding Finding
	// Requested is the version string as asked - "latest", "26.07.5", a
	// branch name - what becomes the package's version slug and what
	// README/MANIFEST.json report as "version". Distinct from Version.SHA,
	// which is what it resolved to.
	Requested string
	Version   Version
	Cfg       ProjectConfig
	Manifest  store.ReproManifest
	Files     []store.ReproFileBytes
}

// Result is what a render produced. Exactly one of Path (BuildPackage) or Dir
// (StageForRun) is set.
type Result struct {
	Path, Dir string
	Name      string
	SHA       string
	SUT       string
	Note      string
}

// dind's Dockerfile installs this baseline regardless of project - what the
// wrapper itself needs (a shell, a Python interpreter for .py repros, basic
// coreutils) - and nothing that assumes what the SUT speaks. What the SUT's
// OWN protocol needs (a postgres client, say) is ProjectConfig.DindPackages;
// baking a Postgres client into every project's image unconditionally was
// exactly packager.py's SereneDB-shaped leak.
const dindBaseline = "bash python3 py3-pip coreutils procps grep"

var (
	dockerfileDindTmpl = template.Must(template.New("dockerfile-dind").Parse(
		"# Runner: a Docker-in-Docker image carrying only the repro - text, no binary. The\n" +
			"# system-under-test is an image tag (SUT_IMAGE) that must exist: a release tag, or\n" +
			"# one you built first.\n" +
			"FROM docker:27-dind\n" +
			"RUN apk add --no-cache " + dindBaseline + "{{.Packages}}\n" +
			"{{.BakeBin}}COPY repro/ /repro/\n" +
			"WORKDIR /repro\n" +
			"COPY entrypoint.sh /usr/local/bin/repro-entrypoint.sh\n" +
			"RUN chmod +x /usr/local/bin/repro-entrypoint.sh\n" +
			"ENTRYPOINT [\"/usr/local/bin/repro-entrypoint.sh\"]\n"))

	entrypointTmpl = template.Must(template.New("entrypoint").Parse(
		"#!/bin/sh\n" +
			"# Start the bundled Docker daemon, make the SUT tag available to it, then run the repro against it.\n" +
			"set -e\n" +
			"dockerd >/tmp/dockerd.log 2>&1 &\n" +
			"tries=0\n" +
			"until docker info >/dev/null 2>&1; do\n" +
			"\ttries=$((tries + 1))\n" +
			"\tif [ \"$tries\" -gt 90 ]; then\n" +
			"\t\techo \"inner dockerd did not come up:\" >&2\n" +
			"\t\tcat /tmp/dockerd.log >&2\n" +
			"\t\texit 1\n" +
			"\tfi\n" +
			"\tsleep 1\n" +
			"done\n" +
			"# The SUT tag is the prerequisite: load a bundled tar if one is mounted (offline), else pull it.\n" +
			"if [ -f /sut.tar ]; then\n" +
			"\tdocker load -i /sut.tar >/dev/null 2>&1 || docker pull \"$SUT_IMAGE\" >/dev/null 2>&1 || true\n" +
			"else\n" +
			"\tdocker pull \"$SUT_IMAGE\" >/dev/null 2>&1 || true\n" +
			"fi\n" +
			"if ! docker image inspect \"$SUT_IMAGE\" >/dev/null 2>&1; then\n" +
			"\techo \"SUT image '$SUT_IMAGE' is not present - build or pull it first (see README.md).\" >&2\n" +
			"\texit 2\n" +
			"fi\n" +
			"# The repro mounts the project's binary over the runtime. If one is already baked in (a\n" +
			"# source build, {{.LocalBin}}) use it as-is; otherwise extract the SUT tag's own, so nothing\n" +
			"# is shipped and the repro runs exactly the tag under test.\n" +
			"if [ ! -f {{.LocalBin}} ]; then\n" +
			"\tcid=$(docker create \"$SUT_IMAGE\" 2>/dev/null || true)\n" +
			"\tif [ -n \"$cid\" ]; then\n" +
			"\t\tdocker cp \"$cid\":{{.BinPath}} {{.LocalBin}} >/dev/null 2>&1 || true\n" +
			"\t\tdocker rm \"$cid\" >/dev/null 2>&1 || true\n" +
			"\tfi\n" +
			"fi\n" +
			"[ -f {{.LocalBin}} ] && export SUT_BINARY={{.LocalBin}}\n" +
			"exec \"$@\"\n"))

	composeDindTmpl = template.Must(template.New("compose-dind").Parse(
		"# Reproduction of {{.FID}} - {{.Title}}\n" +
			"# Version under test: {{.Version}}  (sha {{.SHA}})\n" +
			"#\n" +
			"# PREREQUISITE: the SUT image tag below must exist locally. A release tag is pullable as-is;\n" +
			"# a source build you tag yourself first (see README.md). Then:\n" +
			"#\n" +
			"#   docker compose up --build --abort-on-container-exit --exit-code-from repro\n" +
			"#\n" +
			"# Exit 0 from the `repro` service means the behaviour in README.md still reproduces on that tag.\n" +
			"# The repro runs inside one privileged container with its own Docker daemon - no host Docker\n" +
			"# socket, no host paths, and no binary shipped in this package (it comes from the tag).\n" +
			"services:\n" +
			"  repro:\n" +
			"    build: .\n" +
			"    image: {{.ImgName}}\n" +
			"    privileged: true          # the bundled Docker daemon (dind) needs it\n" +
			"    environment:\n" +
			"      SUT_IMAGE: \"{{.SUT}}\"      # <- the image tag under test; must be present (build/pull first)\n" +
			"    command: [{{.Cmd}}]\n" +
			"    volumes:\n" +
			"{{.Volumes}}\n" +
			"volumes:\n" +
			"  dind:\n"))

	dockerfilePlainTmpl = template.Must(template.New("dockerfile-plain").Parse(
		"# Runner: the repro run directly in its image - a unit test / script, no Docker-in-Docker.\n" +
			"FROM {{.SUT}}\n" +
			"COPY repro/ /repro/\n" +
			"WORKDIR /repro\n"))

	composePlainTmpl = template.Must(template.New("compose-plain").Parse(
		"# Reproduction of {{.FID}} - {{.Title}}\n" +
			"# Runs the repro directly in {{.SUT}} (version under test: {{.Version}}). One command:\n" +
			"#\n" +
			"#   docker compose up --build --abort-on-container-exit --exit-code-from repro\n" +
			"#\n" +
			"# Exit 0 from the `repro` service means the behaviour in README.md still reproduces.\n" +
			"services:\n" +
			"  repro:\n" +
			"    build: .\n" +
			"    image: {{.ImgName}}\n" +
			"    # The SUT image's own ENTRYPOINT is cleared, because `command:`\n" +
			"    # replaces an image's CMD and is then passed to its ENTRYPOINT as\n" +
			"    # arguments. A release image usually has one - ragflow's is\n" +
			"    # [\"./entrypoint.sh\"] - so without this the repro never runs and the\n" +
			"    # container dies on `stat ./entrypoint.sh: no such file or directory`,\n" +
			"    # a failure about the package's own plumbing recorded against the\n" +
			"    # version under test. Measured on infiniflow/ragflow:v0.26.4.\n" +
			"    entrypoint: []\n" +
			"    working_dir: /repro\n" +
			"    command: [{{.Cmd}}]\n"))

	readmeTmpl = template.Must(template.New("readme").Parse(
		"# Reproduction: {{.FID}} - {{.Title}}\n" +
			"\n" +
			"This package is **text only** - no binary shipped. It reproduces against the image `{{.SUT}}`.\n" +
			"\n" +
			"**1. Make the image present.**\n" +
			"{{.Setup}}\n" +
			"**2. Run the repro.**\n" +
			"\n" +
			"```\n" +
			"docker compose up --build --abort-on-container-exit --exit-code-from repro\n" +
			"```\n" +
			"\n" +
			"The `repro` service exits **0** when the behaviour below still reproduces, non-zero when it does not.\n" +
			"Nothing touches your host Docker or filesystem.\n" +
			"\n" +
			"- **Version under test:** `{{.Version}}`  (`{{.SHA}}`)\n" +
			"- **Image:** `{{.SUT}}`\n" +
			"- **Repro:** `{{.Script}}`  (`{{.Interp}}`)\n" +
			"\n" +
			"---\n" +
			"\n" +
			"{{.Report}}\n"))
)

// subs is the one substitution set every template above draws its fields
// from - not every template uses every field.
type subs struct {
	FID, Title, Version, SHA, Cmd, Script, Interp, Report, ImgName string
	SUT, Setup, Packages, BakeBin, Volumes                         string
	LocalBin, BinPath                                              string
}

// packagesSuffix renders extra apk packages as " pkg1 pkg2", "" when there
// are none - appended straight onto the fixed apk add line.
func packagesSuffix(pkgs []string) string {
	if len(pkgs) == 0 {
		return ""
	}
	return " " + strings.Join(pkgs, " ")
}

// mustJSON quotes a string the way a docker-compose command list item needs
// to be quoted: a double-quoted, backslash-escaped token, valid both as a
// YAML flow-scalar and as a JSON string. strconv.Quote rather than
// json.Marshal - encoding/json HTML-escapes '&', '<', '>' by default (into
// & etc), which is exactly wrong for a shell command line that may
// legitimately contain "&&".
func mustJSON(s string) string {
	return strconv.Quote(s)
}

// composeCommand renders docker-compose's `command: [...]` payload (without
// the brackets). cmdOverride wins outright, run through a shell so it can be
// more than one token - packager.py's fm.get("cmd") path. Otherwise it is
// interp+entrypoint, or just entrypoint when Interp is empty (the manifest
// says the entrypoint runs directly - ReproManifest.Interp's documented
// "empty when the entrypoint is executed directly", which packager.py had no
// equivalent of because its interp was always sniffed from a file extension).
func composeCommand(interp, entrypoint, cmdOverride string) string {
	if cmdOverride != "" {
		return fmt.Sprintf(`"sh", "-c", %s`, mustJSON(cmdOverride))
	}
	parts := make([]string, 0, 2)
	if interp != "" {
		parts = append(parts, mustJSON(interp))
	}
	parts = append(parts, mustJSON(entrypoint))
	return strings.Join(parts, ", ")
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// slug is packager.py's _slug: collapse anything that is not
// alnum/dot/underscore/dash to one dash, trim the ends, lowercase. Used for
// the version segment of a package name - "26.07.5" stays as-is, "my
// branch!" becomes "my-branch".
func slug(s string) string {
	return strings.ToLower(strings.Trim(slugRE.ReplaceAllString(s, "-"), "-"))
}

// Name is the package's own name (no extension, no content hash):
// repro-<project>-<num>-<version slug>-<sha12>. build_package appends a
// content hash on top of this (see BuildPackage); stage_for_run uses it
// as-is for its persistent directory.
func Name(f Finding, requested string, v Version) string {
	return fmt.Sprintf("repro-%s-%s-%s-%s", f.Project, f.Num, slug(requested), shortSHA(v.SHA))
}

var releaseTagInImageRE = regexp.MustCompile(`:\d+\.\d+`)

// isPublished reports whether image is something a plain `docker pull`
// satisfies: a digest-pinned ref, a `:latest` tag, or an X.Y release tag.
func isPublished(image string) bool {
	if image == "" {
		return false
	}
	if strings.Contains(image, "@sha256:") {
		return true
	}
	if strings.HasSuffix(image, ":latest") {
		return true
	}
	return releaseTagInImageRE.MatchString(image)
}

// sutImage is the runtime image a repro actually runs against, and whether
// it is pullable.
//
//   - A release/CI tag IS the full runtime - use it as-is.
//   - A source build has no published image (nothing here `docker save`s a
//     freshly built local image into the dind), so it runs against the
//     project's own pullable BaseImage with the built binary mounted over it
//   - see the entrypoint template. "" only when neither applies: a
//     non-published, non-source image this package cannot use.
func sutImage(v Version, cfg ProjectConfig) (image string, published bool) {
	// A RELEASE THAT DID NOT RESOLVE HAS NO IMAGE, whatever its Image field
	// holds: an unpullable release keeps the tag it was asked for there so
	// the failure can name it, and rendering a package whose FROM is a tag
	// nobody could pull only moves the failure to whoever runs the tarball.
	//
	// Only the release case. A source build that did not resolve still has a
	// real image - the project's own BaseImage, which is pullable whether or
	// not git could find the ref - so a package for it renders exactly as it
	// did before, and a runner with no checkout can still package a finding
	// at "latest" (cmd/handoff-runner's own disclosure test does).
	if v.Unresolved && !v.SourceBuild {
		return "", false
	}
	if v.SourceBuild {
		img := v.Image
		if img == "" {
			img = cfg.BaseImage
		}
		return img, true
	}
	if isPublished(v.Image) {
		return v.Image, true
	}
	return "", false
}

// contentHashFiles is a deterministic 8-hex digest over an in-memory repro
// tree's paths and bytes, sorted by path so file order never changes it.
// Used to key stage_for_run's freshness check against the tree that came out
// of the store, before anything is written to disk.
func contentHashFiles(files []store.ReproFileBytes) string {
	sorted := make([]store.ReproFileBytes, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write(f.Content)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// contentHashDir is the same digest over a directory already on disk - what
// build_package appends to its name, computed over the WHOLE rendered
// package (Dockerfile, compose, README, MANIFEST, repro/, entrypoint.sh),
// not tar bytes, which would carry mtimes and make an identical rebuild look
// different.
func contentHashDir(dir string) (string, error) {
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, rel := range paths {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}

// setupText is README step 1: how to make the SUT image present, which
// depends on isolation and on whether this version is a source build.
func setupText(iso string, v Version, cfg ProjectConfig, image string, published, baked bool) string {
	if iso != "dind" {
		return fmt.Sprintf("Runs in `%s` - Docker pulls it automatically.\n", image)
	}
	if v.SourceBuild {
		binName := filepath.Base(cfg.BinaryPath)
		if binName == "" || binName == "." {
			binName = "binary"
		}
		if baked {
			return fmt.Sprintf("Source build at commit `%s`: the %s binary is baked into the "+
				"runner and mounted over `%s` (pulled in the dind).\n", shortSHA(v.SHA), binName, image)
		}
		return fmt.Sprintf("Source build at commit `%s` - no published image. Build %s at that "+
			"commit and place the binary in this dir as `%s`, then run `docker compose up ...`. "+
			"It is baked in and mounted over `%s`.\n", shortSHA(v.SHA), binName, binName, image)
	}
	if published {
		return "This is a published tag - Docker pulls it automatically. Nothing to build.\n"
	}
	return fmt.Sprintf("This is a source build. Build the image and tag it `%s` first (your CI\n"+
		"already produces this - reuse that image). Then continue.\n", image)
}

// renderInto writes one package to stage: repro/ verbatim, then
// Dockerfile/(entrypoint.sh)/docker-compose.yml/README.md/MANIFEST.json,
// branching on isolation. bake controls whether a source build's binary is
// copied into the package (stage_for_run) or left out (build_package, which
// never ships one). sutTarPath, dind-only, is mounted so a staged run loads
// the SUT tag offline instead of pulling.
func renderInto(stage string, in RenderInput, bake bool, sutTarPath string) (sut string, err error) {
	reproDir := filepath.Join(stage, "repro")
	if err := os.MkdirAll(reproDir, 0o755); err != nil {
		return "", err
	}
	for _, f := range in.Files {
		clean := filepath.Clean(filepath.FromSlash(f.Path))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			return "", fmt.Errorf("repro: %s: repro file path %q escapes the tree", in.Finding.ID, f.Path)
		}
		dst := filepath.Join(reproDir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, f.Content, 0o644); err != nil {
			return "", err
		}
	}

	image, published := sutImage(in.Version, in.Cfg)
	if image == "" {
		return "", fmt.Errorf("repro: %s: %s: SUT image not available yet; resolve/run this "+
			"version first (that builds or pulls it), then package", in.Finding.ID, in.Requested)
	}

	// The isolation is checked here, at the last point before a package is
	// rendered, and not only at the doors that fill RenderInput. Everything
	// below branches on `iso == "dind"` and renders plain otherwise, so an
	// unknown word does not fail here - it produces a package that runs the
	// repro with no daemon, and whoever runs it gets a failure about the
	// code under test. The manifest's own vocabulary decides the answer
	// (store.CheckIsolation); the project default is checked with it,
	// because an operator's typo in default_isolation would downgrade every
	// finding that leaves its own isolation empty.
	iso := in.Manifest.Isolation
	if iso == "" {
		iso = in.Cfg.isolation()
	}
	if err := store.CheckIsolation(iso); err != nil {
		return "", fmt.Errorf("repro: %s: %w", in.Finding.ID, err)
	}
	title := in.Finding.Title
	if title == "" {
		title = in.Finding.ID
	}
	polarity := in.Finding.Polarity
	if polarity == "" {
		polarity = "present"
	}
	cmd := composeCommand(in.Manifest.Interp, in.Manifest.Entrypoint, in.Manifest.CmdOverride)
	imgname := fmt.Sprintf("repro-%s-%s-%s", in.Finding.Project, in.Finding.Num, slug(in.Requested))

	s := subs{
		FID: in.Finding.ID, Title: title, Version: in.Requested, SHA: in.Version.SHA,
		Cmd: cmd, Script: in.Manifest.Entrypoint, Interp: in.Manifest.Interp, Report: in.Finding.Report,
		ImgName: imgname, SUT: image,
	}
	bakedBin := false

	if iso == "dind" {
		s.Volumes = "      - dind:/var/lib/docker  # inner daemon state"
		pkgs := append([]string{}, in.Cfg.DindPackages...)
		if in.Manifest.Interp == "python3" {
			pkgs = append(pkgs, in.Cfg.PythonClientPackages...)
		}
		s.Packages = packagesSuffix(pkgs)
		binName := filepath.Base(in.Cfg.BinaryPath)
		if binName == "" || binName == "." {
			binName = "binary"
		}
		s.LocalBin = "/opt/" + binName
		s.BinPath = in.Cfg.BinaryPath

		if in.Version.SourceBuild {
			s.BakeBin = fmt.Sprintf("COPY %s /opt/%s\nRUN chmod +x /opt/%s\n", binName, binName, binName)
			if bake && in.Version.Binary != "" && fileExists(in.Version.Binary) {
				if err := copyFile(in.Version.Binary, filepath.Join(stage, binName), 0o755); err != nil {
					return "", err
				}
				bakedBin = true
			}
		} else if sutTarPath != "" {
			s.Volumes += fmt.Sprintf("\n      - %s:/sut.tar:ro  # the SUT tag, loaded offline (no pull)", sutTarPath)
		}
		s.Setup = setupText(iso, in.Version, in.Cfg, image, published, bakedBin)

		if err := execTemplate(dockerfileDindTmpl, filepath.Join(stage, "Dockerfile"), s); err != nil {
			return "", err
		}
		if err := execTemplate(entrypointTmpl, filepath.Join(stage, "entrypoint.sh"), s); err != nil {
			return "", err
		}
		if err := os.Chmod(filepath.Join(stage, "entrypoint.sh"), 0o755); err != nil {
			return "", err
		}
		if err := execTemplate(composeDindTmpl, filepath.Join(stage, "docker-compose.yml"), s); err != nil {
			return "", err
		}
	} else {
		s.Setup = setupText(iso, in.Version, in.Cfg, image, published, false)
		if err := execTemplate(dockerfilePlainTmpl, filepath.Join(stage, "Dockerfile"), s); err != nil {
			return "", err
		}
		if err := execTemplate(composePlainTmpl, filepath.Join(stage, "docker-compose.yml"), s); err != nil {
			return "", err
		}
	}

	if err := execTemplate(readmeTmpl, filepath.Join(stage, "README.md"), s); err != nil {
		return "", err
	}

	manifest := manifestDoc{
		Finding: in.Finding.ID, Title: title, Version: in.Requested, SHA: in.Version.SHA,
		Image: image, Isolation: iso, Cmd: in.Manifest.CmdOverride, Interp: in.Manifest.Interp,
		Script: in.Manifest.Entrypoint, ExpectedExit: 0, Polarity: polarity, Note: in.Version.Note,
	}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "MANIFEST.json"), mb, 0o644); err != nil {
		return "", err
	}
	return image, nil
}

// manifestDoc is MANIFEST.json's shape - packager.py's json.dump call,
// typed.
type manifestDoc struct {
	Finding      string `json:"finding"`
	Title        string `json:"title"`
	Version      string `json:"version"`
	SHA          string `json:"sha"`
	Image        string `json:"image"`
	Isolation    string `json:"isolation"`
	Cmd          string `json:"cmd,omitempty"`
	Interp       string `json:"interp"`
	Script       string `json:"script"`
	ExpectedExit int    `json:"expected_exit"`
	Polarity     string `json:"polarity"`
	Note         string `json:"note,omitempty"`
}

func execTemplate(t *template.Template, path string, s subs) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, s)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// BuildPackage renders the downloadable package: EXACTLY Dockerfile,
// docker-compose.yml, README.md, MANIFEST.json, repro/, and for dind
// isolation entrypoint.sh - never a binary, even for a source build (that is
// the whole point of the split with StageForRun). cacheDir is where the tgz
// lands; it is created if missing. The name carries an 8-hex hash over the
// rendered tree's contents, so a no-op rebuild reproduces the same name and a
// real change gets a new one - see contentHashDir.
func BuildPackage(cacheDir string, in RenderInput) (Result, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return Result{}, err
	}
	stage, err := os.MkdirTemp(cacheDir, "pkg-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)

	sut, err := renderInto(stage, in, false, "")
	if err != nil {
		return Result{}, err
	}
	hash, err := contentHashDir(stage)
	if err != nil {
		return Result{}, err
	}
	name := Name(in.Finding, in.Requested, in.Version) + "-" + hash
	tgz := filepath.Join(cacheDir, name+".tgz")
	if err := tarDir(tgz, stage, name); err != nil {
		return Result{}, err
	}
	return Result{Path: tgz, Name: name + ".tgz", SHA: in.Version.SHA, SUT: sut, Note: in.Version.Note}, nil
}

// tarDir writes dir's contents to a gzipped tar at tgzPath, every entry
// prefixed with arcname/ - the same shape `tar.add(stage, arcname=name)`
// produces in packager.py, so extracting the tgz gives one top-level
// directory named after the package rather than the temp dir it was staged
// in.
func tarDir(tgzPath, dir, arcname string) error {
	f, err := os.Create(tgzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		if rel == "." {
			hdr.Name = arcname + "/"
		} else {
			hdr.Name = arcname + "/" + filepath.ToSlash(rel)
			if d.IsDir() {
				hdr.Name += "/"
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.Open(p)
		if err != nil {
			return err
		}
		defer content.Close()
		_, err = io.Copy(tw, content)
		return err
	})
}

// Packager stages runnable packages, which - unlike BuildPackage - needs
// docker (to save the SUT tag into an offline-loadable form). Its run field
// is the same swappable shell-out seen on Resolver.
type Packager struct {
	run runFunc
	mu  sync.Mutex // guards the check-then-render-then-rename in StageForRun
}

// NewPackager returns a Packager that shells to the real docker on PATH.
func NewPackager() *Packager { return &Packager{run: runCommand} }

// TemplateVersion is bumped whenever the Dockerfile/entrypoint/compose
// templates above change, so a persistent staging directory rendered before
// the change is detected as stale and re-rendered rather than a run reusing
// an old layout. See packager.py's PKG_TEMPLATE_VERSION and its comment on
// the finding-8 mount error that not having this caused.
const TemplateVersion = "2026-08-18-go-1"

// stagingKey is what StageForRun compares against a staging directory's
// .staged marker to decide whether it is still fresh: the template version,
// the repro tree's own content hash, and the resolved SUT ref/commit. Any of
// the three changing invalidates the cache - a template edit, a new repro
// file, or "latest" having moved all mean the old directory no longer
// matches what should be there.
func stagingKey(templateVersion, contentHash, sut, sha string) string {
	return templateVersion + ":" + contentHash + ":" + sut + ":" + sha
}

// sutTar `docker save`s the SUT tag to a cache file in cacheDir keyed by the
// image id, so a staged run's inner daemon loads it offline instead of
// pulling - a source-build tag was never pushed to any registry, so this is
// the only way it reaches the dind. "" (not an error) means it could not be
// saved; the entrypoint falls back to `docker pull` in that case.
func (p *Packager) sutTar(ctx context.Context, cacheDir, tag string) string {
	iid := strings.TrimPrefix(p.run(ctx, "docker", "image", "inspect", "-f", "{{.Id}}", tag), "sha256:")
	if iid == "" {
		return ""
	}
	tarPath := filepath.Join(cacheDir, "sut-"+shortSHA(iid)+".tar")
	if fileExists(tarPath) {
		return tarPath
	}
	tmp := tarPath + ".partial"
	p.run(ctx, "docker", "save", tag, "-o", tmp)
	if !fileExists(tmp) {
		return ""
	}
	if err := os.Rename(tmp, tarPath); err != nil {
		return ""
	}
	return tarPath
}

// StageForRun stages the SAME package into a persistent directory under
// cacheDir for a local run: additionally carries the resolved binary (source
// build only) and a .staged marker. A fresh call for the same
// template-version + content-hash + resolved-SUT-ref reuses the existing
// directory instead of re-rendering - see stagingKey.
func (p *Packager) StageForRun(ctx context.Context, cacheDir string, in RenderInput) (Result, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return Result{}, err
	}
	sut, _ := sutImage(in.Version, in.Cfg)
	if sut == "" {
		return Result{}, fmt.Errorf("repro: %s: %s: SUT image not available yet; resolve/run "+
			"this version first (that builds or pulls it), then package", in.Finding.ID, in.Requested)
	}
	name := Name(in.Finding, in.Requested, in.Version)
	final := filepath.Join(cacheDir, "run-"+name)
	marker := filepath.Join(final, ".staged")
	want := stagingKey(TemplateVersion, contentHashFiles(in.Files), sut, in.Version.SHA)

	p.mu.Lock()
	defer p.mu.Unlock()

	if existing, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(existing)) == want {
		return Result{Dir: final, Name: name, SHA: in.Version.SHA, SUT: sut, Note: in.Version.Note}, nil
	}

	var sutTarPath string
	if !in.Version.SourceBuild {
		sutTarPath = p.sutTar(ctx, cacheDir, sut)
	}
	tmp, err := os.MkdirTemp(cacheDir, "run-stage-")
	if err != nil {
		return Result{}, err
	}
	if _, err := renderInto(tmp, in, true, sutTarPath); err != nil {
		os.RemoveAll(tmp)
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".staged"), []byte(want), 0o644); err != nil {
		os.RemoveAll(tmp)
		return Result{}, err
	}
	os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp)
		return Result{}, err
	}
	return Result{Dir: final, Name: name, SHA: in.Version.SHA, SUT: sut, Note: in.Version.Note}, nil
}
