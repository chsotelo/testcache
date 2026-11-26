package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/integrate"
	"github.com/chsotelo/testcache/internal/runlog"
)

const (
	envPreloadTC     = "TC_NODE_PRELOAD_TC"
	envPreloadActive = "TC_NODE_PRELOAD_ACTIVE"
	plainTwoPassed   = "2 passed"
	tcSummaryPrefix  = "tc:"
)

// preloadRun is one launch of the project's Vitest binary with the preload in NODE_OPTIONS.
type preloadRun struct {
	proj     string
	cacheDir string
	tcPath   string
	extraEnv []string
	args     []string
}

type preloadResult struct {
	stdout, stderr string
	exit           int
}

// writeNodePreload installs the embedded preload in a temporary directory, as install.sh does.
func writeNodePreload(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), integrate.NodePreloadFile)
	if err := os.WriteFile(path, integrate.NodePreloadSource(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runVitestBin starts node_modules/.bin/vitest the way `pnpm exec vitest`, `npx vitest` or
// a hand-typed path do: nothing in the command mentions tc.
func runVitestBin(t *testing.T, preload string, run preloadRun) preloadResult {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	// A sampled revalidation would turn a warm hit into a run: these runs must be exact.
	userConfig := filepath.Join(run.cacheDir, "no-user-config")
	if err := os.MkdirAll(userConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userConfig, "config.json"), []byte(`{"revalidateRate":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{filepath.Join(run.proj, "node_modules", ".bin", "vitest")}, run.args...)
	cmd := exec.Command("node", args...)
	cmd.Dir = run.proj
	cmd.Env = append(os.Environ(),
		integrate.EnvNodeOptions+"=--require "+preload,
		envPreloadTC+"="+run.tcPath,
		"TESTCACHE_DIR="+run.cacheDir,
		"TESTCACHE_CONFIG_DIR="+filepath.Join(run.cacheDir, "no-user-config"),
		"CI=1",
		envPreloadActive+"=",
		runlog.EnvInvokedBy+"=",
		config.EnvDisable+"=",
	)
	cmd.Env = append(cmd.Env, run.extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("start vitest: %v", err)
	}
	return preloadResult{stdout: stdout.String(), stderr: stderr.String(), exit: cmd.ProcessState.ExitCode()}
}

// A Vitest started without tc anywhere in the command is served by tc and logged as such.
func TestNodePreloadHandsVitestToTC(t *testing.T) {
	bin := buildTC(t)
	preload := writeNodePreload(t)
	proj := copyFixtureProject(t)
	run := preloadRun{proj: proj, cacheDir: filepath.Join(filepath.Dir(proj), "cache"), tcPath: bin.tc, args: []string{"run"}}

	cold := runVitestBin(t, preload, run)
	if cold.exit != 0 || !strings.Contains(cold.stdout, "tc: 2 files, 0 cached, 2 run") {
		t.Fatalf("the first run must go through tc:\n%s\n%s", cold.stdout, cold.stderr)
	}
	warm := runVitestBin(t, preload, run)
	if warm.exit != 0 || !strings.Contains(warm.stdout, "tc: 2 files, 2 cached, 0 run") {
		t.Fatalf("the second run must be served from the cache:\n%s\n%s", warm.stdout, warm.stderr)
	}
	runs, err := runlog.OpenLog(run.cacheDir).ReadAll()
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected one log line per run, got %d (err=%v)", len(runs), err)
	}
	for _, r := range runs {
		if r.Record.InvokedBy != runlog.InvokerNodePreload || strings.Join(r.Record.Argv, " ") != "tc vitest run" {
			t.Fatalf("unexpected log line: %s", r.Raw)
		}
	}

	run.extraEnv = []string{config.EnvDisable + "=1"}
	disabled := runVitestBin(t, preload, run)
	if strings.Contains(disabled.stdout, tcSummaryPrefix) || !strings.Contains(disabled.stdout, plainTwoPassed) {
		t.Fatalf("TC_DISABLE=1 must give plain Vitest:\n%s", disabled.stdout)
	}
}

// When tc cannot be reached the tests still run, and the failure is on stderr and in the log
// that `tc doctor` reads.
func TestNodePreloadFailureFallsBackAndIsLogged(t *testing.T) {
	preload := writeNodePreload(t)
	proj := copyFixtureProject(t)
	cacheDir := filepath.Join(filepath.Dir(proj), "cache")
	missing := filepath.Join(t.TempDir(), "no-such-tc")

	res := runVitestBin(t, preload, preloadRun{proj: proj, cacheDir: cacheDir, tcPath: missing, args: []string{"run"}})
	if res.exit != 0 || !strings.Contains(res.stdout, plainTwoPassed) || strings.Contains(res.stdout, tcSummaryPrefix) {
		t.Fatalf("plain Vitest must run when tc is missing:\n%s\n%s", res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "tc: node preload: tc is not runnable at "+missing) {
		t.Fatalf("the failure must be reported on stderr:\n%s", res.stderr)
	}
	logged, err := os.ReadFile(filepath.Join(cacheDir, integrate.NodePreloadErrorLog))
	if err != nil || !strings.Contains(string(logged), missing) || !strings.Contains(string(logged), proj) {
		t.Fatalf("the failure must be logged with the binary and the directory: %q %v", logged, err)
	}
	check := checkNodePreloadErrors(doctorScene{cacheDir: cacheDir})
	if check.level != doctorWarn || !strings.Contains(check.detail, "1 logged") {
		t.Fatalf("tc doctor must surface the logged failure: %+v", check)
	}
}

// A failing test file keeps Vitest's exit code through the preload.
func TestNodePreloadKeepsExitCode(t *testing.T) {
	bin := buildTC(t)
	preload := writeNodePreload(t)
	proj := copyFixtureProject(t)
	failing := "import { expect, it } from 'vitest';\nit('fails', () => { expect(1).toBe(2); });\n"
	writeAgedFile(t, filepath.Join(proj, "tests", "fail.test.ts"), []byte(failing))

	res := runVitestBin(t, preload, preloadRun{
		proj: proj, cacheDir: filepath.Join(filepath.Dir(proj), "cache"), tcPath: bin.tc,
		args: []string{"run", "tests/fail.test.ts"},
	})
	if res.exit != 1 || !strings.Contains(res.stdout, "tc: 1 file, 0 cached, 1 run") {
		t.Fatalf("want Vitest's exit code 1 through tc, got %d:\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
}

// Every other Node process only pays for loading the file, even one whose first argument is
// literally "vitest" (with -e, argv[1] is the first script argument).
func TestNodePreloadLeavesOtherNodeProcessesAlone(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	preload := writeNodePreload(t)
	cmd := exec.Command("node", "-e", "process.stdout.write(process.argv.slice(1).join(','))", "vitest", "run")
	cmd.Env = append(os.Environ(), integrate.EnvNodeOptions+"=--require "+preload, envPreloadTC+"=/no-such-tc", config.EnvDisable+"=")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "vitest,run" {
		t.Fatalf("a script that is not the Vitest CLI must run untouched: %q %v", out, err)
	}
}
