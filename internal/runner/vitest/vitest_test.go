package vitest

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/runner"
)

func TestParseArgs(t *testing.T) {
	p := ParseArgs([]string{"run", "--no-cache", "--root", "apps/web", "-t", "adds", "--reporter=json", "tests/a.test.ts"})
	if !p.NoCache || !p.HasReporter || p.BypassCache || p.RootDir != "apps/web" {
		t.Fatalf("unexpected flags: %+v", p)
	}
	for _, f := range p.ForwardFlags {
		if f == "--root" || f == "apps/web" {
			t.Fatalf("expected --root stripped from ForwardFlags (handled via cmd.Dir): %v", p.ForwardFlags)
		}
	}
	if len(p.Positionals) != 1 || p.Positionals[0] != "tests/a.test.ts" {
		t.Fatalf("unexpected positionals: %v", p.Positionals)
	}
	if len(p.CacheableFlags) != 1 || p.CacheableFlags[0] != "-t=adds" {
		t.Fatalf("unexpected cacheable flags: %v", p.CacheableFlags)
	}

	pWatch := ParseArgs([]string{"--watch", "tests/a.test.ts"})
	if !pWatch.BypassCache {
		t.Fatalf("expected --watch to bypass cache")
	}

	pSub := ParseArgs([]string{"bench", "tests/a.test.ts"})
	if !pSub.BypassCache {
		t.Fatalf("expected bench subcommand to bypass cache")
	}

	pIso := ParseArgs([]string{"--isolate=false", "--run", "-r", "apps/web", "tests/a.test.ts"})
	if !pIso.BypassCache || pIso.RootDir != "apps/web" {
		t.Fatalf("unexpected isolate/root flags: %+v", pIso)
	}
	for _, f := range pIso.ForwardFlags {
		if f == "--run" || f == "-r" || f == "apps/web" {
			t.Fatalf("did not expect %q in ForwardFlags: %v", f, pIso.ForwardFlags)
		}
	}
}

func TestResolveVitestToolingPnpmLayout(t *testing.T) {
	root := t.TempDir()
	pnpmVitest := filepath.Join(root, "node_modules", ".pnpm", "vitest@4.1.11", "node_modules", "vitest")
	pnpmVite := filepath.Join(root, "node_modules", ".pnpm", "vitest@4.1.11", "node_modules", "vite")
	if err := os.MkdirAll(filepath.Join(pnpmVitest, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pnpmVite, "dist", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pnpmVitest, "package.json"), []byte(`{"name":"vitest","version":"4.1.11"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pnpmVitest, "vitest.mjs"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pnpmVitest, "dist", "config.js"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pnpmVite, "package.json"), []byte(`{"name":"vite","version":"6.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pnpmVite, "dist", "node", "index.js"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(pnpmVitest, "vitest.mjs"), filepath.Join(root, "node_modules", ".bin", "vitest")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pnpmVitest, filepath.Join(root, "node_modules", "vitest")); err != nil {
		t.Fatal(err)
	}

	tools, err := resolveVitestTooling(root, root)
	if err != nil {
		t.Fatalf("resolveVitestTooling: %v", err)
	}
	if tools.vitestBin == "" || tools.vitestVersion != "4.1.11" || tools.viteEntry == "" || tools.vitestConfigEntry == "" {
		t.Fatalf("unexpected tooling resolution: %+v", tools)
	}
}

const fixtureFileAge = time.Minute

const (
	// envTestFixture selects the fixture project (a directory under testdata) the end-to-end
	// tests run against, so the same suite checks every supported Vitest major.
	envTestFixture     = "TC_TEST_FIXTURE"
	defaultTestFixture = "fixture-vitest"
)

func fixtureDirName() string {
	if name := os.Getenv(envTestFixture); name != "" {
		return name
	}
	return defaultTestFixture
}

func setupFixtureCopy(t *testing.T) (projDir, cacheDir string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	srcFixture := filepath.Join(repoRoot, "testdata", fixtureDirName())
	tmpRoot, err := os.MkdirTemp(filepath.Join(repoRoot, "testdata"), ".tmp-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })

	projDir = filepath.Join(tmpRoot, "proj")
	cacheDir = filepath.Join(tmpRoot, "cache")
	_ = os.MkdirAll(filepath.Join(projDir, "src"), 0o755)
	_ = os.MkdirAll(filepath.Join(projDir, "tests"), 0o755)
	_ = os.MkdirAll(cacheDir, 0o755)

	filesToCopy := []string{
		"package.json",
		"package-lock.json",
		"vitest.config.ts",
		filepath.Join("src", "usedByA.ts"),
		filepath.Join("src", "usedByB.ts"),
		filepath.Join("src", "onlyImported.ts"),
		filepath.Join("tests", "a.test.ts"),
		filepath.Join("tests", "b.test.ts"),
	}
	for _, rel := range filesToCopy {
		data, err := os.ReadFile(filepath.Join(srcFixture, rel))
		if err != nil {
			t.Fatalf("read fixture %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(projDir, rel), data, 0o644); err != nil {
			t.Fatalf("write copy %s: %v", rel, err)
		}
	}
	if err := os.Symlink(filepath.Join(srcFixture, "node_modules"), filepath.Join(projDir, "node_modules")); err != nil {
		t.Fatalf("symlink node_modules: %v", err)
	}

	ageFixtureTree(t, projDir)

	t.Setenv("TESTCACHE_DIR", cacheDir)
	t.Setenv("TESTCACHE_CONFIG_DIR", filepath.Join(tmpRoot, "usercfg"))
	return projDir, cacheDir
}

func runVitestFixture(t *testing.T, projDir string, noCache bool, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	r := New()
	code, err := r.RunTests(context.Background(), runner.RunOptions{
		WorkDir:      projDir,
		Args:         args,
		NoCache:      noCache,
		NoRevalidate: true,
		Stdout:       &out,
		Stderr:       &errBuf,
	})
	if err != nil {
		t.Fatalf("runner error: %v\nstderr: %s", err, errBuf.String())
	}
	return code, out.String(), errBuf.String()
}

func assertVitestSummary(t *testing.T, projDir, wantSummary string, args ...string) string {
	t.Helper()
	code, out, _ := runVitestFixture(t, projDir, false, args...)
	if code != 0 || !strings.Contains(out, wantSummary) {
		t.Fatalf("unexpected run result code=%d want=%q out:\n%s", code, wantSummary, out)
	}
	return out
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ageFixturePath(t, path)
	ageFixturePath(t, filepath.Dir(path))
}

// ageFixturePath backdates path so tc does not treat it as modified just before the run.
func ageFixturePath(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-fixtureFileAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// ageFixtureTree backdates every file and directory of a fresh fixture copy.
func ageFixtureTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		ageFixturePath(t, p)
		return nil
	})
	if err != nil {
		t.Fatalf("age fixture tree: %v", err)
	}
}

func TestVitestCacheEndToEnd(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	t.Setenv("TZ", "UTC")

	// 1. Cold run: 2 run, 0 cached.
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")

	// 2. Warm run: 2 cached, 0 run, prints (cached).
	start := time.Now()
	out := assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")
	if !strings.Contains(out, "(cached)") {
		t.Fatalf("warm run missing (cached) marker:\n%s", out)
	}
	t.Logf("warm hit duration for 2 files: %v", time.Since(start))

	// 3. Edit source file only imported (not executed) by A -> still 2 cached, 0 run.
	writeFixtureFile(t, filepath.Join(projDir, "src", "onlyImported.ts"),
		"export function unusedHelper(): string { return 'edited-still-unused'; }\n")
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")

	// 4. Edit source file executed by A -> re-runs A only, B stays cached.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"import { unusedHelper } from './onlyImported.js';\nexport function add(a: number, b: number): number {\n  if (a === -999999) return unusedHelper().length + b;\n  return a + b + 0;\n}\n")
	out = assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")
	if !strings.Contains(out, "tests/b.test.ts") || !strings.Contains(out, "(cached)") {
		t.Fatalf("expected b.test.ts cached in output:\n%s", out)
	}

	// 5. --no-cache forces a run.
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run", "--no-cache")

	// 6. Changing allowlisted env var invalidates.
	t.Setenv("TZ", "America/New_York")
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")

	// 7. Failing test is never cached.
	origB, err := os.ReadFile(filepath.Join(projDir, "tests", "b.test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(projDir, "tests", "b.test.ts"),
		strings.ReplaceAll(string(origB), "toBe(12)", "toBe(999)"))
	for i := 0; i < 2; i++ {
		code, out, _ := runVitestFixture(t, projDir, false, "tests/b.test.ts")
		if code == 0 || !strings.Contains(out, "tc: 1 file, 0 cached, 1 run") {
			t.Fatalf("failing run %d should not be cached, code=%d out:\n%s", i+1, code, out)
		}
	}
}

func TestVitestConcurrentDedup(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)

	const n = 3
	var wg sync.WaitGroup
	outs := make([]string, n)
	errs := make([]string, n)
	codes := make([]int, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			code, out, errOut := runVitestFixture(t, projDir, false, "tests/a.test.ts")
			codes[idx] = code
			outs[idx] = out
			errs[idx] = errOut
		}(i)
	}
	wg.Wait()

	runs := 0
	hits := 0
	for i := 0; i < n; i++ {
		if codes[i] != 0 {
			t.Fatalf("worker %d failed code=%d out=%s err=%s", i, codes[i], outs[i], errs[i])
		}
		if strings.Contains(outs[i], "1 cached, 0 run") {
			hits++
		} else if strings.Contains(outs[i], "0 cached, 1 run") {
			runs++
		}
	}
	if runs != 1 || hits != n-1 {
		t.Fatalf("expected 1 real run and %d deduped hits, got runs=%d hits=%d\nouts=%v", n-1, runs, hits, outs)
	}
}

func TestVitestExcludeAndFallback(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)

	cfgJSON := `{"exclude":["tests/b.test.ts"],"env":["CUSTOM_TC_VAR"]}`
	if err := os.WriteFile(filepath.Join(projDir, "testcache.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUSTOM_TC_VAR", "alpha")

	// Force V8 coverage missing on first run to test static import graph fallback.
	t.Setenv("TESTCACHE_DISABLE_V8_COVERAGE", "1")
	code, out, _ := runVitestFixture(t, projDir, false)
	if code != 0 {
		t.Fatalf("fallback cold run failed code=%d out:\n%s", code, out)
	}
	if !strings.Contains(out, "using static import graph fallback") {
		t.Fatalf("expected fallback message in output:\n%s", out)
	}
	if !strings.Contains(out, "tc: 2 files, 0 cached, 2 run") {
		t.Fatalf("unexpected summary:\n%s", out)
	}

	// Second run: a.test.ts is cached (with fallback marker), b.test.ts is excluded so it always runs.
	t.Setenv("TESTCACHE_DISABLE_V8_COVERAGE", "0")
	code, out, _ = runVitestFixture(t, projDir, false)
	if code != 0 || !strings.Contains(out, "tc: 2 files, 1 cached, 1 run") {
		t.Fatalf("expected 1 cached (a) and 1 excluded run (b), code=%d out:\n%s", code, out)
	}
	if !strings.Contains(out, "[fallback: static import graph]") {
		t.Fatalf("expected cached fallback indicator in output:\n%s", out)
	}

	// Changing custom env var invalidates a.test.ts.
	t.Setenv("CUSTOM_TC_VAR", "beta")
	code, out, _ = runVitestFixture(t, projDir, false)
	if code != 0 || !strings.Contains(out, "tc: 2 files, 0 cached, 2 run") {
		t.Fatalf("expected custom env change to invalidate, code=%d out:\n%s", code, out)
	}
}
