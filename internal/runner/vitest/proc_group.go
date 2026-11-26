package vitest

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const (
	// groupStopGrace is how long a cancelled Vitest process group gets to exit before SIGKILL.
	groupStopGrace = time.Second
	// exitCodeInterrupted mirrors the shell convention for a run stopped by SIGINT.
	exitCodeInterrupted = 130
)

var errRunInterrupted = errors.New("interrupted: vitest run stopped, nothing cached")

// runInProcessGroup runs cmd as the leader of its own process group. When ctx is cancelled the
// whole group gets SIGTERM and, after groupStopGrace, SIGKILL, so no worker outlives tc.
func runInProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pgid := cmd.Process.Pid
	exited := make(chan struct{})
	go stopGroupOnCancel(ctx, pgid, exited)
	err := cmd.Wait()
	close(exited)
	if ctx.Err() != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return err
}

func stopGroupOnCancel(ctx context.Context, pgid int, exited <-chan struct{}) {
	select {
	case <-exited:
		return
	case <-ctx.Done():
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	grace := time.NewTimer(groupStopGrace)
	defer grace.Stop()
	select {
	case <-exited:
	case <-grace.C:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}
