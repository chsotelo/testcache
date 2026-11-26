package vitest

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

func (r *Runner) runPassthrough(ctx context.Context, workDir string, opts runner.RunOptions) (int, error) {
	opts.Trace.SetMode(runlog.ModePassthrough)
	return runVitestUncached(ctx, workDir, opts)
}

// runVitestUncached hands the arguments to the project's Vitest without any tc machinery.
func runVitestUncached(ctx context.Context, workDir string, opts runner.RunOptions) (int, error) {
	repoRoot := fingerprint.FindRepoRoot(workDir)
	tools, err := resolveVitestTooling(workDir, repoRoot)
	if err != nil {
		return 1, err
	}
	filtered := make([]string, 0, len(opts.Args))
	for _, a := range opts.Args {
		if a != string(tcFlagNoCache) && a != string(tcFlagNoRevalidate) {
			filtered = append(filtered, a)
		}
	}
	// Watch and UI modes read the terminal, so the child stays in tc's foreground process group.
	cmd := exec.CommandContext(ctx, tools.vitestBin, filtered...)
	cmd.Dir = workDir
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = buildChildEnv()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = groupStopGrace
	start := time.Now()
	code, err := readExitCode(cmd.Run())
	opts.Trace.AddPhase(runlog.PhaseRun, time.Since(start))
	if err == nil {
		opts.Trace.SetVitestExit(code)
	}
	return code, err
}

func readExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		if code >= 0 {
			return code, nil
		}
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return 1, nil
	}
	return 1, err
}
