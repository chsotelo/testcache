package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
)

const (
	envScriptShellNpm  = "npm_config_script_shell"
	envScriptShellPnpm = "pnpm_config_script_shell"
	pretestMarker      = "pretest.marker"
	// e2eFileAge backdates the copied fixture so tc does not treat it as just edited.
	e2eFileAge = time.Minute
)

// builtTC is a real tc binary with its tc-shell link, as scripts/install.sh lays them out.
type builtTC struct {
	tc    string
	shell string
}

func buildTC(t *testing.T) builtTC {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := builtTC{tc: filepath.Join(dir, programName), shell: filepath.Join(dir, shellShimName)}
	if out, err := exec.Command("go", "build", "-o", bin.tc, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if err := os.Symlink(programName, bin.shell); err != nil {
		t.Fatal(err)
	}
	return bin
}

// shellResult is what one script-shell call produced.
type shellResult struct {
	stdout, stderr string
	status         syscall.WaitStatus
}

type shellCall struct {
	args  []string
	dir   string
	stdin string
	env   []string
}

func (b builtTC) runShell(t *testing.T, call shellCall) shellResult {
	t.Helper()
	cmd := exec.Command(b.shell, call.args...)
	cmd.Dir = call.dir
	cmd.Stdin = strings.NewReader(call.stdin)
	cmd.Env = append(os.Environ(), call.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("start %s: %v", b.shell, err)
	}
	return shellResult{stdout: stdout.String(), stderr: stderr.String(), status: cmd.ProcessState.Sys().(syscall.WaitStatus)}
}

func TestScriptShellPassThroughIsTheRealShell(t *testing.T) {
	bin := buildTC(t)
	p := newFakeProject(t, 0)
	script := filepath.Join(filepath.Dir(p.dir), "script.sh")
	if err := os.WriteFile(script, []byte("echo from-script \"$1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Like npm and pnpm, put the project's node_modules/.bin first on PATH.
	withBin := "PATH=" + filepath.Join(p.dir, "node_modules", ".bin") + string(os.PathListSeparator) + os.Getenv("PATH")

	cases := []struct {
		name       string
		call       shellCall
		wantStdout string
		wantStderr string
		wantExit   int
	}{
		{"exit code", shellCall{args: []string{"-c", "exit 7"}}, "", "", 7},
		{"stdout and stderr", shellCall{args: []string{"-c", "echo out; echo err >&2"}}, "out\n", "err\n", 0},
		{"stdin", shellCall{args: []string{"-c", "cat"}, stdin: "piped in\n"}, "piped in\n", "", 0},
		{"argv", shellCall{args: []string{"-c", `echo "$0|$1|$#"`, "zero", "one"}}, "zero|one|1\n", "", 0},
		{"argv0 is the shell", shellCall{args: []string{"-c", `echo "$0"`}}, "sh\n", "", 0},
		{"environment", shellCall{args: []string{"-c", `echo "$TC_E2E_VALUE"`}, env: []string{"TC_E2E_VALUE=kept"}}, "kept\n", "", 0},
		{"working directory", shellCall{args: []string{"-c", "pwd"}, dir: p.dir}, p.dir + "\n", "", 0},
		{"script file instead of -c", shellCall{args: []string{script, "arg"}}, "from-script arg\n", "", 0},
		{"command not found", shellCall{args: []string{"-c", "tc-e2e-no-such-command 2>/dev/null"}}, "", "", 127},
		{"vitest watch stays with vitest", shellCall{args: []string{"-c", "vitest"}, dir: p.dir, env: []string{withBin}}, fakeVitestBanner + "\n", "", 0},
		{"compound command", shellCall{args: []string{"-c", "vitest run && echo after"}, dir: p.dir, env: []string{withBin}}, fakeVitestBanner + "\nafter\n", "", 0},
		{"TC_DISABLE", shellCall{args: []string{"-c", "vitest run"}, dir: p.dir, env: []string{withBin, config.EnvDisable + "=1"}}, fakeVitestBanner + "\n", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bin.runShell(t, tc.call)
			if got.stdout != tc.wantStdout || got.stderr != tc.wantStderr || got.status.ExitStatus() != tc.wantExit {
				t.Fatalf("stdout=%q stderr=%q exit=%d\n want stdout=%q stderr=%q exit=%d",
					got.stdout, got.stderr, got.status.ExitStatus(), tc.wantStdout, tc.wantStderr, tc.wantExit)
			}
		})
	}
	if runs := p.readLog(t); len(runs) != 0 {
		t.Fatalf("a pass-through never reaches tc, but the log has %d lines", len(runs))
	}
}

func TestScriptShellPassThroughPreservesSignals(t *testing.T) {
	bin := buildTC(t)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGKILL} {
		got := bin.runShell(t, shellCall{args: []string{"-c", "kill -" + signalName(sig) + " $$"}})
		if !got.status.Signaled() || got.status.Signal() != sig {
			t.Errorf("the caller must see death by %v, got status %v", sig, got.status)
		}
	}
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGKILL:
		return "KILL"
	default:
		return "TERM"
	}
}

// copyFixtureProject makes a private copy of testdata/fixture-vitest whose package.json has
// pretest/test/posttest scripts. node_modules is a directory of links into the fixture, so a
// package manager that decided to touch it could not damage the fixture itself.
// e2eFixtureDir is the fixture under testdata to run against (TC_TEST_FIXTURE, as in the
// runner tests), so the suite can be pointed at another Vitest major.
func e2eFixtureDir() string {
	if name := os.Getenv("TC_TEST_FIXTURE"); name != "" {
		return name
	}
	return "fixture-vitest"
}

func copyFixtureProject(t *testing.T) string {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", e2eFixtureDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture, "node_modules", "vitest", "vitest.mjs")); err != nil {
		t.Skipf("fixture dependencies are not installed: %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "proj")
	for _, rel := range []string{"vitest.config.ts", "src/usedByA.ts", "src/usedByB.ts", "src/onlyImported.ts", "tests/a.test.ts", "tests/b.test.ts"} {
		data, err := os.ReadFile(filepath.Join(fixture, rel))
		if err != nil {
			t.Fatal(err)
		}
		writeAgedFile(t, filepath.Join(proj, rel), data)
	}
	manifest := `{"name":"fixture-e2e","private":true,"type":"module","scripts":{` +
		`"pretest":"echo ran > ` + pretestMarker + `","test":"vitest run","posttest":"echo POSTTEST-RAN && true"}}`
	writeAgedFile(t, filepath.Join(proj, "package.json"), []byte(manifest))

	modules := filepath.Join(proj, "node_modules")
	if err := os.MkdirAll(filepath.Join(modules, ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(modules, "vitest"):         filepath.Join(fixture, "node_modules", "vitest"),
		filepath.Join(modules, "vite"):           filepath.Join(fixture, "node_modules", "vite"),
		filepath.Join(modules, ".bin", "vitest"): filepath.Join("..", "vitest", "vitest.mjs"),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	return proj
}

func writeAgedFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-e2eFileAge)
	for _, p := range []string{path, filepath.Dir(path)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// packageManagerEnv is the environment for running a package manager offline with tc-shell
// as its script shell. pnpm 11 reads pnpm_config_*, npm and pnpm <= 10 read npm_config_*.
func packageManagerEnv(bin builtTC, cacheDir string) []string {
	return append(os.Environ(),
		envScriptShellNpm+"="+bin.shell,
		envScriptShellPnpm+"="+bin.shell,
		"TESTCACHE_DIR="+cacheDir,
		"TESTCACHE_CONFIG_DIR="+filepath.Join(cacheDir, "no-user-config"),
		"PATH="+filepath.Dir(bin.tc)+string(os.PathListSeparator)+os.Getenv("PATH"),
		// Nothing may be downloaded or installed: pnpm 11 would otherwise run an install first.
		"COREPACK_ENABLE_NETWORK=0",
		"pnpm_config_verify_deps_before_run=false",
		"npm_config_update_notifier=false",
		"npm_config_offline=true",
		"CI=1",
		runlog.EnvInvokedBy+"=",
		config.EnvDisable+"=",
	)
}

// findPackageManagers returns the package managers that work offline in dir.
func findPackageManagers(env []string, dir string) []string {
	var found []string
	for _, name := range []string{"pnpm", "npm"} {
		probe := exec.Command(name, "--version")
		probe.Dir, probe.Env = dir, env
		if probe.Run() == nil {
			found = append(found, name)
		}
	}
	return found
}

func TestPackageScriptIsServedByTC(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	bin := buildTC(t)
	managers := findPackageManagers(packageManagerEnv(bin, t.TempDir()), t.TempDir())
	if len(managers) == 0 {
		t.Skip("neither pnpm nor npm works offline here")
	}
	for _, manager := range managers {
		t.Run(manager+" test", func(t *testing.T) {
			proj := copyFixtureProject(t)
			cacheDir := filepath.Join(filepath.Dir(proj), "cache")
			env := packageManagerEnv(bin, cacheDir)
			// A sampled revalidation would turn a warm hit into a run: the warm run must be exact.
			userConfig := filepath.Join(cacheDir, "no-user-config")
			if err := os.MkdirAll(userConfig, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(userConfig, "config.json"), []byte(`{"revalidateRate":0}`), 0o644); err != nil {
				t.Fatal(err)
			}

			cold := runPackageTest(t, packageRun{manager: manager, dir: proj, env: env})
			if !strings.Contains(cold, "tc: 2 files, 0 cached, 2 run") || !strings.Contains(cold, "POSTTEST-RAN") {
				t.Fatalf("the test script must run through tc and posttest must follow:\n%s", cold)
			}
			if data, err := os.ReadFile(filepath.Join(proj, pretestMarker)); err != nil || strings.TrimSpace(string(data)) != "ran" {
				t.Fatalf("the pretest script must still run through the real shell: %q %v", data, err)
			}
			warm := runPackageTest(t, packageRun{manager: manager, dir: proj, env: env})
			if !strings.Contains(warm, "tc: 2 files, 2 cached, 0 run") {
				t.Fatalf("the second run must be served from the cache:\n%s", warm)
			}

			assertScriptShellRuns(t, cacheDir, proj)

			disabled := runPackageTest(t, packageRun{manager: manager, dir: proj, env: append(env, config.EnvDisable+"=1")})
			if strings.Contains(disabled, "tc:") || !strings.Contains(disabled, "2 passed") {
				t.Fatalf("TC_DISABLE=1 must give plain vitest:\n%s", disabled)
			}
			if after, _ := runlog.OpenLog(cacheDir).ReadAll(); len(after) != 2 {
				t.Fatalf("a disabled script shell must not reach tc, log has %d lines", len(after))
			}
		})
	}
}

// assertScriptShellRuns checks that the two test runs each left one script-shell log line.
func assertScriptShellRuns(t *testing.T, cacheDir, proj string) {
	t.Helper()
	runs, err := runlog.OpenLog(cacheDir).ReadAll()
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected one log line per test run, got %d (err=%v)", len(runs), err)
	}
	for _, r := range runs {
		rec := r.Record
		if rec.InvokedBy != runlog.InvokerScriptShell || rec.Command != "vitest" || rec.TCExit != 0 || rec.Cwd != proj {
			t.Fatalf("unexpected log line: %s", r.Raw)
		}
		if strings.Join(rec.Argv, " ") != "tc vitest run" || rec.Files.Discovered != 2 {
			t.Fatalf("unexpected log line: %s", r.Raw)
		}
	}
}

type packageRun struct {
	manager string
	dir     string
	env     []string
}

func runPackageTest(t *testing.T, run packageRun) string {
	t.Helper()
	cmd := exec.Command(run.manager, "test")
	cmd.Dir, cmd.Env = run.dir, run.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s test: %v\n%s", run.manager, err, out)
	}
	return string(out)
}

func TestClaudeHookSubcommand(t *testing.T) {
	bin := buildTC(t)
	p := newFakeProject(t, 0)
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": filepath.Dir(p.dir),
		"tool_input": map[string]any{"command": "cd proj && npx vitest run a.test.ts", "timeout": 5000},
	})
	if err != nil {
		t.Fatal(err)
	}
	runHook := func(env ...string) string {
		cmd := exec.Command(bin.tc, string(cmdClaudeHook))
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("the hook must exit 0: %v", err)
		}
		return string(out)
	}

	got := runHook()
	var response struct {
		HookSpecificOutput struct {
			HookEventName string         `json:"hookEventName"`
			UpdatedInput  map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &response); err != nil || strings.Count(got, "\n") != 1 {
		t.Fatalf("the hook must print one line of JSON: %v\n%s", err, got)
	}
	updated := response.HookSpecificOutput.UpdatedInput
	wantCommand := "cd proj && TC_INVOKED_BY=claude-hook tc vitest run a.test.ts"
	if response.HookSpecificOutput.HookEventName != "PreToolUse" || updated["command"] != wantCommand || updated["timeout"] != float64(5000) {
		t.Fatalf("unexpected hook output: %s", got)
	}
	if got := runHook(config.EnvDisable + "=1"); got != "" {
		t.Fatalf("TC_DISABLE=1 must silence the hook, got %q", got)
	}

	garbage := exec.Command(bin.tc, string(cmdClaudeHook))
	garbage.Stdin = strings.NewReader("not json")
	if out, err := garbage.Output(); err != nil || len(out) != 0 {
		t.Fatalf("a hook that cannot parse its input stays out of the way: out=%q err=%v", out, err)
	}
}
