package integrate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/config"
)

func TestPlanScriptShellRewrites(t *testing.T) {
	cases := []struct {
		command  string
		wantArgs []string
		wantEnv  []string
	}{
		{"vitest run", []string{"run"}, nil},
		{"vitest run tests/a.test.ts", []string{"run", "tests/a.test.ts"}, nil},
		{"vitest --run", []string{"--run"}, nil},
		{"vitest --run tests/a.test.ts -t adds", []string{"--run", "tests/a.test.ts", "-t", "adds"}, nil},
		{"vitest tests/a.test.ts --run", []string{"tests/a.test.ts", "--run"}, nil},
		{"  vitest   run  ", []string{"run"}, nil},
		{"vitest run 'a b.test.ts' --x", []string{"run", "a b.test.ts", "--x"}, nil},
		{`vitest run "a b.test.ts"`, []string{"run", "a b.test.ts"}, nil},
		{`vitest run -t "adds \"two\""`, []string{"run", "-t", `adds "two"`}, nil},
		{`vitest run a\ b.test.ts`, []string{"run", "a b.test.ts"}, nil},
		{"vitest run -- 'a b' --x", []string{"run", "--", "a b", "--x"}, nil},
		{"CI=1 vitest run", []string{"run"}, []string{"CI=1"}},
		{"CI=1 TZ='Europe/Madrid' vitest run a", []string{"run", "a"}, []string{"CI=1", "TZ=Europe/Madrid"}},
		{"cross-env CI=1 TZ=UTC vitest run", []string{"run"}, []string{"CI=1", "TZ=UTC"}},
		{"cross-env vitest run", []string{"run"}, nil},
		{"A=1 cross-env B=2 vitest --run", []string{"--run"}, []string{"A=1", "B=2"}},
		{"npx vitest run a", []string{"run", "a"}, nil},
		{"pnpm exec vitest run", []string{"run"}, nil},
		{"pnpm vitest run", []string{"run"}, nil},
		{"yarn vitest run", []string{"run"}, nil},
		{"./node_modules/.bin/vitest run", []string{"run"}, nil},
		{"vitest run --coverage", []string{"run", "--coverage"}, nil},
		{"vitest run --reporter=dot --maxWorkers=2", []string{"run", "--reporter=dot", "--maxWorkers=2"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			plan := PlanScriptShell([]string{"-c", tc.command})
			if !plan.Rewrite {
				t.Fatalf("expected a rewrite")
			}
			if !reflect.DeepEqual(plan.VitestArgs, tc.wantArgs) {
				t.Errorf("args: expected %q, got %q", tc.wantArgs, plan.VitestArgs)
			}
			if !reflect.DeepEqual(plan.Env, tc.wantEnv) {
				t.Errorf("env: expected %q, got %q", tc.wantEnv, plan.Env)
			}
		})
	}
}

func TestPlanScriptShellPassesThrough(t *testing.T) {
	commands := []string{
		"", "echo hello", "tsc --noEmit", "node scripts/vitest-wrapper.js run",
		// Watch, UI and other subcommands.
		"vitest", "vitest tests/a.test.ts", "vitest watch", "vitest dev", "vitest --ui", "vitest run --ui",
		"vitest run --watch", "vitest run -w", "vitest --run --watch=true", "vitest bench", "vitest bench --run",
		"vitest list", "vitest related src/a.ts --run", "vitest typecheck", "vitest init",
		// Compound commands and anything the shell must interpret.
		"vitest run && echo done", "tsc && vitest run", "vitest run; echo done", "vitest run | tee out.txt",
		"vitest run || true", "vitest run &", "vitest run > out.txt", "vitest run 2>&1", "vitest run < in.txt",
		"vitest run\necho done", "(vitest run)", "{ vitest run; }", "vitest run $FILES", `vitest run "$FILES"`,
		"vitest run $(git diff --name-only)", "vitest run `git diff --name-only`", "vitest run tests/*.test.ts",
		"vitest run tests/a?.test.ts", "vitest run tests/[ab].test.ts", "vitest run ~/proj/a.test.ts",
		"FOO=$BAR vitest run", "FOO=~/x vitest run", "vitest run # all tests", "vitest run 'unterminated",
		`vitest run "unterminated`, "vitest run <<EOF\nx\nEOF", "vitest run \\\n a.test.ts",
		// Vitest reached through something else.
		"time vitest run", "exec vitest run", "env CI=1 vitest run", "nyc vitest run", "dotenv -- vitest run",
		"npx vitest@4 run", "npm exec vitest run", "bunx vitest run", "vitest-cli run", "jest run",
		"cross-env-shell CI=1 vitest run", "rtk vitest run", "rtk proxy vitest run", "tc vitest run",
		"echo vitest run",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if plan := PlanScriptShell([]string{"-c", command}); plan.Rewrite {
				t.Fatalf("must be passed to the real shell untouched, got %+v", plan)
			}
		})
	}
}

func TestPlanScriptShellOnlyAcceptsDashC(t *testing.T) {
	for _, args := range [][]string{
		nil, {"-c"}, {"vitest run"}, {"script.sh"}, {"-c", "vitest run", "extra"}, {"-e", "vitest run"}, {"-lc", "vitest run"},
	} {
		if plan := PlanScriptShell(args); plan.Rewrite {
			t.Errorf("args %q must go to the real shell", args)
		}
	}
}

func TestPlanScriptShellHonoursDisable(t *testing.T) {
	args := []string{"-c", "vitest run"}
	t.Setenv(config.EnvDisable, "1")
	if PlanScriptShell(args).Rewrite {
		t.Fatal("TC_DISABLE=1 must pass everything through")
	}
	t.Setenv(config.EnvDisable, "0")
	if !PlanScriptShell(args).Rewrite {
		t.Fatal("TC_DISABLE=0 leaves the integration on")
	}
}

const (
	hookCwd       = "/work/repo"
	hookPrefix    = "TC_INVOKED_BY=claude-hook "
	hookVitestDir = "/work/repo/apps/web"
)

// newTestRewriter knows Vitest in the session directory and in one package below it.
func newTestRewriter() HookRewriter {
	return HookRewriter{HasVitest: func(dir string) bool { return dir == hookCwd || dir == hookVitestDir }}
}

func TestRewriteCommandRewrites(t *testing.T) {
	cases := []struct{ command, want string }{
		{"npx vitest run", hookPrefix + "tc vitest run"},
		{"npx vitest run tests/a.test.ts -t 'adds two'", hookPrefix + "tc vitest run tests/a.test.ts -t 'adds two'"},
		{"pnpm exec vitest run a", hookPrefix + "tc vitest run a"},
		{"pnpm vitest run a", hookPrefix + "tc vitest run a"},
		{"yarn vitest run a", hookPrefix + "tc vitest run a"},
		{"./node_modules/.bin/vitest run a", hookPrefix + "tc vitest run a"},
		{"node_modules/.bin/vitest run a", hookPrefix + "tc vitest run a"},
		{"vitest run a", hookPrefix + "tc vitest run a"},
		{"vitest --run a", hookPrefix + "tc vitest --run a"},
		{"npx vitest a --run", hookPrefix + "tc vitest a --run"},
		{"CI=1 npx vitest run", hookPrefix + "CI=1 tc vitest run"},
		{"cross-env CI=1 vitest run", hookPrefix + "cross-env CI=1 tc vitest run"},
		// After cd, with the directory followed from the session cwd.
		{"cd apps/web && npx vitest run", "cd apps/web && " + hookPrefix + "tc vitest run"},
		{"cd /work/repo/apps/web && pnpm vitest run a", "cd /work/repo/apps/web && " + hookPrefix + "tc vitest run a"},
		{"cd apps && cd web && vitest run", "cd apps && cd web && " + hookPrefix + "tc vitest run"},
		{"cd 'apps/web'; npx vitest run", "cd 'apps/web'; " + hookPrefix + "tc vitest run"},
		// Already prefixed by rtk.
		{"rtk proxy npx vitest run a", hookPrefix + "rtk proxy tc vitest run a"},
		{"rtk proxy vitest run", hookPrefix + "rtk proxy tc vitest run"},
		{"rtk vitest a", hookPrefix + "tc vitest a"},
		{"rtk vitest run a", hookPrefix + "tc vitest run a"},
		{"cd apps/web && rtk vitest", "cd apps/web && " + hookPrefix + "tc vitest"},
		// Only the launcher changes: the rest of the line is the shell's business.
		{"npx vitest run 2>&1 | tail -20", hookPrefix + "tc vitest run 2>&1 | tail -20"},
		{"npx vitest run $FILES > out.txt", hookPrefix + "tc vitest run $FILES > out.txt"},
		{"npx vitest run tests/*.test.ts", hookPrefix + "tc vitest run tests/*.test.ts"},
		{"npm run build && npx vitest run && echo ok", "npm run build && " + hookPrefix + "tc vitest run && echo ok"},
		{"npx vitest run a; npx vitest run b", hookPrefix + "tc vitest run a; " + hookPrefix + "tc vitest run b"},
		{"export CI=1\nnpx vitest run", "export CI=1\n" + hookPrefix + "tc vitest run"},
	}
	rewriter := newTestRewriter()
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, changed := rewriter.RewriteCommand(tc.command, hookCwd)
			if !changed || got != tc.want {
				t.Fatalf("expected %q, got %q (changed=%v)", tc.want, got, changed)
			}
			if again, changedAgain := rewriter.RewriteCommand(got, hookCwd); changedAgain {
				t.Fatalf("the rewrite must be idempotent, second pass gave %q", again)
			}
		})
	}
}

func TestRewriteCommandLeavesAlone(t *testing.T) {
	commands := []string{
		"", "ls -la", "git status", "pnpm test", "npm test", "npm run test:unit", "echo 'npx vitest run'",
		`bash -c "npx vitest run"`, "grep -r vitest src",
		// Not a single run.
		"npx vitest", "npx vitest --watch", "npx vitest watch", "npx vitest --ui", "npx vitest run --watch",
		"vitest bench", "npx vitest list", "rtk vitest --watch", "rtk vitest watch", "rtk vitest --ui",
		// Already rewritten.
		"tc vitest run", hookPrefix + "tc vitest run a", hookPrefix + "rtk proxy tc vitest run a", "rtk proxy tc vitest run",
		"cd apps/web && " + hookPrefix + "tc vitest run",
		// Vitest reached through something this hook does not model.
		"timeout 60 npx vitest run", "time npx vitest run", "xargs npx vitest run", "npx vitest@4 run",
		"npx --no-install vitest run", "pnpm --filter web exec vitest run", "pnpm -C apps/web vitest run",
		"if npx vitest run; then echo ok; fi", "for f in a b; do npx vitest run $f; done", "! npx vitest run",
		// Constructs that are left to the shell untouched.
		"(cd apps/web && npx vitest run)", "npx vitest run $(git diff --name-only)", "npx vitest run `ls`",
		"{ npx vitest run; }", "cat <<EOF\nnpx vitest run\nEOF", "npx vitest run # comment", "npx vitest run 'open",
		// Directories where Vitest cannot be confirmed.
		"cd packages/api && npx vitest run", "cd $DIR && npx vitest run", "cd ~/x && npx vitest run",
		"cd - && npx vitest run", "cd && npx vitest run", "cd /elsewhere && npx vitest run",
		"cd $DIR && cd web && npx vitest run",
	}
	rewriter := newTestRewriter()
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if got, changed := rewriter.RewriteCommand(command, hookCwd); changed || got != command {
				t.Fatalf("must be left alone, got %q", got)
			}
		})
	}
	if _, changed := rewriter.RewriteCommand("npx vitest run", ""); changed {
		t.Fatal("without a session cwd nothing can be confirmed, so nothing is rewritten")
	}
	if _, changed := rewriter.RewriteCommand("cd $DIR && cd /work/repo && npx vitest run", hookCwd); !changed {
		t.Fatal("an absolute cd makes the directory known again")
	}
}

func runHook(t *testing.T, payload string) string {
	t.Helper()
	var out bytes.Buffer
	if err := newTestRewriter().RunClaudeHook(strings.NewReader(payload), &out); err != nil {
		t.Fatalf("hook error: %v", err)
	}
	return out.String()
}

func buildHookPayload(tool, command string) string {
	payload := map[string]any{
		"session_id": "abc", "cwd": hookCwd, "hook_event_name": "PreToolUse", "tool_name": tool, "tool_use_id": "toolu_1",
		"tool_input": map[string]any{
			"command": command, "description": "Run tests", "timeout": 120000, "run_in_background": false,
		},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func TestRunClaudeHookEmitsTheDocumentedResponse(t *testing.T) {
	out := runHook(t, buildHookPayload("Bash", `cd apps/web && npx vitest run -t "a \"b\""`))

	var response map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	specific := response["hookSpecificOutput"]
	if len(response) != 1 || len(specific) != 2 {
		t.Fatalf("expected only hookSpecificOutput{hookEventName, updatedInput}, got %s", out)
	}
	if string(specific["hookEventName"]) != `"PreToolUse"` {
		t.Fatalf("unexpected hookEventName: %s", specific["hookEventName"])
	}
	var updated map[string]any
	if err := json.Unmarshal(specific["updatedInput"], &updated); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"command":     `cd apps/web && ` + hookPrefix + `tc vitest run -t "a \"b\""`,
		"description": "Run tests", "timeout": float64(120000), "run_in_background": false,
	}
	if !reflect.DeepEqual(updated, want) {
		t.Fatalf("updatedInput must be the whole input with only the command changed:\n got %v\nwant %v", updated, want)
	}
}

func TestRunClaudeHookIsSilentOtherwise(t *testing.T) {
	payloads := map[string]string{
		"other tool":        buildHookPayload("Read", "npx vitest run"),
		"other command":     buildHookPayload("Bash", "git status"),
		"watch mode":        buildHookPayload("Bash", "npx vitest"),
		"already rewritten": buildHookPayload("Bash", hookPrefix+"tc vitest run"),
		"no tool input":     `{"tool_name":"Bash","cwd":"/work/repo"}`,
		"command not text":  `{"tool_name":"Bash","cwd":"/work/repo","tool_input":{"command":42}}`,
	}
	for name, payload := range payloads {
		if out := runHook(t, payload); out != "" {
			t.Errorf("%s: expected no output, got %q", name, out)
		}
	}
	var out bytes.Buffer
	if err := newTestRewriter().RunClaudeHook(strings.NewReader("not json"), &out); err == nil || out.Len() != 0 {
		t.Fatalf("garbage input must produce an error and no output, got err=%v out=%q", err, out.String())
	}
}

func TestRunClaudeHookHonoursDisable(t *testing.T) {
	t.Setenv(config.EnvDisable, "1")
	if out := runHook(t, buildHookPayload("Bash", "npx vitest run")); out != "" {
		t.Fatalf("TC_DISABLE=1 must make the hook a no-op, got %q", out)
	}
}

func TestSplitShellCommandWords(t *testing.T) {
	scan, ok := splitShellCommand(`A=1 cmd 'x y' "p q" r\ s 2>&1 $V *.ts && next | last`)
	if !ok || scan.operators != 2 || len(scan.segments) != 3 {
		t.Fatalf("unexpected scan: ok=%v %+v", ok, scan)
	}
	type word struct {
		text  string
		plain bool
	}
	var got []word
	for _, w := range scan.segments[0].words {
		got = append(got, word{w.text, w.plain})
	}
	want := []word{{"A=1", true}, {"cmd", true}, {"x y", true}, {"p q", true}, {"r s", true}, {"2>&1", false}, {"$V", false}, {"*.ts", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected words:\n got %+v\nwant %+v", got, want)
	}
}
