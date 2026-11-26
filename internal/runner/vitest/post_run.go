package vitest

import (
	"fmt"
	"sort"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

type baseKeyTarget struct {
	absPath  string
	fileHash string
}

func computeTargetBaseKey(env *runEnv, parsed ParsedArgs, target baseKeyTarget) string {
	normVer := ""
	if env.cfg.NormalizeHash {
		normVer = fingerprint.NormalizeVersion
	}
	return fingerprint.ComputeBaseKey(fingerprint.BaseKeyInputs{
		FormatVersion:    cache.FormatVersion,
		NormalizeVersion: normVer,
		Runner:           string(pkgVitest),
		RunnerVersion:    env.proj.VitestVersion,
		NodeVersion:      env.nodeVer,
		ConfigHash:       env.cfgHash,
		ProjectFiles:     env.proj.ProjectFiles,
		Args:             parsed.CacheableFlags,
		TestFile:         relSlashPath(env.proj.RepoRoot, target.absPath),
		TestFileHash:     target.fileHash,
		Env:              env.envPairs,
	})
}

// refreshInputsAfterRun re-reads git and the base-key inputs now that the run is over, adopts
// the main-process inputs the run reported, re-keys the missed files with them and returns a
// reason when nothing from this run may be cached.
func refreshInputsAfterRun(req persistResultsRequest, report reportPayload) string {
	env := req.env
	env.reloadGitState()
	if report.MainInputs == nil {
		return "vitest did not report what its main process read"
	}
	if report.UncacheableReason != "" {
		return report.UncacheableReason
	}
	if reason := env.rejectNodeMismatch(report.NodeVersion); reason != "" {
		return reason
	}
	if err := env.adoptConfigInputs(*report.MainInputs); err != nil {
		return fmt.Sprintf("config inputs could not be saved: %v", err)
	}
	if err := env.loadBaseInputs(); err != nil {
		return fmt.Sprintf("project files unreadable after the run: %v", err)
	}
	rekeyTargets(env, req.parsed, req.misses)
	env.rekeyDiscovery(req.parsed)
	return req.window.rejectProjectFiles(env)
}

func rekeyTargets(env *runEnv, parsed ParsedArgs, states []*targetState) {
	for _, st := range states {
		st.baseKey = computeTargetBaseKey(env, parsed, baseKeyTarget{absPath: st.absPath, fileHash: st.fileHash})
	}
}

// reloadConfigInputs picks up a config-inputs record written by another tc process since this
// one started and re-keys states with it. It reports nothing: a failed reload just keeps the keys.
func (e *runEnv) reloadConfigInputs(parsed ParsedArgs, states []*targetState) {
	latest := e.store.LoadConfigInputs(e.cfgInputsKey)
	if latest == nil || (e.cfgInputs != nil && sameConfigInputs(*latest, *e.cfgInputs)) {
		return
	}
	previous := e.cfgInputs
	e.cfgInputs = latest
	if err := e.loadBaseInputs(); err != nil {
		e.cfgInputs = previous
		return
	}
	rekeyTargets(e, parsed, states)
}

// rekeyDiscovery files the test list found before the run under the discovery key the next
// run will compute, so learning new main-process inputs does not cost a second `vitest list`.
func (e *runEnv) rekeyDiscovery(parsed ParsedArgs) {
	if e.discovery == nil {
		return
	}
	newKey := computeDiscoveryKey(e, parsed, e.discovery.candidates)
	if newKey == e.discovery.key {
		return
	}
	if rec := e.store.LoadDiscovery(e.discovery.key); rec != nil {
		rec.Key = newKey
		_ = e.store.SaveDiscovery(*rec)
	}
}

func sameConfigInputs(a, b cache.ConfigInputs) bool {
	return a.EnvDir == b.EnvDir && stringSlicesEqual(a.Files, b.Files) && stringSlicesEqual(a.EnvKeys, b.EnvKeys)
}

// adoptConfigInputs stores the main-process inputs of the run that just finished when they
// differ from the record the base keys were computed with.
func (e *runEnv) adoptConfigInputs(reported reportMainInputs) error {
	rec := cache.ConfigInputs{
		Key:     e.cfgInputsKey,
		Files:   normalizeRepoPaths(e.proj.RepoRoot, reported.Files),
		EnvKeys: normalizeEnvKeys(reported.EnvKeys),
	}
	if reported.EnvDir != "" {
		rec.EnvDir = relSlashPath(e.proj.RepoRoot, resolveRealPath(reported.EnvDir))
	}
	if e.cfgInputs != nil && sameConfigInputs(rec, *e.cfgInputs) {
		return nil
	}
	if err := e.store.SaveConfigInputs(rec); err != nil {
		return err
	}
	e.cfgInputs = &rec
	return nil
}

// normalizeRepoPaths converts absolute paths to sorted, unique repo-relative slash paths.
// Paths outside the repo are kept (as "../..."): a config may import from anywhere.
func normalizeRepoPaths(repoRoot string, absPaths []string) []string {
	seen := make(map[string]struct{}, len(absPaths))
	out := make([]string, 0, len(absPaths))
	for _, p := range absPaths {
		rel := relSlashPath(repoRoot, resolveRealPath(p))
		if _, dup := seen[rel]; dup {
			continue
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
