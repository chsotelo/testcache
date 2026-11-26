package vitest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatOutcomeCounts(t *testing.T) {
	cases := map[string]struct {
		in   outcomeCounts
		want string
	}{
		"all passed":        {outcomeCounts{passed: 156, total: 156}, "156 passed (156)"},
		"with failures":     {outcomeCounts{failed: 1, passed: 155, total: 156}, "1 failed | 155 passed (156)"},
		"with skipped":      {outcomeCounts{passed: 9, skipped: 2, total: 11}, "9 passed | 2 skipped (11)"},
		"only failures":     {outcomeCounts{failed: 3, total: 3}, "3 failed (3)"},
		"nothing collected": {outcomeCounts{}, "0 passed (0)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := formatOutcomeCounts(tc.in); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// The totals cover the whole selection: an agent that greps "Test Files" must not see only
// the files Vitest executed and conclude the cached ones went missing.
func TestPrintCombinedTotals(t *testing.T) {
	cached := resultTally{known: true, files: 144, tests: 1894, passedTests: 1890, skippedTests: 4}
	ran := resultTally{known: true, files: 12, failedFiles: 1, tests: 111, passedTests: 109, failedTests: 2}
	var out bytes.Buffer
	printCombinedTotals(&out, cached, ran)
	want := "\n Test Files  1 failed | 155 passed (156) [tc total: 144 cached + 12 run]\n" +
		"      Tests  2 failed | 1999 passed | 4 skipped (2005) [tc total]\n"
	if out.String() != want {
		t.Fatalf("want:\n%q\ngot:\n%q", want, out.String())
	}

	for name, pair := range map[string][2]resultTally{
		"nothing cached":       {{known: true}, ran},
		"unknown run outcomes": {cached, {}},
	} {
		out.Reset()
		printCombinedTotals(&out, pair[0], pair[1])
		if out.Len() != 0 {
			t.Fatalf("%s: no total may be printed, got %q", name, out.String())
		}
	}
}

func TestTallyRunReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	report := `{"modules":[
	 {"testPath":"/p/a.test.ts","passed":true,"testsTotal":3,"testsPassed":2,"testsSkipped":1},
	 {"testPath":"/p/b.test.ts","passed":false,"testsTotal":2,"testsPassed":1,"testsFailed":1}]}`
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tallyRunReport(path)
	want := resultTally{known: true, files: 2, failedFiles: 1, tests: 5, passedTests: 3, failedTests: 1, skippedTests: 1}
	if got != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
	if missing := tallyRunReport(filepath.Join(t.TempDir(), "none.json")); missing.known {
		t.Fatalf("a missing report must not be reported as known: %+v", missing)
	}
}

// A partly cached run ends with totals for everything selected and says what "(cached)" means.
func TestVitestPartialHitPrintsTotalsAndHint(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")

	full := assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")
	if !strings.Contains(full, " Test Files  2 passed (2) [tc total: 2 cached + 0 run]") || !strings.Contains(full, cachedHint) {
		t.Fatalf("a full hit must print the totals and the hint:\n%s", full)
	}

	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"export function add(a: number, b: number): number {\n  return a + b + 0;\n}\n")
	partial := assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")
	for _, want := range []string{
		" Test Files  2 passed (2) [tc total: 1 cached + 1 run]",
		"      Tests  2 passed (2) [tc total]",
		cachedHint,
	} {
		if !strings.Contains(partial, want) {
			t.Fatalf("missing %q in:\n%s", want, partial)
		}
	}
	if strings.Index(partial, "[tc total") > strings.Index(partial, "tc: 2 files") {
		t.Fatalf("the totals must come before the tc line:\n%s", partial)
	}

	cold := assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run", "--no-cache")
	if strings.Contains(cold, "[tc total") || strings.Contains(cold, cachedHint) {
		t.Fatalf("a run with nothing cached must look like plain Vitest plus the tc line:\n%s", cold)
	}
}

func TestWallSharesAddUpToTheRunTime(t *testing.T) {
	mods := []reportModule{{DurationMs: 300}, {DurationMs: 100}, {DurationMs: 600}}
	b := moduleBatch{req: persistResultsRequest{wallMs: 400}, sumDurMs: sumModuleDurations(mods), shareMs: 133}
	var total int64
	for _, m := range mods {
		total += b.wallShareOf(m)
	}
	if total != 400 {
		t.Fatalf("shares add up to %dms, the run took 400ms", total)
	}
	if got := savedByHit(0, 250); got != 250 {
		t.Fatalf("an entry without a share must fall back to its own duration, got %d", got)
	}
}
