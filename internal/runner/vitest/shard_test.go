package vitest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The ranges of every shard follow each other and cover all files once, in both formulas.
func TestShardRangesCoverEveryFileOnce(t *testing.T) {
	for _, major := range []int{3, 4, 5} {
		for n := 0; n <= 40; n++ {
			for count := 1; count <= 9; count++ {
				next := 0
				for index := 1; index <= count; index++ {
					start, end := shardRange(n, shardSpec{index: index, count: count}, major)
					if start != next || end < start || end > n {
						t.Fatalf("v%d n=%d %d/%d: range [%d,%d) after %d", major, n, index, count, start, end, next)
					}
					next = end
				}
				if next != n {
					t.Fatalf("v%d n=%d count=%d: ranges end at %d", major, n, count, next)
				}
			}
		}
	}
	// 9 files in 4 shards: Vitest 4+ gives 3,2,2,2; Vitest 3 gives 3,3,3,0.
	for _, c := range []struct {
		major int
		sizes []int
	}{{4, []int{3, 2, 2, 2}}, {3, []int{3, 3, 3, 0}}} {
		for i, want := range c.sizes {
			start, end := shardRange(9, shardSpec{index: i + 1, count: 4}, c.major)
			if end-start != want {
				t.Fatalf("v%d shard %d/4 of 9: %d files, want %d", c.major, i+1, end-start, want)
			}
		}
	}
}

// vitestShardFiles asks Vitest itself, without tc, which test files shard s runs.
func vitestShardFiles(t *testing.T, projDir string, s shardSpec) []string {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command(filepath.Join(projDir, "node_modules", ".bin", "vitest"), "run", "--shard="+s.String(),
		"--reporter=json", "--outputFile="+reportPath)
	cmd.Dir = projDir
	cmd.Env = append(os.Environ(), "TC_DISABLE=1", "NODE_OPTIONS=")
	_ = cmd.Run()
	out, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("vitest --shard=%s wrote no report: %v", s, err)
	}
	var report struct {
		TestResults []struct {
			Name string `json:"name"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("vitest --shard=%s json report: %v\n%s", s, err, out)
	}
	var files []string
	for _, r := range report.TestResults {
		files = append(files, relSlashPath(resolveRealPath(projDir), resolveRealPath(r.Name)))
	}
	sort.Strings(files)
	return files
}

func tcShardFiles(t *testing.T, projDir string, s shardSpec) []string {
	t.Helper()
	var files []string
	for _, e := range mustExplain(t, projDir, "--shard="+s.String()) {
		// Files the Vitest config excludes are listed by explain, never run by any shard.
		if strings.Contains(e.Reason, "excluded by vitest config") {
			continue
		}
		files = append(files, e.Path)
	}
	sort.Strings(files)
	return files
}

// Each shard holds the files Vitest gives it without tc, and the shards together hold every
// file once.
func TestShardOwnsWhatVitestWouldRun(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	// explain starts no Node: a run first stores the file list Vitest resolves, which every
	// shard of a run splits.
	runVitestFixture(t, projDir, false)
	all := tcShardFiles(t, projDir, shardSpec{index: 1, count: 1})
	// 3 shards split 9 files alike in every version; 4 do not (3,3,3,0 in Vitest 3, 3,2,2,2 after).
	for count := 3; count <= 4; count++ {
		seen := map[string]int{}
		for index := 1; index <= count; index++ {
			s := shardSpec{index: index, count: count}
			got, want := tcShardFiles(t, projDir, s), vitestShardFiles(t, projDir, s)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("shard %s: tc owns %v, Vitest runs %v", s, got, want)
			}
			for _, f := range got {
				seen[f]++
			}
		}
		if len(seen) != len(all) {
			t.Fatalf("%d shards hold %d files, the project has %d", count, len(seen), len(all))
		}
		for f, n := range seen {
			if n != 1 {
				t.Fatalf("%s is held by %d of %d shards", f, n, count)
			}
		}
	}
}

// A result stored by one shard serves a run without shards, since --shard is no part of a key.
// An edit re-runs only in the shard that owns the edited test; the others replay everything.
func TestShardRunsShareTheCache(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	var cold []string
	for index := 1; index <= 2; index++ {
		s := shardSpec{index: index, count: 2}
		code, out, _ := runVitestFixture(t, projDir, false, "--shard="+s.String())
		if code != 0 || !strings.Contains(out, "tc: shard "+s.String()+": ") || !strings.Contains(out, " 0 cached") {
			t.Fatalf("cold shard %s: code=%d out:\n%s", s, code, out)
		}
		cold = append(cold, out)
	}
	if code, out, _ := runVitestFixture(t, projDir, false); code != 0 || !strings.Contains(out, " 0 run") {
		t.Fatalf("a run without shards must replay what the shards stored: code=%d out:\n%s", code, out)
	}

	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"import { unusedHelper } from './onlyImported.js';\nexport function add(a: number, b: number): number {\n  if (a === -999999) return unusedHelper().length + b;\n  return a + b + 0;\n}\n")
	owner := ""
	for index := 1; index <= 2; index++ {
		s := shardSpec{index: index, count: 2}
		for _, f := range tcShardFiles(t, projDir, s) {
			if f == targetATest {
				owner = s.String()
			}
		}
	}
	for index := 1; index <= 2; index++ {
		s := shardSpec{index: index, count: 2}
		code, out, _ := runVitestFixture(t, projDir, false, "--shard="+s.String())
		wantRun := " 0 run"
		if s.String() == owner {
			wantRun = " 1 run"
		}
		if code != 0 || !strings.Contains(out, wantRun) {
			t.Fatalf("after the edit, shard %s (owner of %s: %s) want %q: code=%d out:\n%s", s, targetATest, owner, wantRun, code, out)
		}
	}
}

// More shards than files: Vitest refuses the split, so tc hands the run to Vitest unchanged.
func TestShardCountAboveFilesGoesToVitest(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	n := len(tcShardFiles(t, projDir, shardSpec{index: 1, count: 1}))
	arg := "--shard=" + shardSpec{index: n + 1, count: n + 1}.String()
	code, out, _ := runVitestFixture(t, projDir, false, arg)
	if strings.Contains(out, "tc: shard") {
		t.Fatalf("%s must not be served by tc:\n%s", arg, out)
	}
	cmd := exec.Command(filepath.Join(projDir, "node_modules", ".bin", "vitest"), "run", arg)
	cmd.Dir = projDir
	cmd.Env = append(os.Environ(), "TC_DISABLE=1", "NODE_OPTIONS=")
	_ = cmd.Run()
	if want := cmd.ProcessState.ExitCode(); code != want {
		t.Fatalf("%s: tc exit %d, plain Vitest exit %d", arg, code, want)
	}
}
