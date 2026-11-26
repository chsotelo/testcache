package vitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chsotelo/testcache/internal/cache"
)

// setupClonePair makes a git repository holding the fixture and a separate clone of it at
// another path, the way two CI runners each clone into their own directory.
func setupClonePair(t *testing.T) (first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	srcFixture := filepath.Join(repoRoot, "testdata", fixtureDirName())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first = filepath.Join(root, "runner-1", "repo")
	second = filepath.Join(root, "runner-2", "repo")
	copyFixtureTree(t, srcFixture, first)
	if err := os.WriteFile(filepath.Join(first, ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, first, "init", "-q")
	runGit(t, first, "add", "-A")
	runGit(t, first, "commit", "-q", "-m", "fixture")
	if err := os.MkdirAll(filepath.Dir(second), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(second), "clone", "-q", first, second)
	if err := os.Symlink(filepath.Join(srcFixture, string(dirNodeModules)), filepath.Join(second, string(dirNodeModules))); err != nil {
		t.Fatal(err)
	}
	ageFixtureTree(t, first)
	ageFixtureTree(t, second)
	t.Setenv("TESTCACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("TESTCACHE_CONFIG_DIR", filepath.Join(root, "usercfg"))
	return first, second
}

// Two clones at different paths are two repositories, unless TESTCACHE_REPO_ID names them:
// then the second clone replays what the first one stored, and an edit still re-runs.
func TestVitestRepoIDSharesTheCacheAcrossClones(t *testing.T) {
	first, second := setupClonePair(t)
	assertRunsThenHits(t, first, targetATest)
	assertRunOutcome(t, second, targetATest, runExpectation{summarySubstr: summaryOneRun})

	t.Setenv(cache.EnvRepoID, "example/repo")
	assertRunsThenHits(t, first, targetATest)
	assertRunOutcome(t, second, targetATest, runExpectation{summarySubstr: summaryOneHit})

	writeProjectFile(t, second, "src/usedByA.ts", "export function add(a: number, b: number): number {\n  return a - b;\n}\n")
	assertRunOutcome(t, second, targetATest, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
	assertRunOutcome(t, first, targetATest, runExpectation{summarySubstr: summaryOneHit})
}
