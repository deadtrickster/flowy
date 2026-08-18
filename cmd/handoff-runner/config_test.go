package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// write puts a config file in a temp dir and hands back its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// noEnv is a configuration read with nothing in the environment, so a test
// says what it means rather than inheriting whatever the machine running it
// happens to export.
func noEnv(string) string { return "" }

const minimal = `{
  "dsn": "postgres://x/y",
  "cache_dir": "/var/lib/handoff-runner",
  "projects": {
    "serenedb": {
      "source": "/src/serenedb",
      "base_image": "serenedb/serenedb:26.07.4",
      "registry": "serenedb/serenedb",
      "binary_path": "/usr/bin/serened"
    }
  }
}`

func TestAConfigFillsItsOwnDefaultsAndKeepsCachesApartPerProject(t *testing.T) {
	cfg, err := LoadConfig(write(t, minimal), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != defaultAddr {
		t.Errorf("addr defaulted to %q, want %q", cfg.Addr, defaultAddr)
	}
	if cfg.Workers != defaultWorkers {
		t.Errorf("workers defaulted to %d, want %d", cfg.Workers, defaultWorkers)
	}
	if cfg.RunTimeout.Duration() != defaultRunTimeout {
		t.Errorf("run timeout defaulted to %s", cfg.RunTimeout.Duration())
	}
	if cfg.LogDir != "/var/lib/handoff-runner/runs" {
		t.Errorf("log dir defaulted to %q", cfg.LogDir)
	}
	p := cfg.Projects["serenedb"]
	// Every derived directory carries the project name. version.go warns that
	// two projects sharing a cache could serve each other's binaries when the
	// binaries are named alike, and the cheapest way for that never to happen
	// is for the default to be per project.
	for _, dir := range []string{p.CacheDir, p.BuildCacheDir, p.PackageDir} {
		if !strings.Contains(dir, "/serenedb/") {
			t.Errorf("derived dir %q does not name the project", dir)
		}
	}
}

func TestTheEnvironmentWinsOverTheFile(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":           "postgres://elsewhere/db",
		"HANDOFF_RUNNER_ADDR":    "0.0.0.0:9999",
		"HANDOFF_RUNNER_WORKERS": "4",
		"FLOWY_NODE":             "dogfood",
	}
	cfg, err := LoadConfig(write(t, minimal), func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DSN != "postgres://elsewhere/db" || cfg.Addr != "0.0.0.0:9999" ||
		cfg.Workers != 4 || cfg.Node != "dogfood" {
		t.Errorf("the environment did not win: %+v", cfg)
	}
}

// TestADurationIsWrittenTheWayAPersonWritesOne - and a bare number is
// refused rather than read as nanoseconds, which is what a person who wrote
// 30 meaning half a minute would otherwise get.
func TestADurationIsWrittenTheWayAPersonWritesOne(t *testing.T) {
	cfg, err := LoadConfig(write(t, `{
	  "dsn": "postgres://x/y", "cache_dir": "/tmp/c", "run_timeout": "90s",
	  "projects": {"p": {"source": "/s", "base_image": "i"}}}`), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RunTimeout.Duration() != 90*time.Second {
		t.Errorf("run_timeout read as %s", cfg.RunTimeout.Duration())
	}
	_, err = LoadConfig(write(t, `{
	  "dsn": "postgres://x/y", "cache_dir": "/tmp/c", "run_timeout": 30,
	  "projects": {"p": {"source": "/s", "base_image": "i"}}}`), noEnv)
	if err == nil || !strings.Contains(err.Error(), "\"25m\"") {
		t.Errorf("a bare number should be refused with an example, got %v", err)
	}
}

// TestAConfigRefusesRatherThanGuesses walks every refusal, because each one
// is a mistake that is invisible afterwards: a misspelled key that loads
// anyway resolves versions against an empty image and reports the result as
// a fact.
func TestAConfigRefusesRatherThanGuesses(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a misspelled key", `{"dsn":"d","cache_dir":"/c","base_imagee":"x",
			"projects":{"p":{"source":"/s","base_image":"i"}}}`, "base_imagee"},
		{"no dsn", `{"cache_dir":"/c","projects":{"p":{"source":"/s","base_image":"i"}}}`,
			"same Postgres"},
		{"no projects", `{"dsn":"d","cache_dir":"/c","projects":{}}`, "no projects configured"},
		{"no cache dir", `{"dsn":"d","projects":{"p":{"source":"/s","base_image":"i"}}}`,
			"no cache_dir"},
		{"a project name that is a path", `{"dsn":"d","cache_dir":"/c",
			"projects":{"../etc":{"source":"/s","base_image":"i"}}}`, "not a usable name"},
		{"a project that resolves nothing", `{"dsn":"d","cache_dir":"/c",
			"projects":{"p":{"base_image":"i"}}}`, "neither a source checkout nor a registry"},
		{"source with no base image", `{"dsn":"d","cache_dir":"/c",
			"projects":{"p":{"source":"/s"}}}`, "names no base_image"},
		{"a registry with no binary path", `{"dsn":"d","cache_dir":"/c",
			"projects":{"p":{"registry":"o/r"}}}`, "names no binary_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(write(t, tc.body), noEnv)
			if err == nil {
				t.Fatalf("%s should be refused", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal was %q, wanted it to mention %q", err, tc.want)
			}
		})
	}
}

// TestABuildScriptThatCannotRunIsRefusedAtStartup, not at the first run that
// needs it: a runner that accepts a build script it cannot execute reports
// the problem hours later as a failed build, attributed to the commit.
func TestABuildScriptThatCannotRunIsRefusedAtStartup(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "build-sut.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"dsn":"d","cache_dir":"/c","build_script":"` + script + `",
		"projects":{"p":{"source":"/s","base_image":"i"}}}`
	if _, err := LoadConfig(write(t, body), noEnv); err == nil ||
		!strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("a non-executable build script should be refused, got %v", err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(write(t, body), noEnv)
	if err != nil {
		t.Fatalf("an executable build script should load: %v", err)
	}
	// Naming the script also names its config directory, so build-sut.sh
	// resolves --project NAME to NAME.env exactly as a human running it does.
	if cfg.Projects["p"].BuildConfig != filepath.Join(dir, "build-sut.d", "p.env") {
		t.Errorf("build config derived as %q", cfg.Projects["p"].BuildConfig)
	}
}

// TestOnlyAConfiguredProjectIsReachable is the shape of the whole boundary in
// one assertion: the set of projects this process may touch is the set of
// keys an operator wrote, and a name that is not one of them resolves to
// nothing at all.
func TestOnlyAConfiguredProjectIsReachable(t *testing.T) {
	cfg, err := LoadConfig(write(t, minimal), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.ProjectConfig("serenedb"); !ok {
		t.Error("the configured project should resolve")
	}
	for _, name := range []string{"", "flowy", "/home/dead/Projects/serenedb", "serenedb/../flowy"} {
		if _, ok := cfg.ProjectConfig(name); ok {
			t.Errorf("%q should not resolve to a project", name)
		}
	}
	if got := cfg.ProjectNames(); len(got) != 1 || got[0] != "serenedb" {
		t.Errorf("project names came back as %v", got)
	}
}

// TestTheExampleConfigIsOneThisBinaryWouldActuallyAccept. A documented
// example that does not load is worse than none: it is the first thing an
// operator copies, and it fails at startup with a message about a key the
// example itself told them to write.
//
// Only build_script is rewritten, to an executable that exists on the
// machine running the test - the file's own path names a deployment, and
// checking a deployment's paths exist here would be checking the wrong
// machine.
func TestTheExampleConfigIsOneThisBinaryWouldActuallyAccept(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "handoff-runner.example.json"))
	if err != nil {
		t.Fatalf("read the example: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the example is not JSON: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "build-sut.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc["build_script"] = script
	delete(doc, "build_config_dir")
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(write(t, string(body)), noEnv)
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}
	if len(cfg.Projects) == 0 {
		t.Error("the example configures no project, so it demonstrates nothing")
	}
	if cfg.Comment == "" {
		t.Error("the example lost its own explanation")
	}
}

// TestAProjectTranslatesIntoTheReproConfigWhole - every field the resolver
// and the packager read is carried across, because a field silently left at
// its zero value here is a package built against the wrong image.
func TestAProjectTranslatesIntoTheReproConfigWhole(t *testing.T) {
	p := Project{
		Source: "/src", DefaultBranch: "trunk", BaseImage: "img:1", Registry: "org/repo",
		BinaryPath: "/usr/bin/x", CacheDir: "/c/bin", BuildCacheDir: "/c/build",
		PrebuiltBinary: "/src/build/x", DefaultIsolation: "dind",
		DindPackages: []string{"postgresql-client"}, PythonClientPackages: []string{"py3-psycopg"},
	}
	got := p.ReproConfig()
	if got.Source != p.Source || got.DefaultBranch != p.DefaultBranch ||
		got.BaseImage != p.BaseImage || got.Registry != p.Registry ||
		got.BinaryPath != p.BinaryPath || got.CacheDir != p.CacheDir ||
		got.BuildCacheDir != p.BuildCacheDir || got.PrebuiltBinary != p.PrebuiltBinary ||
		got.DefaultIsolation != p.DefaultIsolation ||
		len(got.DindPackages) != 1 || len(got.PythonClientPackages) != 1 {
		t.Errorf("the translation dropped something: %+v", got)
	}
}
