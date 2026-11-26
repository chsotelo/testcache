package main

import (
	"context"
	"testing"
)

func TestAwaitRunOrStopReturnsTheRunOutcome(t *testing.T) {
	out := awaitRunOrStop(context.Background(), func() runOutcome { return runOutcome{code: 3} })
	if out.code != 3 || out.err != nil {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

// A tc blocked on a lock never returns from the runner: once stopped it must still exit.
func TestAwaitRunOrStopGivesUpOnABlockedRunAfterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	out := awaitRunOrStop(ctx, func() runOutcome {
		<-release
		return runOutcome{}
	})
	if out.code != exitCodeInterrupted || out.err == nil {
		t.Fatalf("expected an interrupted outcome, got %+v", out)
	}
}
