package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
	"github.com/chsotelo/testcache/internal/runner/vitest"
)

// runInvocation is one `tc vitest` or `tc verify` call: what to run and how to log it.
type runInvocation struct {
	command        cliSubcommand
	label          string
	mode           runlog.Mode
	args           []string
	workDir        string
	noCache        bool
	stdout, stderr io.Writer
	// execute is the runner entry point; tests replace it to provoke failures.
	execute func(ctx context.Context, opts runner.RunOptions) (int, error)
}

// executeLoggedRun runs the invocation, falls back to plain Vitest if tc itself panics, and
// appends exactly one line to the run log. The exit code is always the test run's.
func executeLoggedRun(inv runInvocation) int {
	started := time.Now()
	trace := openRunTrace(inv, started)
	ctx, stop := notifyStopSignals()
	defer stop()

	out := runGuarded(ctx, inv, trace)
	if out.panicMsg != "" {
		out = fallbackToPlainVitest(ctx, inv, trace, out)
	}
	code := reportRunOutcome(inv.stderr, inv.label, out)
	finishRunLog(runFinish{
		inv: inv, trace: trace, started: started, outcome: out, code: code, interrupted: ctx.Err() != nil,
	})
	return code
}

// openRunTrace starts the trace with what is known before the run. A failure while
// resolving the repository is recorded on the trace instead of stopping the run.
func openRunTrace(inv runInvocation, started time.Time) (trace *runlog.Trace) {
	trace = runlog.NewTrace(runlog.Record{
		Time:      started.UTC().Format(time.RFC3339),
		TCVersion: version,
		Command:   string(inv.command),
		Mode:      inv.mode,
		InvokedBy: runlog.ReadInvoker(),
		Cwd:       inv.workDir,
		Argv:      append([]string{programName, string(inv.command)}, inv.args...),
	})
	defer func() {
		if r := recover(); r != nil {
			trace.AddAnomaly(runlog.AnomalyInternalError, describePanic(r))
		}
	}()
	if inv.mode == runlog.ModeCache && config.IsDisabled() {
		trace.SetMode(runlog.ModeDisabled)
	}
	repoRoot := resolveRepoRoot(inv.workDir)
	trace.SetRepo(repoRoot, cache.ComputeRepoID(repoRoot))
	return trace
}

func (inv runInvocation) buildRunOptions(trace *runlog.Trace) runner.RunOptions {
	return runner.RunOptions{
		WorkDir: inv.workDir, Args: inv.args, NoCache: inv.noCache,
		Stdout: inv.stdout, Stderr: inv.stderr, Trace: trace,
	}
}

// runGuarded runs the invocation and turns a panic into an outcome instead of a crash.
func runGuarded(ctx context.Context, inv runInvocation, trace *runlog.Trace) runOutcome {
	return awaitRunOrStop(ctx, func() (out runOutcome) {
		defer func() {
			if r := recover(); r != nil {
				out = runOutcome{code: exitCodeFailure, panicMsg: describePanic(r)}
			}
		}()
		opts := inv.buildRunOptions(trace)
		if trace.CopyRecord().Mode == runlog.ModeDisabled {
			code, err := vitest.RunPlain(ctx, opts)
			return runOutcome{code: code, err: err}
		}
		code, err := inv.execute(ctx, opts)
		return runOutcome{code: code, err: err}
	})
}

// fallbackToPlainVitest keeps the user's test run alive after a tc panic. If Vitest had
// already finished, its verdict stands; otherwise plain Vitest runs with the same arguments.
func fallbackToPlainVitest(ctx context.Context, inv runInvocation, trace *runlog.Trace, failed runOutcome) runOutcome {
	trace.AddAnomaly(runlog.AnomalyInternalError, failed.panicMsg)
	if exit := trace.CopyRecord().VitestExit; exit != nil {
		fmt.Fprintf(inv.stderr, "tc: internal error (%s); keeping the vitest result\n", failed.panicMsg)
		return runOutcome{code: *exit}
	}
	fmt.Fprintf(inv.stderr, "tc: internal error (%s); running plain vitest\n", failed.panicMsg)
	trace.SetMode(runlog.ModeFallback)
	return awaitRunOrStop(ctx, func() (out runOutcome) {
		defer func() {
			if r := recover(); r != nil {
				out = runOutcome{code: exitCodeFailure, err: fmt.Errorf("plain vitest fallback failed: %s", describePanic(r))}
			}
		}()
		code, err := vitest.RunPlain(ctx, inv.buildRunOptions(trace))
		return runOutcome{code: code, err: err}
	})
}

// describePanic renders a recovered panic with the first frame outside the Go runtime.
func describePanic(recovered any) string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	pastPanic := false
	for {
		frame, more := frames.Next()
		if strings.HasPrefix(frame.Function, "runtime.") {
			pastPanic = pastPanic || frame.Function == "runtime.gopanic"
		} else if pastPanic {
			return fmt.Sprintf("panic: %v (%s:%d)", recovered, shortenSourcePath(frame.File), frame.Line)
		}
		if !more {
			return fmt.Sprintf("panic: %v", recovered)
		}
	}
}

func shortenSourcePath(file string) string {
	parts := strings.Split(file, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// runLastResortVitest serves a `tc vitest` whose panic escaped every closer guard: the test
// run still happens, as plain Vitest, and its exit code is returned.
func runLastResortVitest(args []string, workDir string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "tc: plain vitest fallback failed: %v\n", r)
			code = exitCodeFailure
		}
	}()
	ctx, stop := notifyStopSignals()
	defer stop()
	code, err := vitest.RunPlain(ctx, runner.RunOptions{WorkDir: workDir, Args: args, Stdout: os.Stdout, Stderr: os.Stderr})
	return reportRunOutcome(os.Stderr, programName, runOutcome{code: code, err: err})
}
