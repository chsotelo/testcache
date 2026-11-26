package vitest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVitestDiscoveryExcludesAndRefreshes(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv("TC_VERBOSE", "1")

	// Fixture has 9 real tests (7 cacheable + 2 uncacheable: spawn, network) + 1 excluded (tests/ignored.test.ts).
	code, out, errOut := runVitestFixture(t, projDir, false)
	if code != 0 || !strings.Contains(out, "tc: 9 files, 0 cached, 9 run") {
		t.Fatalf("cold discovery run code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "discover:") || !strings.Contains(errOut, "vitest list") {
		t.Fatalf("expected verbose vitest list timing in stderr:\n%s", errOut)
	}

	// Second run must hit discovery cache and cache the 7 cacheable files without invoking vitest list.
	code, out, errOut = runVitestFixture(t, projDir, false)
	if code != 0 || !strings.Contains(out, "tc: 9 files, 7 cached, 2 run") {
		t.Fatalf("warm discovery run code=%d out:\n%s\nerr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "(cache, 9 files)") {
		t.Fatalf("expected cached discovery in stderr:\n%s", errOut)
	}

	// Adding a new test file refreshes candidate list and runs the new file + 2 uncacheable files.
	newTest := filepath.Join(projDir, "tests", "c.test.ts")
	writeFixtureFile(t, newTest, "import { expect, it } from 'vitest';\nit('works', () => { expect(1).toBe(1); });\n")
	assertVitestSummary(t, projDir, "tc: 10 files, 7 cached, 3 run")

	// Removing it refreshes candidate list back to 7 cached, 2 uncacheable run.
	if err := os.Remove(newTest); err != nil {
		t.Fatal(err)
	}
	assertVitestSummary(t, projDir, "tc: 9 files, 7 cached, 2 run")
}

func TestExplainColdAndNodeChangeNeverStartsNode(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	poisonDir := t.TempDir()
	marker := filepath.Join(poisonDir, "node-was-called")
	writePoisonNode(t, poisonDir, marker)
	origPath := os.Getenv("PATH")

	// Cold explain with poison node first on PATH -> must not start node.
	t.Setenv("PATH", poisonDir+string(os.PathListSeparator)+origPath)
	coldEntries := mustExplain(t, projDir)
	if len(coldEntries) == 0 || coldEntries[0].Cached || coldEntries[0].Reason != "first run" {
		t.Fatalf("unexpected cold explain entries: %+v", coldEntries)
	}
	assertNoMarker(t, marker)

	// Warm with real PATH, then switch PATH to poison node -> reports node uncached/changed without calling node.
	t.Setenv("PATH", origPath)
	assertVitestSummary(t, projDir, "tc: 9 files, 0 cached, 9 run")
	t.Setenv("PATH", poisonDir+string(os.PathListSeparator)+origPath)
	assertExplainContains(t, projDir, "tests/a.test.ts", "node version changed or uncached")
	assertNoMarker(t, marker)
}

func TestExplainDiagnosesAllReasons(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	if orig, had := os.LookupEnv(envDynamicVar); had {
		_ = os.Unsetenv(envDynamicVar)
		t.Cleanup(func() { _ = os.Setenv(envDynamicVar, orig) })
	}
	assertVitestSummary(t, projDir, "tc: 9 files, 0 cached, 9 run")

	// (a) No change -> 7 cacheable cached, 2 uncacheable uncached, ignored.test.ts excluded by vitest config.
	assertWarmExplainResults(t, projDir)
	exc := mustExplain(t, projDir, "tests/ignored.test.ts")
	if len(exc) != 1 || exc[0].Cached || !strings.Contains(exc[0].Reason, "excluded by vitest config") {
		t.Fatalf("unexpected excluded explain: %+v", exc)
	}

	// (b) Edit source dependency -> dependency changed with hash prefixes.
	assertExplainAfterSourceEdit(t, projDir)

	// (c) Change dynamic env var -> env var changed.
	t.Setenv(envDynamicVar, "altered")
	assertExplainContains(t, projDir, "tests/env.test.ts", "env var changed: "+envDynamicVar)
	t.Setenv(envDynamicVar, "")
	_ = os.Unsetenv(envDynamicVar)

	// (d) New file vs missing file, config/lockfile change, testcache exclude, previous run failed.
	assertExplainFSAuditReasons(t, projDir)
	assertExplainConfigAndFailureReasons(t, projDir)
}

func assertWarmExplainResults(t *testing.T, projDir string) {
	t.Helper()
	entries := mustExplain(t, projDir)
	if len(entries) != 9 {
		t.Fatalf("expected 9 explain entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		switch e.Path {
		case "tests/spawn.test.ts":
			if e.Cached || !strings.Contains(e.Reason, "spawned a process:") {
				t.Fatalf("expected spawn.test.ts uncacheable, got %+v", e)
			}
		case "tests/network.test.ts":
			if e.Cached || !strings.Contains(e.Reason, "network access:") {
				t.Fatalf("expected network.test.ts uncacheable, got %+v", e)
			}
		default:
			if !e.Cached {
				t.Fatalf("expected %s cached, got %+v", e.Path, e)
			}
		}
	}
}

func assertExplainAfterSourceEdit(t *testing.T, projDir string) {
	t.Helper()
	path := filepath.Join(projDir, "src", "usedByA.ts")
	orig, _ := os.ReadFile(path)
	writeFixtureFile(t, path, string(orig)+"\nexport const extra = 1;\n")
	assertExplainContains(t, projDir, "tests/a.test.ts", "dependency changed: src/usedByA.ts", "→")
	writeFixtureFile(t, path, string(orig))
}

func assertExplainFSAuditReasons(t *testing.T, projDir string) {
	t.Helper()
	optFlag := filepath.Join(projDir, "data", "optional-flag.txt")
	writeFixtureFile(t, optFlag, "on\n")
	assertExplainContains(t, projDir, "tests/fs.test.ts", "new file: data/optional-flag.txt", "missing→")
	_ = os.Remove(optFlag)

	themeCSS := filepath.Join(projDir, "data", "theme.css")
	origTheme, _ := os.ReadFile(themeCSS)
	_ = os.Remove(themeCSS)
	assertExplainContains(t, projDir, "tests/fs.test.ts", "missing file: data/theme.css", "→missing")
	writeFixtureFile(t, themeCSS, string(origTheme))
}

func assertExplainConfigAndFailureReasons(t *testing.T, projDir string) {
	t.Helper()
	cfgPath := filepath.Join(projDir, "vitest.config.ts")
	origCfg, _ := os.ReadFile(cfgPath)
	writeFixtureFile(t, cfgPath, string(origCfg)+"\n// changed\n")
	assertExplainContains(t, projDir, "tests/a.test.ts", "config/lockfile changed: vitest.config.ts")
	writeFixtureFile(t, cfgPath, string(origCfg))

	lockPath := filepath.Join(projDir, "package-lock.json")
	origLock, _ := os.ReadFile(lockPath)
	writeFixtureFile(t, lockPath, string(origLock)+" ")
	assertExplainContains(t, projDir, "tests/a.test.ts", "config/lockfile changed: package-lock.json")
	writeFixtureFile(t, lockPath, string(origLock))

	tcCfgPath := filepath.Join(projDir, "testcache.json")
	writeFixtureFile(t, tcCfgPath, `{"exclude":["tests/a.test.ts"]}`)
	assertExplainContains(t, projDir, "tests/a.test.ts", "excluded by testcache.json")
	_ = os.Remove(tcCfgPath)

	bPath := filepath.Join(projDir, "tests", "b.test.ts")
	origB, _ := os.ReadFile(bPath)
	writeFixtureFile(t, bPath, strings.ReplaceAll(string(origB), "toBe(12)", "toBe(999)"))
	code, _, _ := runVitestFixture(t, projDir, false, "tests/b.test.ts")
	if code == 0 {
		t.Fatalf("expected failing b.test.ts")
	}
	assertExplainContains(t, projDir, "tests/b.test.ts", "previous run failed")
}

func writePoisonNode(t *testing.T, dir, marker string) {
	t.Helper()
	script := "#!/bin/sh\ntouch \"" + marker + "\"\nexit 99\n"
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertNoMarker(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("explain must never start node, but marker %s exists", marker)
	}
}

func mustExplain(t *testing.T, projDir string, args ...string) []ExplainEntry {
	t.Helper()
	entries, err := New().Explain(context.Background(), ExplainOptions{WorkDir: projDir, Args: args})
	if err != nil {
		t.Fatalf("Explain(%v): %v", args, err)
	}
	return entries
}

func assertExplainContains(t *testing.T, projDir, target string, wants ...string) {
	t.Helper()
	entries := mustExplain(t, projDir, target)
	if len(entries) != 1 || entries[0].Cached {
		t.Fatalf("expected single uncached explain for %s, got %+v", target, entries)
	}
	for _, w := range wants {
		if !strings.Contains(entries[0].Reason, w) {
			t.Fatalf("explain(%s) reason=%q missing %q", target, entries[0].Reason, w)
		}
	}
}
