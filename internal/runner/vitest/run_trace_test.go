package vitest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// runTraced runs tc vitest with a fresh trace and returns the exit code and what was traced.
func runTraced(t *testing.T, opts runner.RunOptions) (int, runlog.Record) {
	t.Helper()
	trace := runlog.NewTrace(runlog.Record{Mode: runlog.ModeCache})
	opts.Trace = trace
	code, _, _ := runVitestWithOpts(t, opts)
	return code, trace.CopyRecord()
}

func TestRunTraceRecordsCountsPhasesAndExit(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	writeFixtureFile(t, filepath.Join(projDir, "testcache.json"), `{"exclude":["tests/b.test.ts"]}`)

	code, cold := runTraced(t, runner.RunOptions{WorkDir: projDir, NoRevalidate: true})
	if code != 0 {
		t.Fatalf("cold run failed with %d", code)
	}
	wantFiles := runlog.FileCounts{Discovered: 2, Cached: 0, Run: 2, Uncacheable: 1}
	if cold.Files != wantFiles {
		t.Fatalf("cold run: expected %+v (b.test.ts is excluded, so its pass is not stored), got %+v", wantFiles, cold.Files)
	}
	if cold.VitestExit == nil || *cold.VitestExit != 0 || cold.PhasesMs.Run <= 0 {
		t.Fatalf("cold run must record the vitest exit code and run time: %+v", cold)
	}
	if cold.RepoID == "" || cold.RepoRoot != fingerprint.FindRepoRoot(projDir) || cold.Mode != runlog.ModeCache {
		t.Fatalf("unexpected repo or mode: %+v", cold)
	}
	if len(cold.Anomalies) != 0 {
		t.Fatalf("a clean run has no anomalies: %+v", cold.Anomalies)
	}

	_, warm := runTraced(t, runner.RunOptions{WorkDir: projDir, NoRevalidate: true, Args: []string{"tests/a.test.ts"}})
	if warm.Files != (runlog.FileCounts{Discovered: 1, Cached: 1}) || warm.VitestExit != nil || warm.SavedS <= 0 {
		t.Fatalf("warm run must be a pure hit with saved time and no vitest: %+v", warm)
	}
}

func TestRunTraceFlagsStaleEntryThenQuarantined(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	t.Setenv(envCounterDir, t.TempDir())
	flakyRel := string(targetFlakyTest)
	writeFlakyCounterTest(t, filepath.Join(projDir, flakyRel))

	if code, first := runTraced(t, runner.RunOptions{WorkDir: projDir, Args: []string{flakyRel}}); code != 0 || len(first.Anomalies) != 0 {
		t.Fatalf("expected a clean cold pass, code=%d anomalies=%+v", code, first.Anomalies)
	}

	// The cached pass fails when re-run with identical inputs.
	code, stale := runTraced(t, runner.RunOptions{
		WorkDir: projDir, Args: []string{flakyRel}, RandFloat64: func() float64 { return 0 },
	})
	if code == 0 || !stale.HasAnomaly(runlog.AnomalyStaleEntry) {
		t.Fatalf("expected stale_entry on a failed revalidation, code=%d anomalies=%+v", code, stale.Anomalies)
	}
	if stale.HasAnomaly(runlog.AnomalyQuarantined) {
		t.Fatalf("quarantined is for files already under quarantine, got %+v", stale.Anomalies)
	}
	if !strings.Contains(stale.Anomalies[0].Detail, flakyRel) {
		t.Fatalf("the detail must name the file: %+v", stale.Anomalies[0])
	}

	// From now on the file runs uncached on every run.
	_, held := runTraced(t, runner.RunOptions{WorkDir: projDir, Args: []string{flakyRel}, NoRevalidate: true})
	if !held.HasAnomaly(runlog.AnomalyQuarantined) || held.Files.Quarantined != 1 || held.Files.Run != 1 {
		t.Fatalf("expected quarantined with 1 file, got files=%+v anomalies=%+v", held.Files, held.Anomalies)
	}
}

func TestVerifyTraceFlagsStaleEntry(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	t.Setenv(envCounterDir, t.TempDir())
	flakyRel := string(targetFlakyTest)
	writeFlakyCounterTest(t, filepath.Join(projDir, flakyRel))
	assertVitestSummary(t, projDir, "tc: 3 files, 0 cached, 3 run")

	trace := runlog.NewTrace(runlog.Record{Mode: runlog.ModeVerify})
	_, code, _, _ := runVerifyWithOpts(t, runner.RunOptions{WorkDir: projDir, Trace: trace})
	rec := trace.CopyRecord()
	if code == 0 || !rec.HasAnomaly(runlog.AnomalyStaleEntry) {
		t.Fatalf("verify must flag the stale entry, code=%d anomalies=%+v", code, rec.Anomalies)
	}
	if rec.Files.Discovered != 3 || rec.Files.Run != 3 || rec.VitestExit == nil || rec.Mode != runlog.ModeVerify {
		t.Fatalf("unexpected verify record: %+v", rec)
	}
}

func TestRunTraceFlagsCorruptCacheData(t *testing.T) {
	projDir, cacheBase := setupFixtureCopy(t)
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")
	corruptDirFiles(t, filepath.Join(singleRepoCacheDir(t, cacheBase), "entries"), []byte("{truncated-entry"))

	code, rec := runTraced(t, runner.RunOptions{WorkDir: projDir, NoRevalidate: true})
	if code != 0 || rec.Files.Run != 2 {
		t.Fatalf("corrupt entries must be re-run, code=%d files=%+v", code, rec.Files)
	}
	if !rec.HasAnomaly(runlog.AnomalyInternalError) || !strings.Contains(rec.Anomalies[0].Detail, "corrupt cache file removed: entries/") {
		t.Fatalf("expected internal_error for corrupt cache data, got %+v", rec.Anomalies)
	}
}

func TestRejectNodeMismatchFlagsTheRun(t *testing.T) {
	trace := runlog.NewTrace(runlog.Record{})
	env := &runEnv{nodeVer: "v24.1.0", baseCacheDir: t.TempDir(), trace: trace}
	if reason := env.rejectNodeMismatch("v24.1.0"); reason != "" || len(trace.CopyRecord().Anomalies) != 0 {
		t.Fatalf("matching versions are fine, got %q", reason)
	}
	if reason := env.rejectNodeMismatch("v22.0.0"); !strings.Contains(reason, "node version mismatch") {
		t.Fatalf("expected a mismatch reason, got %q", reason)
	}
	rec := trace.CopyRecord()
	if !rec.HasAnomaly(runlog.AnomalyNodeVersionMismatch) || !strings.Contains(rec.Anomalies[0].Detail, "v22.0.0") {
		t.Fatalf("expected node_version_mismatch, got %+v", rec.Anomalies)
	}
}

func TestRunPlainRunsVitestOnceWithoutTheCache(t *testing.T) {
	projDir, cacheBase := setupFixtureCopy(t)
	var out, errBuf bytes.Buffer
	trace := runlog.NewTrace(runlog.Record{Mode: runlog.ModeDisabled})
	code, err := RunPlain(context.Background(), runner.RunOptions{
		WorkDir: projDir, Args: []string{string(tcFlagNoCache), "tests/a.test.ts"}, Stdout: &out, Stderr: &errBuf, Trace: trace,
	})
	if err != nil || code != 0 {
		t.Fatalf("plain run failed: code=%d err=%v\n%s", code, err, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 passed") || strings.Contains(out.String(), "tc:") {
		t.Fatalf("expected a plain single vitest run of one file:\n%s", out.String())
	}
	if rec := trace.CopyRecord(); rec.VitestExit == nil || *rec.VitestExit != 0 {
		t.Fatalf("the vitest exit code must be traced: %+v", rec)
	}
	if items, _ := os.ReadDir(cacheBase); len(items) != 0 {
		t.Fatalf("a plain run must not touch the cache, found %d entries", len(items))
	}
}

func TestVitestChildEnvDisablesNestedIntegrations(t *testing.T) {
	if !strings.Contains(strings.Join(buildChildEnv(), "\n"), "\nTC_DISABLE=1") {
		t.Fatal("the Vitest child must see TC_DISABLE=1 so a nested package script cannot queue behind this run")
	}
}

func TestParseArgsStopsAtTheTerminator(t *testing.T) {
	p := ParseArgs([]string{"run", "tests/a.test.ts", "--", "tests/b.test.ts", "--watch"})
	if p.BypassCache || len(p.Positionals) != 1 || p.Positionals[0] != "tests/a.test.ts" || len(p.ForwardFlags) != 0 {
		t.Fatalf("vitest ignores everything after a bare --, so must tc: %+v", p)
	}
}

func TestLocateBinaryFindsTheProjectVitest(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	if bin := LocateBinary(filepath.Join(projDir, "tests")); !strings.HasSuffix(bin, "vitest.mjs") {
		t.Fatalf("expected the project vitest from a subdirectory, got %q", bin)
	}
}
