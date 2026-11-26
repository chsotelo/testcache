package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// runFinish is everything known when a logged run ends.
type runFinish struct {
	inv         runInvocation
	trace       *runlog.Trace
	started     time.Time
	outcome     runOutcome
	code        int
	interrupted bool
}

// finishRunLog completes the record, appends it to the run log and starts the alert command.
// Nothing here may change the outcome of the test run: failures are reported and dropped.
func finishRunLog(fin runFinish) {
	stderr := fin.inv.stderr
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "tc: warning: run log failed: %v\n", r)
		}
	}()
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		return
	}
	log := runlog.OpenLog(baseDir)
	log.ReportAlertFailure(stderr)

	rec := completeRecord(fin)
	if history, err := log.ReadRepoHistory(rec.RepoID); err == nil {
		rec.Anomalies = append(rec.Anomalies, runlog.DetectAnomalies(rec, history)...)
	}
	line, err := runlog.EncodeLine(rec)
	if err == nil {
		err = log.AppendLine(line)
	}
	if err != nil {
		fmt.Fprintf(stderr, "tc: warning: %v\n", err)
		return
	}
	if runlog.ShouldAlert(rec) {
		startAlert(alertStart{rec: rec, line: line, workDir: fin.inv.workDir, log: log})
	}
}

// completeRecord adds what only the end of the run knows: duration, exit code, error.
func completeRecord(fin runFinish) runlog.Record {
	trace := fin.trace
	switch err := fin.outcome.err; {
	case fin.interrupted:
		trace.AddAnomaly(runlog.AnomalyInterrupted, "stopped by a signal")
	case err != nil && !runner.IsUsageError(err):
		trace.AddAnomaly(runlog.AnomalyInternalError, err.Error())
	}
	rec := trace.CopyRecord()
	rec.DurationMs = time.Since(fin.started).Milliseconds()
	rec.TCExit = fin.code
	if fin.outcome.err != nil && !fin.interrupted {
		rec.Error = fin.outcome.err.Error()
	}
	return rec
}

// alertStart is what launching the alert command needs.
type alertStart struct {
	rec     runlog.Record
	line    []byte
	workDir string
	log     runlog.Log
}

// startAlert launches the configured alertCommand in a detached helper process and returns
// at once: the helper enforces the timeout and records a failure for the next run to print.
func startAlert(req alertStart) {
	cfg, _, err := config.Load(req.rec.RepoRoot, resolveAbsPath(req.workDir))
	if err != nil || cfg.AlertCommand == "" {
		return
	}
	self, err := os.Executable()
	if err == nil {
		helper := exec.Command(self, string(cmdAlertExec), cfg.AlertCommand, string(req.line))
		helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err = helper.Start(); err == nil {
			_ = helper.Process.Release()
			return
		}
	}
	req.log.SaveAlertFailure(fmt.Errorf("alertCommand could not start: %w", err))
}

// runAlertExec is the detached helper behind startAlert: `tc alert-exec <command> <line>`.
func runAlertExec(args []string) int {
	if len(args) != 2 {
		return exitCodeUsage
	}
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		return exitCodeFailure
	}
	req := runlog.AlertRequest{Command: args[0], Line: []byte(args[1]), Timeout: runlog.AlertTimeout}
	if err := runlog.OpenLog(baseDir).ExecuteAlert(req); err != nil {
		return exitCodeFailure
	}
	return 0
}
