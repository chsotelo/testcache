package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	// stopDeadline bounds how long tc lingers after SIGINT/SIGTERM; the runner needs about one
	// second to stop the Vitest process group, and a tc still queued on a lock needs none.
	stopDeadline        = 2 * time.Second
	exitCodeInterrupted = 130
)

type runOutcome struct {
	code int
	err  error
	// panicMsg is set when the run panicked instead of returning.
	panicMsg string
}

// notifyStopSignals returns a context cancelled by SIGINT or SIGTERM, so a stopped tc takes
// the Vitest process group down with it instead of leaving it orphaned.
func notifyStopSignals() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// awaitRunOrStop waits for run to return. Once ctx is cancelled it waits stopDeadline more and
// then gives up, so a tc blocked waiting for a lock still exits.
func awaitRunOrStop(ctx context.Context, run func() runOutcome) runOutcome {
	done := make(chan runOutcome, 1)
	go func() { done <- run() }()
	select {
	case out := <-done:
		return out
	case <-ctx.Done():
	}
	deadline := time.NewTimer(stopDeadline)
	defer deadline.Stop()
	select {
	case out := <-done:
		return out
	case <-deadline.C:
		return runOutcome{code: exitCodeInterrupted, err: errors.New("interrupted")}
	}
}

func reportRunOutcome(stderr io.Writer, label string, out runOutcome) int {
	if out.err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", label, out.err)
		if out.code == 0 {
			return 1
		}
	}
	return out.code
}
