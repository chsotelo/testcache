package vitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVitestCorruptCacheFilesRecoverCleanly(t *testing.T) {
	projDir, cacheBase := setupFixtureCopy(t)
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")

	repoCacheDir := singleRepoCacheDir(t, cacheBase)

	// 1. Corrupt entry files -> must re-run (never crash, never pass from cache).
	corruptDirFiles(t, filepath.Join(repoCacheDir, "entries"), []byte("{truncated-entry"))
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")

	// 2. Corrupt dep files -> must re-run and heal.
	corruptDirFiles(t, filepath.Join(repoCacheDir, "deps"), []byte("\x00\xffbad-deps"))
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")

	// 3. Corrupt discovery files and filehashes.json -> must recover cleanly.
	corruptDirFiles(t, filepath.Join(repoCacheDir, "discovery"), []byte("null"))
	if err := os.WriteFile(filepath.Join(repoCacheDir, "filehashes.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")
}

func TestVitestKillMidRunRecoversCleanly(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
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

	// Start tc vitest in background and SIGKILL it once vitest is running (lock/runtime dir active).
	cmd := exec.Command(tcBin, "vitest")
	cmd.Dir = projDir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cacheBase := os.Getenv("TESTCACHE_DIR")
	waitForRunActivity(t, cacheBase)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// Next run must recover from stale lock files / partial temp dirs and succeed.
	assertVitestSummary(t, projDir, "tc: 9 files, 0 cached, 9 run")
	assertVitestSummary(t, projDir, "tc: 9 files, 7 cached, 2 run", string(tcFlagNoRevalidate))
}

func TestVitestVerboseTimingAndExitCode(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	t.Setenv("TC_VERBOSE", "1")

	code, out, errOut := runVitestFixture(t, projDir, false)
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
	phases := []string{"discover:", "hashing:", "lookup:", "run:", "persist:", "gc:"}
	for _, phase := range phases {
		if !strings.Contains(errOut, phase) {
			t.Fatalf("expected %q in cold verbose stderr:\n%s", phase, errOut)
		}
	}
	if strings.Contains(out, "[verbose]") {
		t.Fatalf("verbose logs must go to stderr, not stdout:\n%s", out)
	}

	// Warm run must also emit all verbose phase lines to stderr.
	code, out, errOut = runVitestFixture(t, projDir, false)
	if code != 0 {
		t.Fatalf("expected warm 0, got %d", code)
	}
	for _, phase := range phases {
		if !strings.Contains(errOut, phase) {
			t.Fatalf("expected %q in warm verbose stderr:\n%s", phase, errOut)
		}
	}
	if strings.Contains(out, "[verbose]") {
		t.Fatalf("warm verbose logs must go to stderr, not stdout:\n%s", out)
	}

	// Failing test must propagate Vitest's non-zero exit code.
	bPath := filepath.Join(projDir, "tests", "b.test.ts")
	origB, _ := os.ReadFile(bPath)
	writeFixtureFile(t, bPath, strings.ReplaceAll(string(origB), "toBe(12)", "toBe(999)"))
	code, _, _ = runVitestFixture(t, projDir, false, "tests/b.test.ts")
	if code == 0 {
		t.Fatalf("expected non-zero exit code for failing test")
	}
}

func singleRepoCacheDir(t *testing.T, cacheBase string) string {
	t.Helper()
	items, err := os.ReadDir(cacheBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.IsDir() {
			return filepath.Join(cacheBase, it.Name())
		}
	}
	t.Fatalf("no repo cache dir found under %s", cacheBase)
	return ""
}

func corruptDirFiles(t *testing.T, dir string, payload []byte) {
	t.Helper()
	items, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatalf("expected files in %s to corrupt", dir)
	}
	for _, it := range items {
		if !it.IsDir() {
			if err := os.WriteFile(filepath.Join(dir, it.Name()), payload, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func waitForRunActivity(t *testing.T, cacheBase string) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(8 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(cacheBase, "*", "runtime", "run-*", "cov"))
		if len(matches) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for vitest run activity under %s", cacheBase)
		case <-ticker.C:
		}
	}
}
