package vitest

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/runner"
)

func TestParseArgsChanged(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		since  string
		bypass bool
	}{
		{"run --changed", []string{"run", "--changed"}, "", false},
		{"a commit as the next word", []string{"run", "--changed", "HEAD~1"}, "HEAD~1", false},
		{"a commit after =", []string{"run", "--changed=origin/main"}, "origin/main", false},
		{"a flag is not the commit", []string{"--changed", "--run", "--passWithNoTests"}, "", false},
		{"without run it watches", []string{"--changed", "HEAD~1"}, "HEAD~1", true},
		{"test filters go to Vitest", []string{"run", "--changed", "HEAD", "tests/a"}, "HEAD", true},
		{"with related", []string{"related", "--run", "--changed", "src/a.ts"}, "src/a.ts", true},
		{"an empty value", []string{"run", "--changed="}, "", true},
		{"a boolean spelling", []string{"run", "--changed=false"}, "false", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseArgs(tc.args)
			if !p.Changed || !p.Related || p.ChangedSince != tc.since || p.BypassCache != tc.bypass {
				t.Fatalf("got changed=%v related=%v since=%q bypass=%v, want since=%q bypass=%v",
					p.Changed, p.Related, p.ChangedSince, p.BypassCache, tc.since, tc.bypass)
			}
			for _, f := range append(p.ForwardFlags, p.CacheableFlags...) {
				if strings.HasPrefix(f, "--changed") || f == tc.since && f != "" {
					t.Fatalf("--changed must be neither forwarded nor keyed: %v %v", p.ForwardFlags, p.CacheableFlags)
				}
			}
		})
	}
}

func TestChangedFilesMatchesVitestGitProvider(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := resolveRealPath(t.TempDir())
	proj := filepath.Join(repo, "web")
	write := func(rel, content string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("web/src/committed.ts", "1")
	write("web/src/staged.ts", "1")
	write("web/src/modified.ts", "1")
	write("web/src/stable.ts", "1")
	write(".gitignore", "*.log\n")
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "base")
	runGit(t, repo, "tag", "base")

	write("web/src/committed.ts", "2")
	runGit(t, repo, "commit", "-qam", "change")
	write("web/src/staged.ts", "2")
	runGit(t, repo, "add", "web/src/staged.ts")
	write("web/src/modified.ts", "2")
	write("web/src/new file.ts", "1")
	write("web/debug.log", "ignored")

	rel := func(files []string) string {
		var out []string
		for _, f := range files {
			r, _ := filepath.Rel(repo, f)
			out = append(out, filepath.ToSlash(r))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	got, err := changedFiles(context.Background(), proj, "base")
	if err != nil {
		t.Fatal(err)
	}
	want := "web/src/committed.ts,web/src/modified.ts,web/src/new file.ts,web/src/staged.ts"
	if rel(got) != want {
		t.Fatalf("since the base branch: got %s, want %s", rel(got), want)
	}

	got, err = changedFiles(context.Background(), proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "web/src/modified.ts,web/src/new file.ts,web/src/staged.ts"; rel(got) != want {
		t.Fatalf("without a commit: got %s, want %s", rel(got), want)
	}

	if _, err := changedFiles(context.Background(), proj, "no-such-ref"); err == nil {
		t.Fatal("an unknown commit must be an error, so Vitest reports it")
	}
}

// A pull request pushed several times: plain `--changed <base>` reruns every test affected
// since the base on each push, tc reruns only what changed since the last run.
func TestVitestChangedEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	projDir, _ := setupFixtureCopy(t)
	writeFixtureFile(t, filepath.Join(projDir, ".gitignore"), "node_modules\n")
	runGit(t, projDir, "init", "-q")
	runGit(t, projDir, "add", "-A")
	runGit(t, projDir, "commit", "-qm", "base")
	base := strings.TrimSpace(gitOut(t, projDir, "rev-parse", "HEAD"))
	changed := []string{"run", "--changed", base}

	// Nothing changed since the base: Vitest selects nothing, and neither does tc.
	assertVitestSummary(t, projDir, "tc: 0 files, 0 cached, 0 run", changed...)

	// Push 1, cold: only the affected test runs, not the whole suite.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"export function add(a: number, b: number): number {\n  return b + a;\n}\n")
	runGit(t, projDir, "commit", "-qam", "push 1")
	out := assertVitestSummary(t, projDir, "tc: 1 file, 0 cached, 1 run", changed...)
	if strings.Contains(out, "b.test.ts") {
		t.Fatalf("b.test.ts is not affected and must not run:\n%s", out)
	}

	// Push 2: both tests are affected since the base, only b.test.ts since push 1.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByB.ts"),
		"export function multiply(a: number, b: number): number {\n  return b * a;\n}\n")
	runGit(t, projDir, "commit", "-qam", "push 2")
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run", changed...)

	// Push 3, a comment only: both are affected, neither runs.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"// Adds two numbers.\nexport function add(a: number, b: number): number {\n  return b + a;\n}\n")
	runGit(t, projDir, "commit", "-qam", "push 3")
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run", changed...)

	// An uncommitted edit counts as changed, as in Vitest.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByB.ts"),
		"export function multiply(a: number, b: number): number {\n  return a * b * 1;\n}\n")
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run", changed...)

	// An unknown commit goes to Vitest, which selects nothing (Vitest 3 and 4) or fails
	// (Vitest 5): tc exits as Vitest does.
	unknown := []string{"run", "--changed", "no-such-ref"}
	code, out, _ := runVitestFixture(t, projDir, false, unknown...)
	plainCode, err := RunPlain(context.Background(), runner.RunOptions{
		WorkDir: projDir, Args: unknown, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil || code != plainCode || strings.Contains(out, "tc:") {
		t.Fatalf("an unknown commit: tc exit %d, Vitest exit %d (err %v), tc output:\n%s", code, plainCode, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
