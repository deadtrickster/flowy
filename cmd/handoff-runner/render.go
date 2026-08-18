package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
)

// findingType is the artifact type a repro belongs to. internal/store keeps
// the same constant unexported for its own narrowing; the check here is not
// a second copy of that rule but the door's own refusal, so that a caller
// who hands over a todo id is told what is wrong rather than being told the
// todo has no repro tree.
const findingType = "finding"

// versionRE is what a version string may look like before it is handed to
// the resolver.
//
// THE RESOLVER SHELLS TO GIT AND DOCKER WITH THIS STRING AS AN ARGUMENT. It
// does so through exec.Command with a real argv, so there is no shell to
// inject into - but a value beginning with "-" is still read by git as an
// OPTION rather than as a ref, and "git rev-parse --some-flag" is a
// different program from the one this code believes it is running. So a ref
// is a ref: the characters git itself allows in one, and nothing that starts
// with a dash.
var versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// checkVersion refuses a version string that is not ref-shaped.
func checkVersion(v string) error {
	if v == "" {
		return nil // the resolver's own default, "latest"
	}
	if !versionRE.MatchString(v) {
		return refuse(http.StatusBadRequest, fmt.Sprintf(
			"%q is not a version: a version is latest, a branch, a commit sha or a release "+
				"tag, and it is passed to git as a ref", v))
	}
	return nil
}

// checkIsolation refuses a manifest whose isolation the packager does not
// know, instead of letting it be silently downgraded.
//
// The vocabulary is store.CheckIsolation's and not a second copy of it: the
// manifest is the store's field, so the set of words it may hold is decided
// there, in one named place, and this door only turns that refusal into an
// HTTP answer. Since that check now runs at write time too, a finding
// reaching here with an unknown isolation is one written before the
// vocabulary was narrowed - "vm" and "container", which nothing ever built.
// It is still refused rather than downgraded: running a repro that needs its
// own Docker daemon without one makes it fail for a reason that has nothing
// to do with the code under test, and that failure would be recorded as a
// verdict.
//
// 409 rather than 400 because the request is well formed and the finding is
// the thing that cannot be served: rewrite its manifest with an isolation
// this runner builds, and the same request works.
func checkIsolation(iso string) error {
	if err := store.CheckIsolation(iso); err != nil {
		return refuse(http.StatusConflict, fmt.Sprintf(
			"this finding's repro cannot be run as recorded: %s - running it anyway would "+
				"run it under the wrong isolation and report the failure as a verdict", err))
	}
	return nil
}

// renderInput assembles everything one package render needs for one finding
// at one version, reading THROUGH THE CALLER'S PRINCIPAL at every step.
//
// The three reads are three separate permission decisions and all of them
// are the store's, not this binary's: ReadArtifact answers whether this
// principal may see the finding at all, ReadFindingRepro answers it again
// for the tree and once more per attachment, and the project lookup answers
// whether this deployment is configured to touch that project. Nothing here
// widens any of them.
func (s *service) renderInput(
	ctx context.Context, p *store.Principal, findingID, version string,
) (repro.RenderInput, error) {
	if err := checkVersion(version); err != nil {
		return repro.RenderInput{}, err
	}
	art, err := s.db.ReadArtifact(ctx, p, findingID, false)
	if err != nil {
		return repro.RenderInput{}, err
	}
	if art.Type != findingType {
		return repro.RenderInput{}, refuse(http.StatusBadRequest, fmt.Sprintf(
			"%s is a %s, not a finding - only a finding carries a repro tree",
			findingID, art.Type))
	}
	if art.Project == nil || *art.Project == "" {
		return repro.RenderInput{}, refuse(http.StatusBadRequest, fmt.Sprintf(
			"finding %s belongs to no project, so there is no source checkout, "+
				"base image or cache to run it against", findingID))
	}
	project := *art.Project
	cfg, ok := s.cfg.ProjectConfig(project)
	if !ok {
		return repro.RenderInput{}, refuse(http.StatusNotFound, fmt.Sprintf(
			"this runner is not configured for project %q - it holds %s",
			project, strings.Join(s.cfg.ProjectNames(), ", ")))
	}

	manifest, files, err := s.db.ReadFindingRepro(ctx, p, findingID)
	if err != nil {
		return repro.RenderInput{}, err
	}
	if err := checkIsolation(manifest.Isolation); err != nil {
		return repro.RenderInput{}, err
	}

	return repro.RenderInput{
		Finding: repro.Finding{
			ID:       art.ID,
			Project:  project,
			Num:      findingNum(art.ID),
			Title:    art.Title,
			Report:   reportOf(art),
			Polarity: fieldString(art, "polarity"),
		},
		Requested: version,
		Version:   s.resolver.Resolve(ctx, cfg, version),
		Cfg:       cfg,
		Manifest:  manifest,
		Files:     files,
	}, nil
}

// findingNum is the short, filename-safe token the package name carries.
//
// hands-off's findings were files in numbered directories, so "serenedb-0007"
// gave the packager a 7 to print. A Flowy finding's id is a ULID and has no
// such digit, and the packager says as much: Num is whatever short stable
// token the caller wants. The last six characters of the id are that - stable
// for the life of the finding, short enough to read out, and the tail of a
// ULID is its random half rather than its timestamp, so two findings minted in
// the same millisecond still get different names.
func findingNum(id string) string {
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return strings.ToLower(id)
}

// reportOf is what the package's README says the finding is.
//
// IT IS THE BODY AND NEVER THE DISCOVERY. A finding carries both: the body
// is the write-up, and Discovery is the investigation record - how it was
// actually found, in our own words, which is exactly what the Python service
// served only to an authenticated caller and never baked into anything it
// handed out. A package is made to be sent to the project it is about, so
// the candid half must not be in it, and the way to guarantee that is for
// the code that builds packages to never read that field at all.
func reportOf(art *store.Artifact) string {
	if strings.TrimSpace(art.Body) != "" {
		return art.Body
	}
	return "No write-up was recorded for this finding (" + art.ID + "). " +
		"The repro tree below is the whole of what it says."
}

// fieldString reads one string off a finding's Fields, "" for anything that
// is not there or is not a string. Polarity is optional and a finding
// without one takes the packager's default.
func fieldString(art *store.Artifact, key string) string {
	if len(art.Fields) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(art.Fields, &fields); err != nil {
		return ""
	}
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
