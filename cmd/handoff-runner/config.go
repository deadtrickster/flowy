package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
)

// Config is the whole of what this binary is allowed to do, read once at
// startup from a file the operator wrote.
//
// EVERYTHING PROJECT-SPECIFIC IS A NAMED KEY AND NEVER A PATH IN A REQUEST.
// The Python service this replaces held SereneDB's checkout path, base image
// and cache dir as module constants, so it could only ever run one project's
// findings; the fix is not "let the caller say where the source is", it is
// "the operator names the projects, one entry each". A caller that could
// hand over a source path could make a process with Docker on the trusted
// host build and run any directory on that machine, which is the one thing
// this boundary exists to prevent.
type Config struct {
	// Comment is a note the operator left in the file. It exists because
	// unknown keys are refused below, and a strictness that leaves nowhere
	// to write down WHY a value is what it is gets relaxed the first time
	// somebody needs to explain one. JSON has no comments; this is the key
	// that is one, at both levels.
	Comment string `json:"//,omitempty"`
	// Addr is the listen address. Loopback by default on purpose: this
	// process holds Docker and a source checkout, and the deployment that
	// exposes it (row 14) should be doing so through something that
	// terminates TLS and knows where the callers are.
	Addr string `json:"addr"`
	// DSN is the SAME Postgres the Flowy node writes to - not a copy, not a
	// feed. A run's verdict is a signed finding.run event on the finding's
	// own log, and it is one log or it is nothing.
	DSN string `json:"dsn"`
	// Node is the node name stamped onto rows this process writes. It should
	// be the Flowy node's name: the verdicts belong to that node's history,
	// and a second name would make one deployment look like two peers that
	// never replicate.
	Node string `json:"node"`
	// CacheDir is the root under which packages, staged run directories and
	// per-project binary caches live, unless a project names its own.
	CacheDir string `json:"cache_dir"`
	// LogDir is where a run's log file is written. GET /run/{id}/log serves
	// out of here and refuses to serve a path that resolves outside it.
	LogDir string `json:"log_dir"`
	// Workers is how many runs execute at once. Each is a privileged
	// docker-in-docker unit, so the default is small.
	Workers int `json:"workers"`
	// RunTimeout, BuildTimeout and PackageBuildTimeout bound the three
	// waits, matching runner.py's RUN_TIMEOUT, BUILD_TIMEOUT and
	// PKG_BUILD_TIMEOUT. A cold build of a real database is measured in
	// hours, which is why the middle one is so much larger than the others.
	RunTimeout          duration `json:"run_timeout"`
	BuildTimeout        duration `json:"build_timeout"`
	PackageBuildTimeout duration `json:"package_build_timeout"`
	// BuildScript is scripts/build-sut.sh - the one thing that compiles a
	// system under test at a sha. It is named here rather than found at run
	// time because "which script builds our binaries" is an operator's
	// decision about this machine, and a runner that searched for one could
	// be pointed at a different one by anything that could write a file.
	BuildScript string `json:"build_script"`
	// BuildConfigDir is that script's per-project config directory
	// (scripts/build-sut.d), passed through as SUT_CONFIG_DIR so the script
	// resolves --project NAME to NAME.env the same way a human running it
	// does.
	BuildConfigDir string `json:"build_config_dir"`
	// Projects is the whole set of projects whose findings this runner will
	// resolve, package or run, keyed by the project name a finding carries.
	Projects map[string]Project `json:"projects"`
}

// Project is one project's answer to "which checkout, which image, which
// cache" - the fields internal/repro's resolver and packager need, plus the
// build script's own config file. Nothing here describes a repro tree: that
// lives on the finding, in its manifest.
type Project struct {
	Comment              string   `json:"//,omitempty"`
	Source               string   `json:"source"`
	DefaultBranch        string   `json:"default_branch"`
	BaseImage            string   `json:"base_image"`
	Registry             string   `json:"registry"`
	BinaryPath           string   `json:"binary_path"`
	CacheDir             string   `json:"cache_dir"`
	BuildCacheDir        string   `json:"build_cache_dir"`
	PrebuiltBinary       string   `json:"prebuilt_binary"`
	DefaultIsolation     string   `json:"default_isolation"`
	DindPackages         []string `json:"dind_packages"`
	PythonClientPackages []string `json:"python_client_packages"`
	// BuildConfig is the build-sut.sh config file for this project, when it
	// is not simply <build_config_dir>/<name>.env.
	BuildConfig string `json:"build_config"`
	// PackageDir is where this project's built packages and staged run
	// directories land. Defaulted under Config.CacheDir when empty.
	PackageDir string `json:"package_dir"`
}

// duration is a time.Duration that reads "25m" or "3h" in JSON. Durations in
// a config file are written by a person, and a bare number of nanoseconds -
// which is what time.Duration marshals as - is a number nobody writes
// correctly twice.
type duration time.Duration

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 == nil {
			return fmt.Errorf("a duration is written as a string like \"25m\", not as the number %d", n)
		}
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}

func (d duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// Duration is the plain time.Duration behind it.
func (d duration) Duration() time.Duration { return time.Duration(d) }

// projectNameRE is what a project key may look like. It becomes a path
// component under CacheDir and the --project argument to build-sut.sh, which
// applies the same rule from the other side; a name that is not this shape
// is refused at load rather than at the first run that happens to use it.
var projectNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Defaults that are only defaults: every one of them is overridable, and
// none of them decides anything that could be silently wrong.
const (
	defaultAddr                = "127.0.0.1:8801"
	defaultWorkers             = 2
	defaultRunTimeout          = 25 * time.Minute
	defaultBuildTimeout        = 3 * time.Hour
	defaultPackageBuildTimeout = 30 * time.Minute
)

// LoadConfig reads the config file, lets the environment override the few
// values a deployment moves around, fills defaults and then refuses anything
// it cannot honour.
//
// UNKNOWN KEYS ARE AN ERROR. A misspelled "base_image" in a config that
// loaded anyway is a project that resolves every version against an empty
// image and reports it as a fact - the failure would show up as a strange
// run verdict hours later, attributed to the code rather than to the typo.
func LoadConfig(path string, env func(string) string) (*Config, error) {
	if env == nil {
		env = os.Getenv
	}
	cfg := &Config{}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	}

	// The environment wins over the file, for the same reason build-sut.sh
	// lets it win: one deployment's config file should be runnable against a
	// different database or port without editing the file the runner reads.
	if v := env("HANDOFF_RUNNER_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := env("DATABASE_URL"); v != "" {
		cfg.DSN = v
	}
	if v := env("FLOWY_NODE"); v != "" {
		cfg.Node = v
	}
	if v := env("HANDOFF_RUNNER_CACHE"); v != "" {
		cfg.CacheDir = v
	}
	if v := env("HANDOFF_RUNNER_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("HANDOFF_RUNNER_WORKERS=%q is not a worker count", v)
		}
		cfg.Workers = n
	}
	if v := env("HANDOFF_RUNNER_BUILD_SCRIPT"); v != "" {
		cfg.BuildScript = v
	}

	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.Workers == 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = duration(defaultRunTimeout)
	}
	if cfg.BuildTimeout == 0 {
		cfg.BuildTimeout = duration(defaultBuildTimeout)
	}
	if cfg.PackageBuildTimeout == 0 {
		cfg.PackageBuildTimeout = duration(defaultPackageBuildTimeout)
	}
	if cfg.BuildScript != "" && cfg.BuildConfigDir == "" {
		cfg.BuildConfigDir = filepath.Join(filepath.Dir(cfg.BuildScript), "build-sut.d")
	}

	if err := cfg.check(); err != nil {
		return nil, err
	}
	cfg.fill()
	return cfg, nil
}

// check is every refusal, in the order a reader would ask them. Each one is
// a thing that cannot be discovered later from a run's output: a runner with
// no database writes no verdict anywhere, and a runner with no projects
// answers every request with the same 404 for a reason nobody can see.
func (c *Config) check() error {
	if c.DSN == "" {
		return fmt.Errorf("no DSN: set DATABASE_URL or \"dsn\" in the config - " +
			"it must be the same Postgres the Flowy node writes to, because a run's " +
			"verdict is an event on the finding's own log")
	}
	if c.Workers < 1 {
		return fmt.Errorf("workers must be at least 1, not %d", c.Workers)
	}
	if len(c.Projects) == 0 {
		return fmt.Errorf("no projects configured: this runner would refuse every " +
			"finding it was handed, and say the same thing about all of them")
	}
	if c.CacheDir == "" {
		return fmt.Errorf("no cache_dir: packages, staged runs and built binaries " +
			"all need somewhere to live that survives a restart")
	}
	for _, name := range sortedKeys(c.Projects) {
		p := c.Projects[name]
		if !projectNameRE.MatchString(name) {
			return fmt.Errorf("project %q is not a usable name: it becomes a directory "+
				"under the cache and the --project argument to build-sut.sh", name)
		}
		if p.Source == "" && p.Registry == "" {
			return fmt.Errorf("project %q names neither a source checkout nor a registry, "+
				"so no version string could ever resolve to anything for it", name)
		}
		if p.Source != "" && p.BaseImage == "" {
			return fmt.Errorf("project %q builds from source but names no base_image: "+
				"a source build has nothing to run its binary on top of", name)
		}
		if p.Registry != "" && p.BinaryPath == "" {
			return fmt.Errorf("project %q resolves releases but names no binary_path, "+
				"so there is nothing to copy out of a release image", name)
		}
		// default_isolation is what every finding that leaves its own
		// isolation empty runs under, so a word the packager does not build
		// is refused at load rather than quietly downgrading each of those
		// runs to plain. Same vocabulary as a manifest's own field, from the
		// same place.
		if err := store.CheckIsolation(p.DefaultIsolation); err != nil {
			return fmt.Errorf("project %q's default_isolation: %w", name, err)
		}
	}
	if c.BuildScript != "" {
		st, err := os.Stat(c.BuildScript)
		if err != nil {
			return fmt.Errorf("build_script %s: %w", c.BuildScript, err)
		}
		if st.IsDir() || st.Mode()&0o111 == 0 {
			return fmt.Errorf("build_script %s is not an executable file", c.BuildScript)
		}
	}
	return nil
}

// fill gives every project the cache directories it did not name, derived
// from its own name.
//
// PER PROJECT, ALWAYS. version.go's cacheFileName already notes that two
// projects sharing a cache directory could serve each other's binaries if
// their binaries were named the same; deriving the directory from the
// project name means the question never arises, and it costs nothing.
func (c *Config) fill() {
	if c.LogDir == "" {
		c.LogDir = filepath.Join(c.CacheDir, "runs")
	}
	for name, p := range c.Projects {
		root := filepath.Join(c.CacheDir, name)
		if p.CacheDir == "" {
			p.CacheDir = filepath.Join(root, "binaries")
		}
		if p.BuildCacheDir == "" {
			p.BuildCacheDir = filepath.Join(root, "build")
		}
		if p.PackageDir == "" {
			p.PackageDir = filepath.Join(root, "packages")
		}
		if p.BuildConfig == "" && c.BuildConfigDir != "" {
			p.BuildConfig = filepath.Join(c.BuildConfigDir, name+".env")
		}
		c.Projects[name] = p
	}
}

// ReproConfig is one project as internal/repro wants it. It is a translation
// and not an embedding: this file's Project also carries what build-sut.sh
// needs, which the resolver and packager have no business seeing.
func (p Project) ReproConfig() repro.ProjectConfig {
	return repro.ProjectConfig{
		Source:               p.Source,
		DefaultBranch:        p.DefaultBranch,
		BaseImage:            p.BaseImage,
		Registry:             p.Registry,
		BinaryPath:           p.BinaryPath,
		CacheDir:             p.CacheDir,
		BuildCacheDir:        p.BuildCacheDir,
		PrebuiltBinary:       p.PrebuiltBinary,
		DefaultIsolation:     p.DefaultIsolation,
		DindPackages:         p.DindPackages,
		PythonClientPackages: p.PythonClientPackages,
	}
}

// ProjectConfig is the lookup the resolver, the packager and row 08's runner
// all go through: a project name in, its configuration out, and false for
// anything that is not a configured key. It is the whole of the answer to
// "which projects may this process touch".
func (c *Config) ProjectConfig(name string) (repro.ProjectConfig, bool) {
	p, ok := c.Projects[name]
	if !ok {
		return repro.ProjectConfig{}, false
	}
	return p.ReproConfig(), true
}

// ProjectNames is the configured keys, sorted - what an operator sees in a
// refusal, so that "no such project" says which ones there are.
func (c *Config) ProjectNames() []string { return sortedKeys(c.Projects) }

func sortedKeys(m map[string]Project) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
