package vitest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureTarget string

const (
	targetFSTest   fixtureTarget = "tests/fs.test.ts"
	targetSnapTest fixtureTarget = "tests/snap.test.ts"
	targetEnvTest  fixtureTarget = "tests/env.test.ts"
	summaryOneHit  string        = "tc: 1 file, 1 cached, 0 run"
	summaryOneRun  string        = "tc: 1 file, 0 cached, 1 run"
	dirFixtureData string        = "data"
	envDynamicVar  string        = "TC_TEST_DYNAMIC_VAR"
)

type runExpectation struct {
	summarySubstr string
	wantFailure   bool
}

func setupFullFixtureCopy(t *testing.T) string {
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

	projDir := filepath.Join(tmpRoot, "proj")
	cacheDir := filepath.Join(tmpRoot, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFixtureTree(t, srcFixture, projDir)
	ageFixtureTree(t, projDir)

	t.Setenv("TESTCACHE_DIR", cacheDir)
	t.Setenv("TESTCACHE_CONFIG_DIR", filepath.Join(tmpRoot, "usercfg"))
	return projDir
}

func copyFixtureTree(t *testing.T, srcRoot, dstRoot string) {
	t.Helper()
	err := filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, p)
		if rel == "." {
			return os.MkdirAll(dstRoot, 0o755)
		}
		dst := filepath.Join(dstRoot, rel)
		if d.Name() == string(dirNodeModules) {
			if err := os.Symlink(p, dst); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture tree: %v", err)
	}
}

func assertRunOutcome(t *testing.T, projDir, target string, want runExpectation) {
	t.Helper()
	code, out, _ := runVitestFixture(t, projDir, false, target)
	if (!want.wantFailure && code != 0) || (want.wantFailure && code == 0) {
		t.Fatalf("target %s exit code=%d (wantFailure=%v) out:\n%s", target, code, want.wantFailure, out)
	}
	if !strings.Contains(out, want.summarySubstr) {
		t.Fatalf("target %s expected %q in output:\n%s", target, want.summarySubstr, out)
	}
}

func TestVitestFSReadInvalidation(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneHit})

	themePath := filepath.Join(projDir, dirFixtureData, "theme.css")
	if err := os.WriteFile(themePath, []byte(":root {\n  --accent: red;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestMissingFileAndDirInvalidation(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneHit})

	flagPath := filepath.Join(projDir, dirFixtureData, "optional-flag.txt")
	if err := os.WriteFile(flagPath, []byte("enabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})

	if err := os.Remove(flagPath); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneHit})

	extraItem := filepath.Join(projDir, dirFixtureData, "dir", "item2.txt")
	if err := os.WriteFile(extraItem, []byte("item2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetFSTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestSnapshotInvalidation(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	assertRunOutcome(t, projDir, string(targetSnapTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetSnapTest), runExpectation{summarySubstr: summaryOneHit})

	snapFile := filepath.Join(projDir, "tests", "__snapshots__", "snap.test.ts.snap")
	origSnap, err := os.ReadFile(snapFile)
	if err != nil {
		t.Fatal(err)
	}
	editedSnap := strings.ReplaceAll(string(origSnap), `"a": 1`, `"a": 2`)
	if err := os.WriteFile(snapFile, []byte(editedSnap), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetSnapTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})

	if err := os.WriteFile(snapFile, origSnap, 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetSnapTest), runExpectation{summarySubstr: summaryOneHit})

	rawSnap := filepath.Join(projDir, "tests", "snapshots", "file-snap.txt")
	if err := os.WriteFile(rawSnap, []byte("changed file snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, string(targetSnapTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestDynamicEnvInvalidation(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	if orig, had := os.LookupEnv(envDynamicVar); had {
		_ = os.Unsetenv(envDynamicVar)
		t.Cleanup(func() { _ = os.Setenv(envDynamicVar, orig) })
	}

	assertRunOutcome(t, projDir, string(targetEnvTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetEnvTest), runExpectation{summarySubstr: summaryOneHit})

	t.Setenv(envDynamicVar, "changed")
	assertRunOutcome(t, projDir, string(targetEnvTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})

	t.Setenv(envDynamicVar, "default-val")
	assertRunOutcome(t, projDir, string(targetEnvTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetEnvTest), runExpectation{summarySubstr: summaryOneHit})
}

func TestVitestCommentInsensitiveNormalization(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)

	// 1. Cold run on a.test.ts and b.test.ts.
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")

	// 2. Comment-only and multi-line whitespace edit to src/usedByA.ts -> stays cached.
	usedByAPath := filepath.Join(projDir, "src", "usedByA.ts")
	origUsedByA, err := os.ReadFile(usedByAPath)
	if err != nil {
		t.Fatal(err)
	}
	commentEdited := "// top comment\n" +
		strings.Replace(string(origUsedByA), "return a + b;", "/* inline comment */ return a + b;\n\n", 1) +
		"\n// trailing comment\n"
	writeFixtureFile(t, usedByAPath, commentEdited)
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")

	// 3. Editing a string in src/onlyImported.ts? No, edit a string inside executed src/usedByA.ts -> re-runs A only.
	stringEdited := strings.Replace(commentEdited, "return a + b;", "const tag = 'v2'; if (tag === 'nope') return 0;\n  return a + b;", 1)
	writeFixtureFile(t, usedByAPath, stringEdited)
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")

	// Warm again after string edit, then mutate the string literal value -> re-runs A only.
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run")
	writeFixtureFile(t, usedByAPath, strings.Replace(stringEdited, "'v2'", "'v3'", 1))
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")

	// 4. Adding // @vitest-environment jsdom to a test file -> must re-run (not replay cached pass).
	aTestPath := filepath.Join(projDir, "tests", "a.test.ts")
	origATest, err := os.ReadFile(aTestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, aTestPath, "// @vitest-environment jsdom\n"+string(origATest))
	code, out, _ := runVitestFixture(t, projDir, false, "tests/a.test.ts")
	if !strings.Contains(out, summaryOneRun) {
		t.Fatalf("expected @vitest-environment jsdom to force re-run, code=%d out:\n%s", code, out)
	}

	// 5. Restoring a.test.ts and disabling normalizeHash in testcache.json -> comment-only edit re-runs.
	writeFixtureFile(t, aTestPath, string(origATest))
	writeFixtureFile(t, filepath.Join(projDir, "testcache.json"), `{"normalizeHash":false}`)
	assertVitestSummary(t, projDir, "tc: 2 files, 0 cached, 2 run")
	writeFixtureFile(t, usedByAPath, string(origUsedByA)+"\n// raw-hash comment\n")
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")
}
