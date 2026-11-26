package runlog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// AlertTimeout is how long an alertCommand may run before it is killed.
	AlertTimeout = 10 * time.Second
	// alertErrorFile holds the last alertCommand failure until the next run reports it.
	alertErrorFile = "alert-error.log"
	alertShell     = "/bin/sh"
	alertKillGrace = time.Second
)

// AlertRequest describes one alertCommand execution.
type AlertRequest struct {
	Command string
	Line    []byte
	Timeout time.Duration
}

// ExecuteAlert runs the alert command through /bin/sh -c with the log line on stdin and
// waits for it, killing its whole process group at the timeout. tc runs it in a detached
// helper process, so nothing here can delay or fail a test run. A failure is written to the
// alert error file for the next run to report.
func (l Log) ExecuteAlert(req AlertRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, alertShell, "-c", req.Command)
	cmd.Stdin = bytes.NewReader(req.Line)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = alertKillGrace
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", req.Timeout)
	}
	err = fmt.Errorf("alertCommand failed: %w", err)
	l.SaveAlertFailure(err)
	return err
}

// SaveAlertFailure remembers an alert failure for the next run to report.
func (l Log) SaveAlertFailure(failure error) {
	_ = os.MkdirAll(l.Dir, 0o755)
	_ = os.WriteFile(filepath.Join(l.Dir, alertErrorFile), []byte(failure.Error()+"\n"), 0o644)
}

// ReportAlertFailure prints the failure of an earlier alertCommand to stderr, once.
func (l Log) ReportAlertFailure(stderr io.Writer) {
	p := filepath.Join(l.Dir, alertErrorFile)
	data, err := os.ReadFile(p)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(p)
		}
		return
	}
	_ = os.Remove(p)
	if msg := strings.TrimSpace(string(data)); msg != "" && stderr != nil {
		fmt.Fprintf(stderr, "tc: %s\n", msg)
	}
}
