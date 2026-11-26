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

const (
	summaryTwoHits    string        = "tc: 2 files, 2 cached, 0 run"
	summaryTwoRuns    string        = "tc: 2 files, 0 cached, 2 run"
	targetFlakyTest   fixtureTarget = "tests/flaky.test.ts"
	envCounterDir     string        = "TC_UNTRACKED_COUNTER_DIR"
	counterMarkerName string        = "ran.txt"
)

func TestShouldRevalidateAndCryptoRand(t *testing.T) {
	zeroRand := func() float64 { return 0.0 }
	highRand := func() float64 { return 0.999 }

	if shouldRevalidate(0, zeroRand) {
		t.Fatalf("rate 0 must never revalidate")
	}
	if !shouldRevalidate(1, highRand) {
		t.Fatalf("rate 1 must always revalidate")
	}
	if !shouldRevalidate(0.25, func() float64 { return 0.1 }) {
		t.Fatalf("expected 0.1 < 0.25 to revalidate")
	}
	if shouldRevalidate(0.25, func() float64 { return 0.5 }) {
		t.Fatalf("expected 0.5 >= 0.25 not to revalidate")
	}
	for i := 0; i < 10; i++ {
		v, err := cryptoRandFloat64()
		if err != nil {
			t.Fatalf("cryptoRandFloat64: %v", err)
		}
		if v < 0 || v >= 1 {
			t.Fatalf("cryptoRandFloat64 out of [0,1): %v", v)
		}
	}
}

func TestParseArgsNoRevalidate(t *testing.T) {
	p := ParseArgs([]string{"--no-revalidate", "tests/a.test.ts"})
	if !p.NoRevalidate {
		t.Fatalf("expected NoRevalidate=true")
	}
	if len(p.ForwardFlags) != 0 || len(p.CacheableFlags) != 0 {
		t.Fatalf("expected --no-revalidate stripped from Forward/Cacheable flags: %+v", p)
	}
}

func TestVitestForcedRevalidationRerunsHit(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)

	// Cold run populates cache.
	assertVitestSummary(t, projDir, summaryTwoRuns)

	// Normal warm hit (rate=0.02, rand returns 0.99 -> no revalidation).
	code, out, errOut := runVitestWithOpts(t, runner.RunOptions{
		WorkDir:     projDir,
		RandFloat64: func() float64 { return 0.99 },
	})
	if code != 0 || !strings.Contains(out, summaryTwoHits) {
		t.Fatalf("expected warm hit code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}

	// Forced revalidation via seeded rand source re-runs both hits and keeps cache warm.
	code, out, errOut = runVitestWithOpts(t, runner.RunOptions{
		WorkDir:     projDir,
		RandFloat64: func() float64 { return 0.0 },
	})
	if code != 0 || !strings.Contains(out, summaryTwoRuns) {
		t.Fatalf("expected forced revalidation to re-run hits code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}

	// Config revalidateRate=1.0 forces revalidation unless --no-revalidate is passed.
	writeFixtureFile(t, filepath.Join(projDir, "testcache.json"), `{"revalidateRate":1.0}`)
	code, out, errOut = runVitestWithOpts(t, runner.RunOptions{WorkDir: projDir})
	if code != 0 || !strings.Contains(out, summaryTwoRuns) {
		t.Fatalf("expected revalidateRate=1.0 to re-run hits code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}

	code, out, errOut = runVitestWithOpts(t, runner.RunOptions{
		WorkDir: projDir,
		Args:    []string{string(tcFlagNoRevalidate)},
	})
	if code != 0 || !strings.Contains(out, summaryTwoHits) {
		t.Fatalf("expected --no-revalidate to skip sampling code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}
}

func TestVitestRevalidationQuarantineAndClear(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	counterDir := t.TempDir()
	t.Setenv(envCounterDir, counterDir)

	flakyRel := string(targetFlakyTest)
	writeFlakyCounterTest(t, filepath.Join(projDir, flakyRel))

	// 1. First run passes (counter file created outside repo) and caches.
	code, out, _ := runVitestWithOpts(t, runner.RunOptions{
		WorkDir: projDir,
		Args:    []string{flakyRel},
	})
	if code != 0 || !strings.Contains(out, summaryOneRun) {
		t.Fatalf("expected cold pass code=%d out:\n%s", code, out)
	}

	// 2. Forced revalidation re-runs with identical inputs, fails, and quarantines.
	code, out, errOut := runVitestWithOpts(t, runner.RunOptions{
		WorkDir:     projDir,
		Args:        []string{flakyRel},
		RandFloat64: func() float64 { return 0.0 },
	})
	if code == 0 {
		t.Fatalf("expected revalidation failure, got code=0 out:\n%s", out)
	}
	wantWarn := "tc: " + flakyRel + " " + string(quarantineReasonText)
	if !strings.Contains(errOut, wantWarn) {
		t.Fatalf("expected quarantine warning %q in stderr:\n%s", wantWarn, errOut)
	}

	// 3. Quarantine list records date and failing test name; explain reports quarantined.
	r := New()
	qEntries, err := r.ListQuarantine(projDir)
	if err != nil || len(qEntries) != 1 {
		t.Fatalf("expected 1 quarantine entry, got=%+v err=%v", qEntries, err)
	}
	if qEntries[0].TestFile != flakyRel || qEntries[0].Date == "" || len(qEntries[0].FailedTests) == 0 {
		t.Fatalf("unexpected quarantine entry: %+v", qEntries[0])
	}
	if !strings.Contains(strings.Join(qEntries[0].FailedTests, ","), "passes only on first run") {
		t.Fatalf("expected failing test name recorded, got %+v", qEntries[0].FailedTests)
	}
	assertExplainContains(t, projDir, flakyRel, "quarantined", "likely flaky or depends on an untracked input")

	// 4. Even after counter is removed so the test passes, quarantined file is never cached.
	for i := 0; i < 2; i++ {
		_ = os.Remove(filepath.Join(counterDir, counterMarkerName))
		code, out, _ = runVitestWithOpts(t, runner.RunOptions{
			WorkDir:      projDir,
			Args:         []string{flakyRel},
			NoRevalidate: true,
		})
		if code != 0 || !strings.Contains(out, summaryOneRun) {
			t.Fatalf("quarantined run %d must never cache, code=%d out:\n%s", i+1, code, out)
		}
	}

	// 5. Clear quarantine releases the file so subsequent passes cache normally.
	if _, ok, err := r.ClearQuarantine(projDir, flakyRel); err != nil || !ok {
		t.Fatalf("ClearQuarantine ok=%v err=%v", ok, err)
	}
	_ = os.Remove(filepath.Join(counterDir, counterMarkerName))
	code, out, _ = runVitestWithOpts(t, runner.RunOptions{
		WorkDir:      projDir,
		Args:         []string{flakyRel},
		NoRevalidate: true,
	})
	if code != 0 || !strings.Contains(out, summaryOneRun) {
		t.Fatalf("post-clear run should execute and cache, code=%d out:\n%s", code, out)
	}
	code, out, _ = runVitestWithOpts(t, runner.RunOptions{
		WorkDir:      projDir,
		Args:         []string{flakyRel},
		NoRevalidate: true,
	})
	if code != 0 || !strings.Contains(out, summaryOneHit) {
		t.Fatalf("expected post-clear warm cache hit, code=%d out:\n%s", code, out)
	}
}

func runVitestWithOpts(t *testing.T, opts runner.RunOptions) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	opts.Stdout = &out
	opts.Stderr = &errBuf
	code, err := New().RunTests(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunTests: %v\nstderr: %s", err, errBuf.String())
	}
	return code, out.String(), errBuf.String()
}

func writeFlakyCounterTest(t *testing.T, dst string) {
	t.Helper()
	src := `import fs from 'node:fs';
import path from 'node:path';
import { expect, it } from 'vitest';

it('passes only on first run', () => {
  const dir = process.env.TC_UNTRACKED_COUNTER_DIR;
  if (!dir) throw new Error('missing TC_UNTRACKED_COUNTER_DIR');
  const marker = path.join(dir, 'ran.txt');
  const exists = fs.existsSync(marker);
  if (!exists) {
    fs.writeFileSync(marker, '1');
  }
  expect(exists).toBe(false);
});
`
	writeFixtureFile(t, dst, src)
}
