package vitest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
)

// cachedHint is printed after every run that replayed something. Whoever reads the output,
// often an agent that has never heard of tc, must be able to tell a replay from a skipped
// test and know how to force a real run without hunting for a way around tc.
const cachedHint = `tc: "(cached)" = that test file passed before with identical inputs (the test, every source file it executed, config, env) and its result was replayed; nothing was skipped. Real run: add --no-cache (or TC_DISABLE=1). Reason per file: tc explain <file>.`

type runSummary struct {
	totalFiles  int
	cachedFiles int
	runFiles    int
	savedMs     int64
}

// resultTally counts test files and tests by outcome. known is false when the numbers could
// not be established, in which case no total is printed rather than a wrong one.
type resultTally struct {
	known        bool
	files        int
	failedFiles  int
	tests        int
	passedTests  int
	failedTests  int
	skippedTests int
}

func (t resultTally) plus(o resultTally) resultTally {
	return resultTally{
		known: t.known && o.known, files: t.files + o.files, failedFiles: t.failedFiles + o.failedFiles,
		tests: t.tests + o.tests, passedTests: t.passedTests + o.passedTests,
		failedTests: t.failedTests + o.failedTests, skippedTests: t.skippedTests + o.skippedTests,
	}
}

// tallyRunReport counts what the Vitest process just ran, from the report tc-reporter.mjs wrote.
func tallyRunReport(reportPath string) resultTally {
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return resultTally{}
	}
	var report reportPayload
	if err := json.Unmarshal(data, &report); err != nil {
		return resultTally{}
	}
	tally := resultTally{known: true}
	for _, mod := range report.Modules {
		tally.files++
		if !mod.Passed {
			tally.failedFiles++
		}
		tally.tests += mod.TestsTotal
		tally.passedTests += mod.TestsPassed
		tally.failedTests += mod.TestsFailed
		tally.skippedTests += mod.TestsSkipped
	}
	return tally
}

// savedByHit is the wall-clock time a cached file saves: its share of the run that stored
// it, or its own test time for an entry stored before shares were recorded.
func savedByHit(wallShareMs, durationMs int64) int64 {
	if wallShareMs > 0 {
		return wallShareMs
	}
	return durationMs
}

// replayCachedHits prints every cached file and returns the time saved and what was replayed.
func replayCachedHits(w io.Writer, hits []*targetState) (int64, resultTally) {
	tally := resultTally{known: true}
	if len(hits) == 0 {
		return 0, tally
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].relPath < hits[j].relPath })
	var savedMs int64
	for _, st := range hits {
		e := st.hit
		savedMs += savedByHit(e.WallShareMs, e.DurationMs)
		tally.files++
		tally.tests += e.TestsTotal
		tally.passedTests += e.TestsPassed
		tally.skippedTests += e.TestsSkipped
		if e.Output != "" {
			fmt.Fprint(w, e.Output)
		}
		fmt.Fprintln(w, formatCachedLine(st.relPath, e))
	}
	return savedMs, tally
}

// printCombinedTotals prints Vitest-style totals for the whole selection, cached plus run.
// Vitest's own summary, printed earlier, only counts the files it executed: read alone it
// looks as if the cached files had gone missing.
func printCombinedTotals(w io.Writer, cached, ran resultTally) {
	if cached.files == 0 {
		return
	}
	total := cached.plus(ran)
	if !total.known {
		return
	}
	fmt.Fprintf(w, "\n Test Files  %s [tc total: %d cached + %d run]\n",
		formatOutcomeCounts(outcomeCounts{failed: total.failedFiles, passed: total.files - total.failedFiles, total: total.files}),
		cached.files, ran.files)
	fmt.Fprintf(w, "      Tests  %s [tc total]\n", formatOutcomeCounts(outcomeCounts{
		failed: total.failedTests, passed: total.passedTests, skipped: total.skippedTests, total: total.tests,
	}))
}

type outcomeCounts struct {
	failed, passed, skipped, total int
}

// formatOutcomeCounts renders counts the way Vitest does: "1 failed | 9 passed | 2 skipped (12)".
func formatOutcomeCounts(c outcomeCounts) string {
	var parts []string
	if c.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", c.failed))
	}
	if c.passed > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d passed", c.passed))
	}
	if c.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", c.skipped))
	}
	return fmt.Sprintf("%s (%d)", strings.Join(parts, " | "), c.total)
}

func formatCachedLine(relPath string, e *cache.Entry) string {
	unit := "tests"
	if e.TestsTotal == 1 {
		unit = "test"
	}
	suffix := ""
	if e.FallbackDeps {
		suffix = " [fallback: static import graph]"
	}
	return fmt.Sprintf(" ✓ %s (%d %s) (cached)%s", relPath, e.TestsTotal, unit, suffix)
}

func printRunSummary(w io.Writer, s runSummary) {
	fileWord := "files"
	if s.totalFiles == 1 {
		fileWord = "file"
	}
	savedSec := float64(s.savedMs) / 1000.0
	if s.savedMs > 0 && s.savedMs < 1000 {
		fmt.Fprintf(w, "tc: %d %s, %d cached, %d run, saved %.2fs\n", s.totalFiles, fileWord, s.cachedFiles, s.runFiles, savedSec)
		return
	}
	fmt.Fprintf(w, "tc: %d %s, %d cached, %d run, saved %.1fs\n", s.totalFiles, fileWord, s.cachedFiles, s.runFiles, savedSec)
}

// printCachedHint explains "(cached)" whenever the run replayed at least one file.
func printCachedHint(w io.Writer, cachedFiles int) {
	if cachedFiles > 0 {
		fmt.Fprintln(w, cachedHint)
	}
}
