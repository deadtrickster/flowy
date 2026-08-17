package repro

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestSlug covers packager.py's _slug: collapse anything not
// alnum/dot/underscore/dash to one dash, trim the ends, lowercase.
func TestSlug(t *testing.T) {
	cases := map[string]string{
		"26.07.5":         "26.07.5",
		"latest":          "latest",
		"my branch!":      "my-branch",
		"Feature/Thing_2": "feature-thing_2",
		"--weird--":       "weird",
		"a///b":           "a-b",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestName covers the naming scheme: repro-<project>-<num>-<versionSlug>-<sha12>.
func TestName(t *testing.T) {
	f := Finding{Project: "serenedb", Num: "07"}
	v := Version{SHA: "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032"}
	got := Name(f, "latest", v)
	want := "repro-serenedb-07-latest-bc07c51d4b8d"
	if got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	// A release version's dots survive the slug untouched.
	got2 := Name(f, "26.07.5", v)
	if got2 != "repro-serenedb-07-26.07.5-bc07c51d4b8d" {
		t.Errorf("Name(26.07.5) = %q", got2)
	}
}

// TestComposeCommand covers the three shapes a docker-compose `command:`
// list is built from.
func TestComposeCommand(t *testing.T) {
	if got := composeCommand("bash", "repro-01.sh", ""); got != `"bash", "repro-01.sh"` {
		t.Errorf("interp+entrypoint: %q", got)
	}
	if got := composeCommand("", "repro-01", ""); got != `"repro-01"` {
		t.Errorf("empty interp (entrypoint executed directly): %q", got)
	}
	if got := composeCommand("bash", "repro-01.sh", `echo "hi" && exit 1`); got != `"sh", "-c", "echo \"hi\" && exit 1"` {
		t.Errorf("cmd override wins and is safely quoted: %q", got)
	}
}

// TestContentHashDeterministic: the same tree hashes the same every time,
// and a real change in either path or bytes changes the hash - the property
// build_package's "no-op rebuild keeps its name, a real change gets a new
// one" rests on.
func TestContentHashDeterministic(t *testing.T) {
	files := []store.ReproFileBytes{
		{Path: "repro-01.sh", Content: []byte("echo hi\n")},
		{Path: "evidence/run.log", Content: []byte("line one\n")},
	}
	h1 := contentHashFiles(files)
	h2 := contentHashFiles(files)
	if h1 != h2 {
		t.Fatalf("same tree hashed differently: %q vs %q", h1, h2)
	}
	if len(h1) != 8 {
		t.Fatalf("hash is %q, want 8 hex chars", h1)
	}

	reordered := []store.ReproFileBytes{files[1], files[0]}
	if h3 := contentHashFiles(reordered); h3 != h1 {
		t.Errorf("file order changed the hash: %q vs %q - it must sort by path first", h3, h1)
	}

	changed := []store.ReproFileBytes{
		{Path: "repro-01.sh", Content: []byte("echo bye\n")},
		{Path: "evidence/run.log", Content: []byte("line one\n")},
	}
	if h4 := contentHashFiles(changed); h4 == h1 {
		t.Error("changed content produced the same hash")
	}

	// contentHashDir must agree with contentHashFiles for the same tree
	// written to disk - one is the in-memory form used before rendering
	// (staging freshness), the other walks the rendered output on disk
	// (build_package's name suffix); they hash the same rule.
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hd, err := contentHashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hd != h1 {
		t.Errorf("contentHashDir = %q, contentHashFiles = %q - should agree", hd, h1)
	}
}

func testInput(isolation string) RenderInput {
	return RenderInput{
		Finding:   Finding{ID: "f-abc123", Project: "serenedb", Num: "07", Title: "temp_directory escapes"},
		Requested: "latest",
		Version: Version{
			SHA: "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032", Image: "serenedb/base:runtime",
			Buildable: true, SourceBuild: true, Note: "latest @ bc07c51d4b8d (built, cached)",
		},
		Cfg: ProjectConfig{
			BaseImage: "serenedb/base:runtime", BinaryPath: "/usr/bin/serened",
			DefaultIsolation: "plain", DindPackages: []string{"postgresql-client"},
			PythonClientPackages: []string{"py3-psycopg"},
		},
		Manifest: store.ReproManifest{Entrypoint: "repro-07-temp.sh", Interp: "bash", Isolation: isolation},
		Files: []store.ReproFileBytes{
			{Path: "repro-07-temp.sh", Content: []byte("#!/bin/bash\necho reproducing\n")},
			{Path: "RESULT.md", Content: []byte("---\nid: serenedb-0007\n---\nbody\n")},
		},
	}
}

// readTgz extracts a tgz written by BuildPackage into a map of arc-relative
// path -> contents, for assertions on exactly what shipped.
func readTgz(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = content
	}
	return out
}

// TestBuildPackagePlainIsolation: plain isolation ships a Dockerfile FROM
// the SUT image with no daemon and no entrypoint.sh, and never a binary.
func TestBuildPackagePlainIsolation(t *testing.T) {
	cacheDir := t.TempDir()
	in := testInput("plain")
	res, err := BuildPackage(cacheDir, in)
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if res.Path == "" || !strings.HasSuffix(res.Path, ".tgz") {
		t.Fatalf("Path = %q", res.Path)
	}
	if res.SUT != in.Cfg.BaseImage {
		t.Errorf("SUT = %q, want %q", res.SUT, in.Cfg.BaseImage)
	}

	entries := readTgz(t, res.Path)
	prefix := res.Name[:len(res.Name)-len(".tgz")] + "/"
	want := []string{"Dockerfile", "docker-compose.yml", "README.md", "MANIFEST.json",
		"repro/repro-07-temp.sh", "repro/RESULT.md"}
	for _, w := range want {
		if _, ok := entries[prefix+w]; !ok {
			t.Errorf("tgz missing %s (have %v)", prefix+w, keys(entries))
		}
	}
	if _, ok := entries[prefix+"entrypoint.sh"]; ok {
		t.Error("plain isolation must not ship entrypoint.sh")
	}
	if _, ok := entries[prefix+"serened"]; ok {
		t.Error("BuildPackage must never ship a binary")
	}
	df := string(entries[prefix+"Dockerfile"])
	if !strings.Contains(df, "FROM "+in.Cfg.BaseImage) {
		t.Errorf("plain Dockerfile does not FROM the SUT image:\n%s", df)
	}
	if !strings.Contains(string(entries[prefix+"repro/repro-07-temp.sh"]), "echo reproducing") {
		t.Error("repro/ was not copied verbatim")
	}

	var manifest map[string]any
	if err := json.Unmarshal(entries[prefix+"MANIFEST.json"], &manifest); err != nil {
		t.Fatalf("MANIFEST.json does not parse: %v", err)
	}
	if manifest["finding"] != "f-abc123" || manifest["isolation"] != "plain" {
		t.Errorf("MANIFEST.json = %+v", manifest)
	}
	if manifest["polarity"] != "present" {
		t.Errorf("polarity default = %v, want present", manifest["polarity"])
	}
}

// TestBuildPackageDindIsolation: dind isolation ships entrypoint.sh and a
// Dockerfile carrying the project's extra apk packages, still never a
// binary even though this version is a source build with one available.
func TestBuildPackageDindIsolation(t *testing.T) {
	cacheDir := t.TempDir()
	in := testInput("dind")
	in.Manifest.Interp = "python3" // exercise PythonClientPackages too
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "serened")
	if err := os.WriteFile(binPath, []byte("fake-binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	in.Version.Binary = binPath

	res, err := BuildPackage(cacheDir, in)
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	entries := readTgz(t, res.Path)
	prefix := res.Name[:len(res.Name)-len(".tgz")] + "/"

	if _, ok := entries[prefix+"entrypoint.sh"]; !ok {
		t.Fatal("dind isolation must ship entrypoint.sh")
	}
	if _, ok := entries[prefix+"serened"]; ok {
		t.Error("BuildPackage must never bake a binary, even for a source build with one available")
	}
	df := string(entries[prefix+"Dockerfile"])
	if !strings.Contains(df, "docker:27-dind") {
		t.Errorf("dind Dockerfile does not use the dind base:\n%s", df)
	}
	if !strings.Contains(df, "postgresql-client") || !strings.Contains(df, "py3-psycopg") {
		t.Errorf("dind Dockerfile missing project-configured packages:\n%s", df)
	}
	// bakebin COPY line is present (source build), but nothing was staged to
	// satisfy it - that is correct for a download.
	if !strings.Contains(df, "COPY serened /opt/serened") {
		t.Errorf("dind Dockerfile for a source build should still declare the bake-in COPY:\n%s", df)
	}
	ep := string(entries[prefix+"entrypoint.sh"])
	if !strings.Contains(ep, "/usr/bin/serened") || !strings.Contains(ep, "/opt/serened") {
		t.Errorf("entrypoint.sh does not reference the project's configured binary path:\n%s", ep)
	}
}

// TestBuildPackageContentHashChangesOnRepoChange: a no-op rebuild keeps its
// name, a real change to the repro tree gets a new one.
func TestBuildPackageContentHashChangesOnRepoChange(t *testing.T) {
	cacheDir := t.TempDir()
	in := testInput("plain")
	res1, err := BuildPackage(cacheDir, in)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := BuildPackage(cacheDir, in)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Name != res2.Name {
		t.Errorf("an unchanged rebuild got a new name: %q vs %q", res1.Name, res2.Name)
	}

	in.Files[0].Content = append([]byte{}, in.Files[0].Content...)
	in.Files[0].Content = []byte("#!/bin/bash\necho a different repro\n")
	res3, err := BuildPackage(cacheDir, in)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Name == res1.Name {
		t.Error("a changed repro tree kept the same package name")
	}
}

// TestBuildPackageRefusesUnavailableSUT: a non-source-build version with no
// published image (a build no one has run yet) refuses rather than
// rendering a package that names an image nothing can pull.
func TestBuildPackageRefusesUnavailableSUT(t *testing.T) {
	in := testInput("plain")
	in.Version = Version{SHA: "deadbeef", Image: "not-a-real/local-only-image", Buildable: false, SourceBuild: false}
	if _, err := BuildPackage(t.TempDir(), in); err == nil {
		t.Fatal("expected a refusal when the SUT image is not published and not a source build")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestStageForRunBakesBinaryAndReusesCache: unlike BuildPackage,
// StageForRun bakes the resolved binary in and drops a .staged marker, and a
// second call for the same finding/version/content reuses the directory
// instead of re-rendering.
func TestStageForRunBakesBinaryAndReusesCache(t *testing.T) {
	cacheDir := t.TempDir()
	in := testInput("dind")
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "serened")
	if err := os.WriteFile(binPath, []byte("fake-binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	in.Version.Binary = binPath

	p := &Packager{run: func(context.Context, string, ...string) string {
		t.Fatal("a source-build stage must never shell out to docker (no sut.tar needed)")
		return ""
	}}

	res, err := p.StageForRun(context.Background(), cacheDir, in)
	if err != nil {
		t.Fatalf("StageForRun: %v", err)
	}
	baked := filepath.Join(res.Dir, "serened")
	content, err := os.ReadFile(baked)
	if err != nil {
		t.Fatalf("baked binary missing: %v", err)
	}
	if string(content) != "fake-binary-bytes" {
		t.Errorf("baked binary content = %q", content)
	}
	marker := filepath.Join(res.Dir, ".staged")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf(".staged marker missing: %v", err)
	}

	// Plant a sentinel inside the staged dir; if a second call re-renders it
	// gets wiped (renderInto renders into a fresh temp dir and swaps it in).
	sentinel := filepath.Join(res.Dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}

	res2, err := p.StageForRun(context.Background(), cacheDir, in)
	if err != nil {
		t.Fatalf("StageForRun (second call): %v", err)
	}
	if res2.Dir != res.Dir {
		t.Errorf("second call staged to a different dir: %q vs %q", res2.Dir, res.Dir)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Error("a fresh second call re-rendered the staging dir instead of reusing it")
	}
}

// TestStageForRunRestagesWhenContentChanges: a repro-tree edit must
// invalidate the cached staging dir (a stale layout silently reused was the
// finding-8 mount error packager.py's own history calls out).
func TestStageForRunRestagesWhenContentChanges(t *testing.T) {
	cacheDir := t.TempDir()
	in := testInput("dind")
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "serened")
	if err := os.WriteFile(binPath, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	in.Version.Binary = binPath
	p := &Packager{run: func(context.Context, string, ...string) string { return "" }}

	res1, err := p.StageForRun(context.Background(), cacheDir, in)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(res1.Dir, "sentinel")
	os.WriteFile(sentinel, []byte("x"), 0o644)

	in.Files[0].Content = []byte("#!/bin/bash\necho changed\n")
	res2, err := p.StageForRun(context.Background(), cacheDir, in)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Dir != res1.Dir {
		t.Fatalf("staging directory name changed on a content edit: %q vs %q (name is keyed on "+
			"finding+version+sha, not content, by design - only the CONTENTS should invalidate)", res2.Dir, res1.Dir)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("a changed repro tree should have invalidated and re-rendered the staging dir")
	}
	got, err := os.ReadFile(filepath.Join(res2.Dir, "repro", "repro-07-temp.sh"))
	if err != nil || !strings.Contains(string(got), "changed") {
		t.Errorf("restaged repro content = %q, %v", got, err)
	}
}
