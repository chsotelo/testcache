package lock

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestAcquireContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "key.lock")

	g1, err := Acquire(lockPath, nil)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	waiting := make(chan struct{})
	var once sync.Once
	done := make(chan *Guard, 1)
	go func() {
		g2, err := Acquire(lockPath, func() { once.Do(func() { close(waiting) }) })
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			return
		}
		done <- g2
	}()

	<-waiting
	select {
	case <-done:
		t.Fatalf("second acquirer succeeded while lock still held")
	default:
	}

	if err := g1.Release(); err != nil {
		t.Fatalf("release g1: %v", err)
	}
	g2 := <-done
	if err := g2.Release(); err != nil {
		t.Fatalf("release g2: %v", err)
	}
}

func TestAcquireSlot(t *testing.T) {
	dir := t.TempDir()
	g1, err := AcquireSlot(dir, "vitest", 2, nil)
	if err != nil {
		t.Fatalf("slot 1: %v", err)
	}
	defer g1.Release()

	g2, err := AcquireSlot(dir, "vitest", 2, nil)
	if err != nil {
		t.Fatalf("slot 2: %v", err)
	}
	defer g2.Release()

	waiting := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		g3, err := AcquireSlot(dir, "vitest", 2, func() { once.Do(func() { close(waiting) }) })
		if err != nil {
			t.Errorf("slot 3: %v", err)
			return
		}
		_ = g3.Release()
		close(done)
	}()

	<-waiting
	_ = g1.Release()
	<-done
}

func TestFlockHelperProcess(t *testing.T) {
	if os.Getenv("TC_FLOCK_HELPER") != "1" {
		lockPath := filepath.Join(t.TempDir(), "try.lock")
		g1, ok, err := TryAcquire(lockPath)
		if err != nil || !ok {
			t.Fatalf("initial TryAcquire failed: ok=%v err=%v", ok, err)
		}
		if _, ok2, err2 := TryAcquire(lockPath); err2 != nil || ok2 {
			_ = g1.Release()
			t.Fatalf("expected contended TryAcquire to fail: ok=%v err=%v", ok2, err2)
		}
		if err := g1.Release(); err != nil {
			t.Fatalf("release lock: %v", err)
		}
		g2, ok, err := TryAcquire(lockPath)
		if err != nil || !ok {
			t.Fatalf("TryAcquire after release failed: ok=%v err=%v", ok, err)
		}
		_ = g2.Release()
		return
	}
	lockPath := os.Getenv("TC_FLOCK_PATH")
	g, err := Acquire(lockPath, nil)
	if err != nil {
		os.Exit(2)
	}
	defer g.Release()
	_, _ = os.Stdout.WriteString("LOCKED\n")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("helper timed out waiting to be killed")
	}
}

func TestKilledProcessReleasesFlock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "stale.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFlockHelperProcess$")
	cmd.Env = append(os.Environ(), "TC_FLOCK_HELPER=1", "TC_FLOCK_PATH="+lockPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.Contains(line, "LOCKED") {
		_ = cmd.Process.Kill()
		t.Fatalf("helper failed to lock: line=%q err=%v", line, err)
	}
	if _, ok, err := TryAcquire(lockPath); err != nil || ok {
		_ = cmd.Process.Kill()
		t.Fatalf("expected TryAcquire to fail while helper holds lock, ok=%v err=%v", ok, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	// Lock file still exists on disk, yet TryAcquire must succeed immediately.
	g, ok, err := TryAcquire(lockPath)
	if err != nil || !ok {
		t.Fatalf("expected stale lock recovered after SIGKILL, ok=%v err=%v", ok, err)
	}
	_ = g.Release()
}
