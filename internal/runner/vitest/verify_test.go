package vitest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/runner"
)

func TestVitestVerifyAgreeingAndNewFailure(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)

	// Cache only a.test.ts; leave b.test.ts uncached.
	assertVitestSummary(t, projDir, "tc: 1 file, 0 cached, 1 run", "tests/a.test.ts")

	rep, code, out, _ := runVerifyWithOpts(t, runner.RunOptions{WorkDir: projDir})
	if code != 0 {
		t.Fatalf("expected verify exit 0, got %d out:\n%s", code, out)
	}
	if rep.CheckedFiles != 2 || rep.AgreeingEntries != 1 || len(rep.StaleFiles) != 0 || len(rep.NewFailures) != 0 {
		t.Fatalf("unexpected verify report: %+v", rep)
	}
	if !strings.Contains(out, "2 checked, 1 cached agreeing, 0 stale, 0 new failures") {
		t.Fatalf("unexpected verify summary output:\n%s", out)
	}

	// Verify runs with no cache, so b.test.ts must still be a miss on next tc vitest.
	assertVitestSummary(t, projDir, "tc: 1 file, 0 cached, 1 run", "tests/b.test.ts")

	// Break b.test.ts (test file changes -> new key without an entry) -> counted as new failure, not stale.
	bPath := filepath.Join(projDir, "tests", "b.test.ts")
	origB, err := os.ReadFile(bPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, bPath, strings.ReplaceAll(string(origB), "toBe(12)", "toBe(999)"))

	repFail, codeFail, outFail, _ := runVerifyWithOpts(t, runner.RunOptions{WorkDir: projDir})
	if codeFail == 0 {
		t.Fatalf("expected non-zero exit on failure")
	}
	if repFail.CheckedFiles != 2 || repFail.AgreeingEntries != 1 || len(repFail.StaleFiles) != 0 {
		t.Fatalf("expected 1 agreeing and 0 stale, got %+v", repFail)
	}
	if len(repFail.NewFailures) != 1 || repFail.NewFailures[0] != "tests/b.test.ts" {
		t.Fatalf("expected new failure for tests/b.test.ts, got %+v\nout:\n%s", repFail, outFail)
	}
	if q, err := New().ListQuarantine(projDir); err != nil || len(q) != 0 {
		t.Fatalf("new failure must not be quarantined, got %+v err=%v", q, err)
	}
}

func TestVitestVerifyQuarantinesStaleAndClearReleases(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	counterDir := t.TempDir()
	t.Setenv(envCounterDir, counterDir)

	flakyRel := string(targetFlakyTest)
	writeFlakyCounterTest(t, filepath.Join(projDir, flakyRel))

	// Cold run caches a.test.ts, b.test.ts, and flaky.test.ts.
	assertVitestSummary(t, projDir, "tc: 3 files, 0 cached, 3 run")

	// Verify runs all 3 uncached: a & b agree; flaky fails with same key -> deleted & quarantined.
	rep, code, out, errOut := runVerifyWithOpts(t, runner.RunOptions{WorkDir: projDir})
	if code == 0 {
		t.Fatalf("expected verify exit non-zero on stale entry, out:\n%s", out)
	}
	if rep.CheckedFiles != 3 || rep.AgreeingEntries != 2 || len(rep.NewFailures) != 0 {
		t.Fatalf("unexpected verify report: %+v", rep)
	}
	if len(rep.StaleFiles) != 1 || rep.StaleFiles[0] != flakyRel {
		t.Fatalf("expected stale %s, got %+v", flakyRel, rep.StaleFiles)
	}
	wantWarn := "tc: " + flakyRel + " " + string(quarantineReasonText)
	if !strings.Contains(errOut, wantWarn) {
		t.Fatalf("expected quarantine warning %q in stderr:\n%s", wantWarn, errOut)
	}

	r := New()
	qEntries, err := r.ListQuarantine(projDir)
	if err != nil || len(qEntries) != 1 || qEntries[0].TestFile != flakyRel {
		t.Fatalf("expected %s in quarantine, got %+v err=%v", flakyRel, qEntries, err)
	}
	assertExplainContains(t, projDir, flakyRel, "quarantined", "likely flaky or depends on an untracked input")

	// Even with counter cleared so it passes, quarantined file is never cached until cleared.
	_ = os.Remove(filepath.Join(counterDir, counterMarkerName))
	codeRun, outRun, _ := runVitestWithOpts(t, runner.RunOptions{
		WorkDir: projDir, Args: []string{flakyRel}, NoRevalidate: true,
	})
	if codeRun != 0 || !strings.Contains(outRun, summaryOneRun) {
		t.Fatalf("quarantined test must run uncached, code=%d out:\n%s", codeRun, outRun)
	}

	if _, ok, err := r.ClearQuarantine(projDir, flakyRel); err != nil || !ok {
		t.Fatalf("ClearQuarantine ok=%v err=%v", ok, err)
	}
	_ = os.Remove(filepath.Join(counterDir, counterMarkerName))
	assertVitestSummary(t, projDir, summaryOneRun, flakyRel, string(tcFlagNoRevalidate))
	assertVitestSummary(t, projDir, summaryOneHit, flakyRel, string(tcFlagNoRevalidate))
}

func runVerifyWithOpts(t *testing.T, opts runner.RunOptions) (VerifyReport, int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	opts.Stdout = &out
	opts.Stderr = &errBuf
	opts.NoCache = true
	rep, code, err := New().Verify(context.Background(), opts)
	if err != nil {
		t.Fatalf("Verify: %v\nstderr: %s", err, errBuf.String())
	}
	return rep, code, out.String(), errBuf.String()
}
