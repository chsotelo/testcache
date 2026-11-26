package vitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// worktreePair is a real git repository with one extra `git worktree`, both holding the
// fixture project and sharing its node_modules.
type worktreePair struct {
	main, linked string
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=tc", "-c", "user.email=tc@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func setupWorktreePair(t *testing.T) worktreePair {
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
	pair := worktreePair{main: filepath.Join(root, "main"), linked: filepath.Join(root, "linked")}
	copyFixtureTree(t, srcFixture, pair.main)
	if err := os.WriteFile(filepath.Join(pair.main, ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, pair.main, "init", "-q")
	runGit(t, pair.main, "add", "-A")
	runGit(t, pair.main, "commit", "-q", "-m", "fixture")
	runGit(t, pair.main, "worktree", "add", "-q", pair.linked)
	if err := os.Symlink(filepath.Join(srcFixture, string(dirNodeModules)), filepath.Join(pair.linked, string(dirNodeModules))); err != nil {
		t.Fatal(err)
	}
	ageFixtureTree(t, pair.main)
	ageFixtureTree(t, pair.linked)
	t.Setenv("TESTCACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("TESTCACHE_CONFIG_DIR", filepath.Join(root, "usercfg"))
	return pair
}

// A new worktree starts with the results its repository already has: same contents, same keys.
func TestVitestWorktreeSharesTheCache(t *testing.T) {
	pair := setupWorktreePair(t)
	assertRunsThenHits(t, pair.main, targetATest)
	assertRunOutcome(t, pair.linked, targetATest, runExpectation{summarySubstr: summaryOneHit})
	if entries := mustExplain(t, pair.linked, targetATest); len(entries) != 1 || !entries[0].Cached {
		t.Fatalf("explain in the new worktree must say cached, got %+v", entries)
	}
}

// An edit in one worktree re-runs there and nowhere else, and fails where it should.
func TestVitestWorktreeEditStaysInItsWorktree(t *testing.T) {
	pair := setupWorktreePair(t)
	assertRunsThenHits(t, pair.main, targetATest)

	writeProjectFile(t, pair.linked, "src/usedByA.ts", "export function add(a: number, b: number): number {\n  return a - b;\n}\n")
	assertRunOutcome(t, pair.linked, targetATest, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
	assertRunOutcome(t, pair.main, targetATest, runExpectation{summarySubstr: summaryOneHit})
	assertRunOutcome(t, pair.linked, targetATest, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// Two checkouts whose sources make one test execute different files each keep their own
// dependency list: with a single list per test they would evict each other on every run.
func TestVitestWorktreesWithDifferentDependencyLists(t *testing.T) {
	pair := setupWorktreePair(t)
	assertRunsThenHits(t, pair.main, targetATest)

	writeProjectFile(t, pair.linked, "src/usedByA.ts", `import { multiply } from './usedByB.js';
export function add(a: number, b: number): number {
  return multiply(a, 1) + b;
}
`)
	assertRunsThenHits(t, pair.linked, targetATest)
	for round := 1; round <= 2; round++ {
		assertRunOutcome(t, pair.main, targetATest, runExpectation{summarySubstr: summaryOneHit})
		assertRunOutcome(t, pair.linked, targetATest, runExpectation{summarySubstr: summaryOneHit})
	}

	// The linked worktree's result depends on usedByB; the main one's does not.
	writeProjectFile(t, pair.linked, "src/usedByB.ts", "export function multiply(a: number, b: number): number {\n  return a * b * 2;\n}\n")
	assertRunOutcome(t, pair.linked, targetATest, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
	assertRunOutcome(t, pair.main, targetATest, runExpectation{summarySubstr: summaryOneHit})
}

// One content, one hash: a modified file and the same file once committed are the same input.
// (They used to hash as "sha256:" and "git:", so every commit of a config or data file threw
// the cache away.)
func TestVitestCommittingAFileKeepsTheCache(t *testing.T) {
	pair := setupWorktreePair(t)
	writeProjectFile(t, pair.main, "package.json", `{"name":"fixture-committed","private":true,"type":"module","scripts":{"test":"vitest run"}}`+"\n")
	writeProjectFile(t, pair.main, "data/theme.css", ":root { --accent: #123456; }\n")
	assertRunsThenHits(t, pair.main, targetATest)

	runGit(t, pair.main, "add", "-A")
	runGit(t, pair.main, "commit", "-q", "-m", "edit config and data")
	assertRunOutcome(t, pair.main, targetATest, runExpectation{summarySubstr: summaryOneHit})
}
