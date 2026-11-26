package vitest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

type coverageFile struct {
	TestPath          string   `json:"testPath"`
	HasCoverage       *bool    `json:"hasCoverage,omitempty"`
	ExecutedFiles     []string `json:"executedFiles"`
	LoadedFiles       []string `json:"loadedFiles,omitempty"`
	FsReads           []string `json:"fsReads,omitempty"`
	EnvReads          []string `json:"envReads,omitempty"`
	UncacheableReason string   `json:"uncacheableReason,omitempty"`
}

type reportPayload struct {
	Modules     []reportModule    `json:"modules"`
	ImportGlobs json.RawMessage   `json:"importGlobs,omitempty"`
	MainInputs  *reportMainInputs `json:"mainInputs,omitempty"`
	// ModuleFacts maps a module to what its source statically implies (see module_facts.go).
	ModuleFacts       json.RawMessage `json:"moduleFacts,omitempty"`
	UncacheableReason string          `json:"uncacheableReason,omitempty"`
	NodeVersion       string          `json:"nodeVersion,omitempty"`
}

// reportMainInputs is what tc-reporter.mjs saw the Vite main process read (absolute paths).
type reportMainInputs struct {
	Files   []string `json:"files"`
	EnvDir  string   `json:"envDir"`
	EnvKeys []string `json:"envKeys"`
}

type reportModule struct {
	TestPath      string   `json:"testPath"`
	RelativeID    string   `json:"relativeId"`
	Passed        bool     `json:"passed"`
	State         string   `json:"state"`
	DurationMs    int64    `json:"durationMs"`
	TestsTotal    int      `json:"testsTotal"`
	TestsPassed   int      `json:"testsPassed"`
	TestsFailed   int      `json:"testsFailed"`
	TestsSkipped  int      `json:"testsSkipped"`
	FailedTests   []string `json:"failedTests,omitempty"`
	Logs          []string `json:"logs"`
	StaticImports []string `json:"staticImports"`
}

type persistResultsRequest struct {
	env        *runEnv
	parsed     ParsedArgs
	misses     []*targetState
	covDir     string
	reportPath string
	wallMs     int64
	runCount   int
	window     *runWindow
	stdout     io.Writer
	stderr     io.Writer
}

type saveModuleRequest struct {
	env       *runEnv
	parsed    ParsedArgs
	st        *targetState
	mod       reportModule
	covDir    string
	globIndex *importGlobIndex
	facts     *moduleFactsIndex
	shareMs   int64
	wallShare int64
	window    *runWindow
	stdout    io.Writer
}

type moduleDepsResult struct {
	deps              []string
	globs             []cache.GlobDep
	envVars           []string
	uncacheableReason string
	fallback          bool
}

func persistRunResults(req persistResultsRequest) (int, error) {
	data, err := os.ReadFile(req.reportPath)
	if err != nil {
		return 0, fmt.Errorf("read vitest report: %w", err)
	}
	var report reportPayload
	if err := json.Unmarshal(data, &report); err != nil {
		return 0, fmt.Errorf("parse vitest report: %w", err)
	}

	byPath := make(map[string]*targetState, len(req.misses))
	for _, st := range req.misses {
		byPath[st.absPath] = st
	}
	batch := moduleBatch{
		req:       req,
		shareMs:   req.wallMs / int64(maxInt(req.runCount, 1)),
		sumDurMs:  sumModuleDurations(report.Modules),
		globIndex: indexImportGlobs(report.ImportGlobs),
		facts:     indexModuleFacts(report.ModuleFacts),
	}
	batch.runReason = refreshInputsAfterRun(req, report)
	if batch.runReason == "" && hasRepeatedModule(report.Modules) {
		batch.runReason = reasonRepeatedFile
	}
	saved := 0

	for _, mod := range report.Modules {
		st, ok := byPath[resolveRealPath(mod.TestPath)]
		if !ok {
			// A file excluded by testcache.json: it ran, and its pass is never stored.
			req.env.traceUnstoredPass(mod.Passed)
			continue
		}
		cached, err := batch.persistModule(st, mod)
		if err != nil {
			return saved, err
		}
		if cached {
			saved++
		}
	}
	return saved, nil
}

func sumModuleDurations(mods []reportModule) int64 {
	var sum int64
	for _, m := range mods {
		sum += m.DurationMs
	}
	return sum
}

// wallShareOf splits the wall-clock time of the run between its files by their own test
// time: files run in parallel, so their durations add up to more than the run took.
func (b moduleBatch) wallShareOf(mod reportModule) int64 {
	if b.sumDurMs <= 0 {
		return b.shareMs
	}
	return b.req.wallMs * mod.DurationMs / b.sumDurMs
}

// moduleBatch is what every module of one Vitest run shares while its results are stored.
type moduleBatch struct {
	req       persistResultsRequest
	shareMs   int64
	sumDurMs  int64
	globIndex *importGlobIndex
	facts     *moduleFactsIndex
	// runReason is non-empty when nothing from this run may be cached.
	runReason string
}

// persistModule stores the result of one test file and reports whether its pass was cached.
func (b moduleBatch) persistModule(st *targetState, mod reportModule) (bool, error) {
	req := b.req
	if !mod.Passed {
		handleFailedModule(req, st, mod)
		return false, nil
	}
	if st.quarantined {
		return false, nil
	}
	if b.runReason != "" {
		req.env.traceUnstoredPass(true)
		_ = saveUncacheablePassedMeta(req.env, req.parsed, st, b.runReason)
		return false, nil
	}
	cached, err := savePassedModule(saveModuleRequest{
		env:       req.env,
		parsed:    req.parsed,
		st:        st,
		mod:       mod,
		covDir:    req.covDir,
		globIndex: b.globIndex,
		facts:     b.facts,
		shareMs:   b.shareMs,
		wallShare: b.wallShareOf(mod),
		window:    req.window,
		stdout:    req.stdout,
	})
	if err != nil {
		return false, err
	}
	req.env.traceUnstoredPass(!cached)
	return cached, nil
}

func handleFailedModule(req persistResultsRequest, st *targetState, mod reportModule) {
	if st.revalidating && st.hit != nil && st.hit.Passed {
		if err := quarantineStaleTest(quarantineRequest{
			env:         req.env,
			st:          st,
			failedTests: mod.FailedTests,
			stderr:      req.stderr,
		}); err != nil && req.stderr != nil {
			fmt.Fprintf(req.stderr, "tc: warning: failed to record quarantine for %s: %v\n", st.relPath, err)
		}
	}
	_ = saveFailedModuleMeta(req.env, req.parsed, st, readUncacheableReason(req.covDir, st.absPath))
}

func savePassedModule(req saveModuleRequest) (bool, error) {
	depRes := loadModuleDeps(loadModuleDepsRequest{
		repoRoot:      req.env.proj.RepoRoot,
		absTestPath:   req.st.absPath,
		covDir:        req.covDir,
		staticImports: req.mod.StaticImports,
		globIndex:     req.globIndex,
		facts:         req.facts,
	})
	if depRes.uncacheableReason != "" {
		return false, saveUncacheablePassedMeta(req.env, req.parsed, req.st, depRes.uncacheableReason)
	}
	req.st.fallbackDeps = depRes.fallback
	if depRes.fallback {
		fmt.Fprintf(req.stdout, "tc: coverage data missing for %s; using static import graph fallback\n", req.st.relPath)
	}
	depHashes, hashOK := req.env.hashDepPaths(depRes.deps)
	globHashes, globOK := req.env.hashGlobDeps(depRes.globs)
	if !hashOK || !globOK {
		return false, nil
	}
	if reason := req.window.rejectModule(req.env, moduleInputs{st: req.st, deps: depHashes, globs: globHashes}); reason != "" {
		return false, saveUncacheablePassedMeta(req.env, req.parsed, req.st, reason)
	}
	envPairs := fingerprint.LookupEnvPairs(depRes.envVars)
	fullKey := fingerprint.ComputeFullKeyWithInputs(fingerprint.FullKeyInputs{
		BaseKey: req.st.baseKey, Deps: depHashes, Globs: globHashes, Envs: envPairs,
	})
	if err := saveDepRecord(req, depRes); err != nil {
		return false, err
	}
	err := savePassedEntryAndMeta(savePassedMetaRequest{
		req: req, fullKey: fullKey, depHashes: depHashes, globHashes: globHashes, envPairs: envPairs,
	})
	return err == nil, err
}

func saveDepRecord(req saveModuleRequest, depRes moduleDepsResult) error {
	return req.env.store.SaveDeps(cache.DepRecord{
		BaseKey:      req.st.baseKey,
		TestFile:     relSlashPath(req.env.proj.RepoRoot, req.st.absPath),
		Deps:         depRes.deps,
		Globs:        depRes.globs,
		EnvVars:      depRes.envVars,
		FallbackDeps: depRes.fallback,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	})
}

type savePassedMetaRequest struct {
	req        saveModuleRequest
	fullKey    string
	depHashes  []cache.DepHash
	globHashes []cache.GlobHash
	envPairs   []fingerprint.EnvPair
}

func savePassedEntryAndMeta(in savePassedMetaRequest) error {
	req := in.req
	dur := maxInt64(req.mod.DurationMs, req.shareMs)
	if err := req.env.store.SaveEntry(cache.Entry{
		BaseKey:      req.st.baseKey,
		FullKey:      in.fullKey,
		Runner:       string(pkgVitest),
		TestFile:     req.st.relPath,
		Passed:       true,
		DurationMs:   dur,
		WallShareMs:  req.wallShare,
		TestsPassed:  req.mod.TestsPassed,
		TestsFailed:  req.mod.TestsFailed,
		TestsSkipped: req.mod.TestsSkipped,
		TestsTotal:   req.mod.TestsTotal,
		Output:       formatCapturedLogs(req.mod.Logs),
		FallbackDeps: req.st.fallbackDeps,
		Deps:         in.depHashes,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		return err
	}
	meta := buildTestMeta(req.env, req.parsed, req.st, true)
	meta.FullKey = in.fullKey
	meta.LastPassFullKey = in.fullKey
	meta.LastPassDeps = in.depHashes
	meta.LastPassGlobs = in.globHashes
	meta.LastPassDynamicEnv = toEnvSnapshots(in.envPairs)
	return req.env.store.SaveTestMeta(meta)
}

func saveUncacheablePassedMeta(env *runEnv, parsed ParsedArgs, st *targetState, reason string) error {
	meta := buildTestMeta(env, parsed, st, true)
	meta.UncacheableReason = reason
	return env.store.SaveTestMeta(meta)
}

func saveFailedModuleMeta(env *runEnv, parsed ParsedArgs, st *targetState, uncacheableReason string) error {
	meta := buildTestMeta(env, parsed, st, false)
	meta.UncacheableReason = uncacheableReason
	if prev, _ := env.store.LoadTestMeta(meta.TestFile); prev != nil {
		meta.LastPassFullKey = prev.LastPassFullKey
		meta.LastPassDeps = prev.LastPassDeps
		meta.LastPassGlobs = prev.LastPassGlobs
		meta.LastPassDynamicEnv = prev.LastPassDynamicEnv
	}
	return env.store.SaveTestMeta(meta)
}

func buildTestMeta(env *runEnv, parsed ParsedArgs, st *targetState, passed bool) cache.TestFileMeta {
	return cache.TestFileMeta{
		TestFile:      relSlashPath(env.proj.RepoRoot, st.absPath),
		BaseKey:       st.baseKey,
		Passed:        passed,
		UpdatedAt:     time.Now().UTC(),
		RunnerVersion: env.proj.VitestVersion,
		NodeVersion:   env.nodeVer,
		ConfigHash:    env.cfgHash,
		ProjectFiles:  env.proj.ProjectFiles,
		Args:          append([]string(nil), parsed.CacheableFlags...),
		TestFileHash:  st.fileHash,
		AllowEnv:      toEnvSnapshots(env.envPairs),
	}
}

func toEnvSnapshots(pairs []fingerprint.EnvPair) []cache.EnvSnapshot {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]cache.EnvSnapshot, len(pairs))
	for i, p := range pairs {
		out[i] = cache.EnvSnapshot{Key: p.Key, Value: p.Value, Set: p.Set}
	}
	return out
}

func formatCapturedLogs(logs []string) string {
	if len(logs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l)
		if !strings.HasSuffix(l, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
