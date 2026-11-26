package vitest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	orphanDeadline = 2 * time.Second
	pollInterval   = 25 * time.Millisecond
)

func buildTcBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tcBin := filepath.Join(t.TempDir(), "tc")
	buildCmd := exec.Command("go", "build", "-o", tcBin, "./cmd/tc")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build tc: %v\n%s", err, out)
	}
	return tcBin
}

// waitForPidFile polls until the hanging test has written "<vitest pid> <worker pid>".
func waitForPidFile(t *testing.T, path string) []int {
	t.Helper()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	deadline := time.After(20 * time.Second)
	for {
		if fields := strings.Fields(readFileOrEmpty(path)); len(fields) == 2 {
			pids := make([]int, 0, len(fields))
			for _, f := range fields {
				pid, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("bad pid %q in %s", f, path)
				}
				pids = append(pids, pid)
			}
			return pids
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
}

func readFileOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func isProcessAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// waitForProcessesGone reports the pids still alive once the deadline has passed.
func waitForProcessesGone(pids []int, limit time.Duration) []int {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	deadline := time.After(limit)
	for {
		var alive []int
		for _, pid := range pids {
			if isProcessAlive(pid) {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			return nil
		}
		select {
		case <-deadline:
			return alive
		case <-ticker.C:
		}
	}
}

// startHangingRun starts tc on a test that never finishes and returns the Vitest main and
// worker pids once the test is running.
func startHangingRun(t *testing.T) (*exec.Cmd, []int) {
	t.Helper()
	projDir := setupFullFixtureCopy(t)
	pidFile := filepath.Join(t.TempDir(), "pids")
	writeFixtureFile(t, filepath.Join(projDir, "tests", "hang.test.ts"), `import { writeFileSync } from 'node:fs';
import { it } from 'vitest';
it('hangs until killed', async () => {
  writeFileSync(process.env.TC_TEST_PID_FILE!, process.ppid + ' ' + process.pid);
  await new Promise((resolve) => setTimeout(resolve, 60_000));
}, 120_000);
`)
	cmd := exec.Command(buildTcBinary(t), "vitest", "tests/hang.test.ts")
	cmd.Dir = projDir
	cmd.Env = append(os.Environ(), "TC_TEST_PID_FILE="+pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pids := waitForPidFile(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return cmd, pids
}

// Stopping tc mid-run must take the Vitest main process and its workers down with it: SIGINT
// and SIGTERM through the process group, SIGKILL through the parent watchdog in the wrapper.
func TestVitestStoppedTcLeavesNoVitestProcess(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd, pids := startHangingRun(t)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			alive := waitForProcessesGone(pids, orphanDeadline)
			_ = cmd.Wait()
			if len(alive) > 0 {
				t.Fatalf("vitest processes %v still alive %s after tc got %s", alive, orphanDeadline, sig)
			}
			if code := cmd.ProcessState.ExitCode(); code == 0 {
				t.Fatalf("stopped tc must not exit 0")
			}
		})
	}
}
