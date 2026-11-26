package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// TestMain lets the test binary stand in for tc when startAlert re-executes it as the
// detached alert helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == string(cmdAlertExec) {
		os.Exit(runAlertExec(os.Args[2:]))
	}
	os.Exit(m.Run())
}

const fakeVitestBanner = "FAKE VITEST RAN"

// fakeProject is a project whose "vitest" is a shell script that records its arguments.
type fakeProject struct {
	dir      string
	cacheDir string
	argsFile string
	stdout   bytes.Buffer
	stderr   bytes.Buffer
}

func newFakeProject(t *testing.T, vitestExit int) *fakeProject {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProject{dir: filepath.Join(root, "proj"), cacheDir: filepath.Join(root, "cache"), argsFile: filepath.Join(root, "vitest-args.txt")}
	binDir := filepath.Join(p.dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\necho '%s'\nexit %d\n", p.argsFile, fakeVitestBanner, vitestExit)
	if err := os.WriteFile(filepath.Join(binDir, "vitest"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTCACHE_DIR", p.cacheDir)
	t.Setenv("TESTCACHE_CONFIG_DIR", filepath.Join(root, "usercfg"))
	t.Setenv(runlog.EnvInvokedBy, "")
	t.Setenv(config.EnvDisable, "")
	return p
}

// invoke runs one logged `tc vitest` whose runner is replaced by execute.
func (p *fakeProject) invoke(args []string, execute func(context.Context, runner.RunOptions) (int, error)) int {
	p.stdout.Reset()
	p.stderr.Reset()
	return executeLoggedRun(runInvocation{
		command: cmdVitest, label: programName, mode: runlog.ModeCache, args: args, workDir: p.dir,
		stdout: &p.stdout, stderr: &p.stderr, execute: execute,
	})
}

func (p *fakeProject) readVitestArgs(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(p.argsFile)
	if err != nil {
		t.Fatalf("the fake vitest did not run: %v", err)
	}
	return strings.Join(strings.Fields(string(data)), " ")
}

func (p *fakeProject) readLog(t *testing.T) []runlog.LoggedRun {
	t.Helper()
	runs, err := runlog.OpenLog(p.cacheDir).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// readOnlyRun returns the single line the log must hold.
func (p *fakeProject) readOnlyRun(t *testing.T) runlog.Record {
	t.Helper()
	runs := p.readLog(t)
	if len(runs) != 1 {
		t.Fatalf("expected exactly one log line, got %d", len(runs))
	}
	return runs[0].Record
}

func joinCodes(rec runlog.Record) string {
	return joinAnomalyCodes(rec.Anomalies)
}

func TestPanicFallsBackToPlainVitest(t *testing.T) {
	const vitestExit = 7
	p := newFakeProject(t, vitestExit)
	code := p.invoke([]string{"--no-cache", "tests/a.test.ts"}, func(context.Context, runner.RunOptions) (int, error) {
		panic("boom")
	})

	if code != vitestExit {
		t.Fatalf("the exit code must be vitest's (%d), got %d\nstderr: %s", vitestExit, code, p.stderr.String())
	}
	if got := p.readVitestArgs(t); got != "run tests/a.test.ts" {
		t.Fatalf("plain vitest must get the original arguments as a single run, got %q", got)
	}
	if !strings.Contains(p.stdout.String(), fakeVitestBanner) || !strings.Contains(p.stderr.String(), "running plain vitest") {
		t.Fatalf("unexpected output\nstdout: %s\nstderr: %s", p.stdout.String(), p.stderr.String())
	}
	rec := p.readOnlyRun(t)
	if rec.Mode != runlog.ModeFallback || joinCodes(rec) != string(runlog.AnomalyInternalError) {
		t.Fatalf("expected a fallback run flagged internal_error, got mode=%s anomalies=%+v", rec.Mode, rec.Anomalies)
	}
	if detail := rec.Anomalies[0].Detail; !strings.Contains(detail, "panic: boom") || !strings.Contains(detail, "run_logged_test.go:") {
		t.Fatalf("the detail must carry the panic and where it happened, got %q", detail)
	}
	if rec.VitestExit == nil || *rec.VitestExit != vitestExit || rec.TCExit != vitestExit || rec.Error != "" {
		t.Fatalf("unexpected exit fields: %+v", rec)
	}
}

func TestPanicAfterVitestRanKeepsItsVerdict(t *testing.T) {
	p := newFakeProject(t, 0)
	code := p.invoke(nil, func(_ context.Context, opts runner.RunOptions) (int, error) {
		opts.Trace.SetVitestExit(3)
		panic("late")
	})
	if code != 3 {
		t.Fatalf("vitest already answered with 3, got %d", code)
	}
	if _, err := os.Stat(p.argsFile); !os.IsNotExist(err) {
		t.Fatal("vitest must not run a second time")
	}
	rec := p.readOnlyRun(t)
	if rec.Mode != runlog.ModeCache || joinCodes(rec) != string(runlog.AnomalyInternalError) || rec.TCExit != 3 {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestDisableRunsPlainVitest(t *testing.T) {
	p := newFakeProject(t, 4)
	t.Setenv(config.EnvDisable, "1")
	code := p.invoke([]string{"tests/a.test.ts"}, func(context.Context, runner.RunOptions) (int, error) {
		t.Error("the tc runner must not be used when TC_DISABLE is set")
		return 0, nil
	})
	if code != 4 || p.readVitestArgs(t) != "run tests/a.test.ts" {
		t.Fatalf("expected plain vitest (exit 4), got code=%d args=%q", code, p.readVitestArgs(t))
	}
	rec := p.readOnlyRun(t)
	if rec.Mode != runlog.ModeDisabled || len(rec.Anomalies) != 0 {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestDisableThroughTheCLIKeepsWatchArgumentsAsTheyAre(t *testing.T) {
	p := newFakeProject(t, 0)
	t.Setenv(config.EnvDisable, "1")
	t.Setenv(runlog.EnvInvokedBy, string(runlog.InvokerClaudeHook))
	if code := runCLI([]string{"vitest", "--watch"}, p.dir, &p.stdout, &p.stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, p.stderr.String())
	}
	if got := p.readVitestArgs(t); got != "--watch" {
		t.Fatalf("a watch run must not be turned into a single run, got %q", got)
	}
	rec := p.readOnlyRun(t)
	if rec.InvokedBy != runlog.InvokerClaudeHook || rec.Command != "vitest" || strings.Join(rec.Argv, " ") != "tc vitest --watch" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.TCVersion != version || rec.Cwd != p.dir || rec.RepoRoot != p.dir || rec.RepoID == "" {
		t.Fatalf("identity fields are wrong: %+v", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.Time); err != nil || !strings.HasSuffix(rec.Time, "Z") {
		t.Fatalf("time must be UTC RFC 3339, got %q", rec.Time)
	}
}

func TestInterruptedRunIsLogged(t *testing.T) {
	p := newFakeProject(t, 0)
	code := p.invoke(nil, func(ctx context.Context, _ runner.RunOptions) (int, error) {
		// tc is listening for SIGINT at this point, so the signal cancels ctx instead of killing the test.
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return 1, err
		}
		<-ctx.Done()
		return exitCodeInterrupted, errors.New("interrupted: vitest run stopped, nothing cached")
	})
	if code != exitCodeInterrupted {
		t.Fatalf("expected exit %d, got %d", exitCodeInterrupted, code)
	}
	rec := p.readOnlyRun(t)
	if joinCodes(rec) != string(runlog.AnomalyInterrupted) || rec.Error != "" || rec.TCExit != exitCodeInterrupted {
		t.Fatalf("expected only the interrupted anomaly and no error, got %+v", rec)
	}
	if runlog.ShouldAlert(rec) {
		t.Fatal("an interrupted run must not alert")
	}
}

func TestRunnerErrorsAreClassified(t *testing.T) {
	p := newFakeProject(t, 0)
	code := p.invoke(nil, func(context.Context, runner.RunOptions) (int, error) {
		return 0, errors.New("write cache: disk full")
	})
	if code != exitCodeFailure || !strings.Contains(p.stderr.String(), "tc: write cache: disk full") {
		t.Fatalf("a tc failure exits non-zero and is printed, got %d %q", code, p.stderr.String())
	}
	p.invoke(nil, func(context.Context, runner.RunOptions) (int, error) {
		return 1, runner.MarkUsageError(errors.New("vitest binary not found in /x"))
	})

	runs := p.readLog(t)
	if len(runs) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(runs))
	}
	failed, usage := runs[0].Record, runs[1].Record
	if joinCodes(failed) != string(runlog.AnomalyInternalError) || failed.Error != "write cache: disk full" || failed.VitestExit != nil {
		t.Fatalf("an unexpected tc error is an internal_error: %+v", failed)
	}
	if len(usage.Anomalies) != 0 || usage.Error != "vitest binary not found in /x" || usage.TCExit != 1 {
		t.Fatalf("a usage error is logged without an anomaly: %+v", usage)
	}
}

func TestEveryInvocationAppendsExactlyOneLine(t *testing.T) {
	p := newFakeProject(t, 0)
	succeed := func(_ context.Context, opts runner.RunOptions) (int, error) {
		opts.Trace.SetFiles(runlog.FileCounts{Discovered: 2, Cached: 2})
		opts.Trace.AddPhase(runlog.PhaseLockWait, 61*time.Second)
		return 0, nil
	}
	for i := 1; i <= 3; i++ {
		p.invoke(nil, succeed)
		if got := len(p.readLog(t)); got != i {
			t.Fatalf("after %d invocations the log has %d lines", i, got)
		}
	}
	for _, args := range [][]string{{"verify", "--help"}, {"log"}, {"stats"}, {"version"}} {
		runCLI(args, p.dir, &p.stdout, &p.stderr)
	}
	runs := p.readLog(t)
	if len(runs) != 3 {
		t.Fatalf("only vitest and verify runs are logged, got %d lines", len(runs))
	}
	// Timing anomalies are computed when the run ends, from the record itself.
	if joinCodes(runs[2].Record) != string(runlog.AnomalyLongLockWait) {
		t.Fatalf("expected long_lock_wait, got %+v", runs[2].Record.Anomalies)
	}
}

func TestAlertCommandRunsDetachedWithTheLogLine(t *testing.T) {
	p := newFakeProject(t, 0)
	fifo := filepath.Join(filepath.Dir(p.dir), "alert.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cfgDir := os.Getenv("TESTCACHE_CONFIG_DIR")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"alertCommand": "cat > '%s'"}`, fifo)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Nobody reads the FIFO yet, so the alert command cannot finish: if tc waited for it,
	// this call would not return.
	code := p.invoke(nil, func(_ context.Context, opts runner.RunOptions) (int, error) {
		opts.Trace.AddAnomaly(runlog.AnomalyStaleEntry, "tests/a.test.ts")
		return 0, nil
	})
	if code != 0 {
		t.Fatalf("an alert must not change the exit code, got %d: %s", code, p.stderr.String())
	}
	runs := p.readLog(t)
	if len(runs) != 1 {
		t.Fatalf("expected one log line, got %d", len(runs))
	}

	received := make(chan []byte, 1)
	go func() {
		data, _ := os.ReadFile(fifo)
		received <- data
	}()
	select {
	case got := <-received:
		if want := string(runs[0].Raw) + "\n"; string(got) != want {
			t.Fatalf("the alert command must get the log line on stdin\n got %q\nwant %q", got, want)
		}
	case <-time.After(runlog.AlertTimeout + 20*time.Second):
		t.Fatal("the alert command never ran")
	}
}

func TestAlertFailureIsReportedOnceByTheNextRun(t *testing.T) {
	p := newFakeProject(t, 0)
	runlog.OpenLog(p.cacheDir).SaveAlertFailure(errors.New("alertCommand failed: exit status 1"))
	succeed := func(context.Context, runner.RunOptions) (int, error) { return 0, nil }

	if code := p.invoke(nil, succeed); code != 0 || !strings.Contains(p.stderr.String(), "tc: alertCommand failed: exit status 1") {
		t.Fatalf("the next run reports the failure without failing, code=%d stderr=%q", code, p.stderr.String())
	}
	if p.invoke(nil, succeed); p.stderr.Len() != 0 {
		t.Fatalf("the failure is reported once, got %q again", p.stderr.String())
	}
}

func TestUnwritableLogNeverBreaksTheRun(t *testing.T) {
	p := newFakeProject(t, 0)
	// The cache root is a file, so the log cannot be created.
	if err := os.WriteFile(p.cacheDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := p.invoke(nil, func(context.Context, runner.RunOptions) (int, error) { return 5, nil })
	if code != 5 || !strings.Contains(p.stderr.String(), "tc: warning:") {
		t.Fatalf("the run result must survive a broken log, code=%d stderr=%q", code, p.stderr.String())
	}
}
