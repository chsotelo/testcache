package vitest

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/lock"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

//go:embed assets/wrapper.config.mjs assets/coverage-setup.mjs assets/tc-reporter.mjs
var embeddedAssets embed.FS

type runtimeFiles struct {
	wrapperConfig string
	coverageSetup string
	reporter      string
}

type missedRunRequest struct {
	env    *runEnv
	parsed ParsedArgs
	toRun  []string
	misses []*targetState
	opts   runner.RunOptions
	// related, when set, makes Vitest run only the given files that import a source.
	related *relatedSelection
}

type vitestCmdRequest struct {
	env        *runEnv
	parsed     ParsedArgs
	rt         runtimeFiles
	toRun      []string
	covDir     string
	reportPath string
	opts       runner.RunOptions
	related    *relatedSelection
}

type vitestProcessResult struct {
	runDir     string
	covDir     string
	reportPath string
	exitCode   int
	wallMs     int64
}

// missedRunResult is what running the missed files produced.
type missedRunResult struct {
	exitCode int
	ran      resultTally
}

func (r *Runner) executeMissed(ctx context.Context, req missedRunRequest) (missedRunResult, error) {
	window := openRunWindow(req.env, req.misses)
	proc, err := runVitestProcess(ctx, req)
	if proc.runDir != "" {
		defer os.RemoveAll(proc.runDir)
	}
	res := missedRunResult{exitCode: proc.exitCode}
	if err != nil {
		return res, err
	}
	res.ran = tallyRunReport(proc.reportPath)
	if req.opts.NoCache || req.parsed.NoCache {
		logVerbose(req.env.verbose, req.opts.Stderr, "persist: 0ms (0 saved, skipped)")
		return res, nil
	}
	persistStart := time.Now()
	saved, err := persistRunResults(persistResultsRequest{
		env: req.env, parsed: req.parsed, misses: req.misses, covDir: proc.covDir, reportPath: proc.reportPath,
		wallMs: proc.wallMs, runCount: len(req.toRun), window: window, stdout: req.opts.Stdout, stderr: req.opts.Stderr,
	})
	if err != nil {
		fmt.Fprintf(req.opts.Stderr, "tc: warning: failed to save cache entries: %v\n", err)
		req.env.trace.AddAnomaly(runlog.AnomalyInternalError, fmt.Sprintf("failed to save cache entries: %v", err))
	}
	req.env.trace.AddPhase(runlog.PhasePersist, time.Since(persistStart))
	logVerbose(req.env.verbose, req.opts.Stderr, "persist: %dms (%d saved, %d misses)",
		time.Since(persistStart).Milliseconds(), saved, len(req.misses))
	return res, nil
}

func runVitestProcess(ctx context.Context, req missedRunRequest) (vitestProcessResult, error) {
	rt, err := materializeAssets(req.env.store.RuntimeDir())
	if err != nil {
		return vitestProcessResult{exitCode: 1}, err
	}
	slotStart := time.Now()
	semGuard, err := lock.AcquireSlot(req.env.store.LocksDir(), string(pkgVitest)+"-"+req.env.store.CheckoutID, req.env.cfg.MaxConcurrentRuns, func() {
		fmt.Fprintf(req.opts.Stderr, "tc: queued (another vitest run is active for this repo)\n")
	})
	if err != nil {
		return vitestProcessResult{exitCode: 1}, fmt.Errorf("acquire vitest semaphore: %w", err)
	}
	req.env.trace.AddPhase(runlog.PhaseLockWait, time.Since(slotStart))
	defer semGuard.Release()

	runDir, covDir, err := createRunTempDirs(req.env.store.RuntimeDir())
	if err != nil {
		return vitestProcessResult{exitCode: 1}, err
	}
	reportPath := filepath.Join(runDir, "report.json")
	cmd := buildVitestCmd(vitestCmdRequest{
		env: req.env, parsed: req.parsed, rt: rt, toRun: req.toRun,
		covDir: covDir, reportPath: reportPath, opts: req.opts, related: req.related,
	})

	start := time.Now()
	exitCode, err := readExitCode(runInProcessGroup(ctx, cmd))
	wallMs := time.Since(start).Milliseconds()
	req.env.trace.AddPhase(runlog.PhaseRun, time.Since(start))
	if err == nil {
		req.env.trace.SetVitestExit(exitCode)
	}
	logVerbose(req.env.verbose, req.opts.Stderr, "run: %dms (%d files, exit %d)", wallMs, len(req.toRun), exitCode)
	res := vitestProcessResult{
		runDir: runDir, covDir: covDir, reportPath: reportPath, exitCode: exitCode, wallMs: wallMs,
	}
	if err != nil {
		return res, fmt.Errorf("execute vitest: %w", err)
	}
	if ctx.Err() != nil {
		res.exitCode = exitCodeInterrupted
		return res, errRunInterrupted
	}
	return res, nil
}

func createRunTempDirs(runtimeDir string) (runDir, covDir string, err error) {
	runDir, err = os.MkdirTemp(runtimeDir, "run-*")
	if err != nil {
		return "", "", fmt.Errorf("create run temp dir: %w", err)
	}
	covDir = filepath.Join(runDir, "cov")
	if err := os.MkdirAll(covDir, 0o755); err != nil {
		_ = os.RemoveAll(runDir)
		return "", "", fmt.Errorf("create coverage temp dir: %w", err)
	}
	return runDir, covDir, nil
}

func buildVitestCmd(req vitestCmdRequest) *exec.Cmd {
	args := []string{"run", "--config", req.rt.wrapperConfig}
	args = append(args, req.parsed.ForwardFlags...)
	if !req.parsed.HasMaxWorkers {
		workers := maxInt(minInt(len(req.toRun), req.env.cfg.MaxWorkers), 1)
		args = append(args, "--maxWorkers="+strconv.Itoa(workers))
	}
	if req.parsed.HasReporter {
		args = append(args, "--reporter="+req.rt.reporter)
	}
	for _, abs := range req.toRun {
		args = append(args, relSlashPath(req.env.proj.ProjectRoot, abs))
	}

	cmd := exec.Command(req.env.proj.VitestBin, args...)
	cmd.Dir = req.env.proj.ProjectRoot
	cmd.Stdout = req.opts.Stdout
	cmd.Stderr = req.opts.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(buildChildEnv(),
		"TESTCACHE_PROJECT_ROOT="+req.env.proj.ProjectRoot,
		"TESTCACHE_REPO_ROOT="+req.env.proj.RepoRoot,
		"TESTCACHE_RUNTIME_DIR="+req.env.store.RuntimeDir(),
		"TESTCACHE_VITE_ENTRY="+req.env.proj.ViteEntry,
		"TESTCACHE_VITEST_CONFIG_ENTRY="+req.env.proj.VitestConfigEntry,
		"TESTCACHE_BASE_CONFIG="+req.env.proj.BaseConfigFile,
		"TESTCACHE_VITE_MODE="+req.parsed.Mode,
		"TESTCACHE_VITEST_VERSION="+req.env.proj.VitestVersion,
		"TESTCACHE_SETUP_FILE="+req.rt.coverageSetup,
		"TESTCACHE_REPORTER_FILE="+req.rt.reporter,
		"TESTCACHE_PASS_REPORTER_CLI="+boolFlag(req.parsed.HasReporter),
		"TESTCACHE_COVERAGE_DIR="+req.covDir,
		"TESTCACHE_REPORT_FILE="+req.reportPath,
	)
	if req.related != nil {
		// wrapper.config.mjs sets test.related from it, so Vitest itself decides which of the
		// given files import a source.
		sources, _ := json.Marshal(req.related.sources)
		cmd.Env = append(cmd.Env, "TESTCACHE_RELATED="+string(sources))
	}
	return cmd
}

func materializeAssets(runtimeDir string) (runtimeFiles, error) {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return runtimeFiles{}, fmt.Errorf("create runtime dir: %w", err)
	}
	specs := []struct {
		embedPath string
		outName   string
	}{
		{"assets/wrapper.config.mjs", "wrapper.config.mjs"},
		{"assets/coverage-setup.mjs", "coverage-setup.mjs"},
		{"assets/tc-reporter.mjs", "tc-reporter.mjs"},
	}
	var out [3]string
	for i, s := range specs {
		data, err := embeddedAssets.ReadFile(s.embedPath)
		if err != nil {
			return runtimeFiles{}, fmt.Errorf("read embedded %s: %w", s.embedPath, err)
		}
		dst := filepath.Join(runtimeDir, s.outName)
		if existing, err := os.ReadFile(dst); err != nil || !bytes.Equal(existing, data) {
			if err := cache.WriteFileAtomic(dst, data, 0o644); err != nil {
				return runtimeFiles{}, fmt.Errorf("write %s: %w", dst, err)
			}
		}
		out[i] = dst
	}
	return runtimeFiles{
		wrapperConfig: out[0],
		coverageSetup: out[1],
		reporter:      out[2],
	}, nil
}

func boolFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
