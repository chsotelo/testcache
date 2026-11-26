package vitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type loadModuleDepsRequest struct {
	repoRoot      string
	absTestPath   string
	covDir        string
	staticImports []string
	globIndex     *importGlobIndex
	facts         *moduleFactsIndex
}

// reasonHooksMissing explains a pass that ran without tc's worker hooks: nothing it read,
// spawned or connected to was observed, so it cannot be cached.
const reasonHooksMissing = "tc hooks did not load for this file"

// reasonRepeatedFile explains a run in which Vitest reported one test file more than once
// (the file belongs to several projects): one cache entry cannot stand for several results.
const reasonRepeatedFile = "a test file ran more than once in the same run (test.projects is not supported)"

// hasRepeatedModule reports whether any test file appears twice in a Vitest report. It backs
// up the config check in tc-reporter.mjs without depending on how Vitest exposes projects.
func hasRepeatedModule(modules []reportModule) bool {
	seen := make(map[string]struct{}, len(modules))
	for _, mod := range modules {
		path := resolveRealPath(mod.TestPath)
		if _, dup := seen[path]; dup {
			return true
		}
		seen[path] = struct{}{}
	}
	return false
}

func readUncacheableReason(covDir, absTestPath string) string {
	cf, _ := readCoverageFile(covDir, absTestPath)
	return cf.UncacheableReason
}

func loadModuleDeps(req loadModuleDepsRequest) moduleDepsResult {
	cf, ok := readCoverageFile(req.covDir, req.absTestPath)
	if !ok {
		return moduleDepsResult{uncacheableReason: reasonHooksMissing}
	}
	hasCov := cf.HasCoverage == nil || *cf.HasCoverage
	importers, loaded := cf.ExecutedFiles, cf.LoadedFiles
	if !hasCov {
		importers, loaded = req.staticImports, req.staticImports
	}
	raw := make([]string, 0, len(importers)+len(cf.FsReads)+1)
	raw = append(raw, importers...)
	raw = append(raw, cf.FsReads...)
	raw = append(raw, defaultSnapshotAbsPath(req.absTestPath))
	raw = append(raw, req.facts.collectDeps(append([]string{req.absTestPath}, loaded...))...)
	globs := collectTestGlobs(collectGlobsRequest{
		repoRoot: req.repoRoot, absTestPath: req.absTestPath,
		importers: importers, globIndex: req.globIndex,
	})
	return moduleDepsResult{
		deps: normalizeDeps(req.repoRoot, raw), globs: globs,
		envVars: normalizeEnvKeys(cf.EnvReads), uncacheableReason: cf.UncacheableReason, fallback: !hasCov,
	}
}

func readCoverageFile(covDir, absTestPath string) (coverageFile, bool) {
	covPath := filepath.Join(covDir, hashPathHex(absTestPath)+".json")
	data, err := os.ReadFile(covPath)
	if err != nil {
		return coverageFile{}, false
	}
	var cf coverageFile
	return cf, json.Unmarshal(data, &cf) == nil
}

func defaultSnapshotAbsPath(absTestPath string) string {
	return filepath.Join(filepath.Dir(absTestPath), "__snapshots__", filepath.Base(absTestPath)+".snap")
}

func normalizeEnvKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func normalizeDeps(repoRoot string, absPaths []string) []string {
	seen := make(map[string]struct{}, len(absPaths))
	out := make([]string, 0, len(absPaths))
	for _, p := range absPaths {
		rp := resolveRealPath(p)
		rel := relSlashPath(repoRoot, rp)
		if strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
