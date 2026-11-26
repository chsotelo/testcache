package vitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// VerifyReport summarizes a cache verification run against fresh uncached execution.
type VerifyReport struct {
	CheckedFiles    int
	AgreeingEntries int
	StaleFiles      []string
	NewFailures     []string
}

// Verify runs selected test files uncached, compares outcomes to cached entries for the
// current keys, deletes and quarantines stale entries, and prints a summary report.
func (r *Runner) Verify(ctx context.Context, opts runner.RunOptions) (VerifyReport, int, error) {
	parsed := ParseArgs(opts.Args)
	if parsed.BypassCache || parsed.Related {
		return VerifyReport{}, 2, runner.MarkUsageError(errors.New("verify does not support watch, related or passthrough flags"))
	}
	workDir := resolveRealPath(opts.WorkDir)
	env, err := r.prepareEnv(workDir, parsed, true, opts.Stderr)
	if err != nil {
		return VerifyReport{}, 1, err
	}
	defer func() { _ = env.fileCache.Save() }()
	env.attachTrace(opts.Trace)

	discStart := time.Now()
	disc, err := DiscoverTargets(ctx, DiscoverRequest{
		Env: env, WorkDir: workDir, Parsed: parsed, AllowNode: true,
	})
	if err != nil {
		return VerifyReport{}, 1, err
	}
	env.trace.AddPhase(runlog.PhaseDiscover, time.Since(discStart))
	if len(disc.MissingTargets) > 0 {
		missing := relSlashPath(env.proj.ProjectRoot, disc.MissingTargets[0])
		return VerifyReport{}, 1, runner.MarkUsageError(fmt.Errorf("missing target file: %s", missing))
	}
	if len(disc.Cacheable) == 0 && len(disc.ExcludedByTC) == 0 {
		if opts.Stdout != nil {
			fmt.Fprintln(opts.Stdout, "tc: no matching test files")
		}
		return VerifyReport{}, 0, nil
	}
	return r.runAndCompareVerify(ctx, verifyExecRequest{env: env, parsed: parsed, disc: disc, opts: opts})
}

type verifyExecRequest struct {
	env    *runEnv
	parsed ParsedArgs
	disc   DiscoveryResult
	opts   runner.RunOptions
}

func (r *Runner) runAndCompareVerify(ctx context.Context, req verifyExecRequest) (VerifyReport, int, error) {
	states, err := r.buildTargetStates(req.env, req.parsed, req.disc.Cacheable)
	if err != nil {
		return VerifyReport{}, 1, err
	}
	for _, st := range states {
		relRepo := relSlashPath(req.env.proj.RepoRoot, st.absPath)
		if _, quarantined := req.env.quarantine.lookupEntry(relRepo); !quarantined {
			req.env.lookupCachedEntry(st)
		}
	}
	toRun := collectPathsToRun(states, req.disc.ExcludedByTC)
	req.env.traceFileCounts(traceCounts{discovered: len(toRun), run: len(toRun), misses: states})
	proc, err := runVitestProcess(ctx, missedRunRequest{
		env: req.env, parsed: req.parsed, toRun: toRun, misses: states, opts: req.opts,
	})
	if proc.runDir != "" {
		defer os.RemoveAll(proc.runDir)
	}
	if err != nil {
		return VerifyReport{}, proc.exitCode, err
	}
	rep, err := compareVerifyReport(verifyCompareRequest{
		env: req.env, states: states, reportPath: proc.reportPath, stderr: req.opts.Stderr,
	})
	if err != nil {
		return VerifyReport{}, 1, err
	}
	printVerifyReport(req.opts.Stdout, rep)
	if len(rep.StaleFiles) > 0 || len(rep.NewFailures) > 0 || proc.exitCode != 0 {
		if proc.exitCode != 0 {
			return rep, proc.exitCode, nil
		}
		return rep, 1, nil
	}
	return rep, 0, nil
}

type verifyCompareRequest struct {
	env        *runEnv
	states     []*targetState
	reportPath string
	stderr     io.Writer
}

func compareVerifyReport(req verifyCompareRequest) (VerifyReport, error) {
	data, err := os.ReadFile(req.reportPath)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("read vitest report: %w", err)
	}
	var payload reportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return VerifyReport{}, fmt.Errorf("parse vitest report: %w", err)
	}
	byPath := make(map[string]*targetState, len(req.states))
	for _, st := range req.states {
		byPath[st.absPath] = st
	}
	var rep VerifyReport
	for _, mod := range payload.Modules {
		rep.CheckedFiles++
		absMod := resolveRealPath(mod.TestPath)
		st, ok := byPath[absMod]
		if ok && st.hit != nil && st.hit.Passed {
			if mod.Passed {
				rep.AgreeingEntries++
				continue
			}
			rep.StaleFiles = append(rep.StaleFiles, st.relPath)
			if err := quarantineStaleTest(quarantineRequest{
				env: req.env, st: st, failedTests: mod.FailedTests, stderr: req.stderr,
			}); err != nil && req.stderr != nil {
				fmt.Fprintf(req.stderr, "tc: warning: failed to record quarantine for %s: %v\n", st.relPath, err)
			}
			continue
		}
		if !mod.Passed {
			rep.NewFailures = append(rep.NewFailures, relSlashPath(req.env.proj.ProjectRoot, absMod))
		}
	}
	sort.Strings(rep.StaleFiles)
	sort.Strings(rep.NewFailures)
	return rep, nil
}

func printVerifyReport(w io.Writer, rep VerifyReport) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "tc verify: %d checked, %d cached agreeing, %d stale, %d new failures\n",
		rep.CheckedFiles, rep.AgreeingEntries, len(rep.StaleFiles), len(rep.NewFailures))
	for _, f := range rep.StaleFiles {
		fmt.Fprintf(w, "  stale (quarantined): %s\n", f)
	}
	for _, f := range rep.NewFailures {
		fmt.Fprintf(w, "  failed (no cache entry): %s\n", f)
	}
}
