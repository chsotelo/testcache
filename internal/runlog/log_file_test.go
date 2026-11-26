package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	envHelperDir   = "TC_RUNLOG_HELPER_DIR"
	envHelperID    = "TC_RUNLOG_HELPER_ID"
	envHelperLines = "TC_RUNLOG_HELPER_LINES"
	envHelperMax   = "TC_RUNLOG_HELPER_MAX"
	helperTestName = "TestHelperAppendProcess"
)

func buildSampleRecord(repoID string, seq int) Record {
	return Record{
		Time: "2026-11-20T10:00:00Z", TCVersion: "test", Command: "vitest", Mode: ModeCache,
		InvokedBy: InvokerDirect, RepoRoot: "/repo/" + repoID, RepoID: repoID, Cwd: "/repo/" + repoID,
		Argv:  []string{"tc", "vitest", strconv.Itoa(seq)},
		Files: FileCounts{Discovered: 3, Cached: 2, Run: 1}, DurationMs: int64(seq),
	}
}

func TestEncodeLineHasTheDocumentedSchema(t *testing.T) {
	exit := 1
	rec := buildSampleRecord("r1", 7)
	rec.VitestExit = &exit
	rec.Anomalies = []Anomaly{{Code: AnomalySlowRun, Detail: "took long"}}
	rec.Error = "boom"

	line, err := EncodeLine(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
		t.Fatalf("a record must be exactly one line: %q", line)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(line, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"time", "tc_version", "command", "mode", "invoked_by", "repo_root", "repo_id", "cwd",
		"argv", "files", "phases_ms", "duration_ms", "vitest_exit", "tc_exit", "saved_s", "anomalies", "error"} {
		if _, ok := top[key]; !ok {
			t.Errorf("missing top-level field %q in %s", key, line)
		}
	}
	assertObjectKeys(t, top["files"], []string{"discovered", "cached", "run", "uncacheable", "quarantined"})
	assertObjectKeys(t, top["phases_ms"], []string{"discover", "hashing", "lookup", "lock_wait", "run", "persist", "gc"})
	if got := string(top["anomalies"]); got != `[{"code":"slow_run","detail":"took long"}]` {
		t.Errorf("unexpected anomalies encoding: %s", got)
	}
}

func TestEncodeLineKeepsEmptyFieldsExplicit(t *testing.T) {
	line, err := EncodeLine(buildSampleRecord("r1", 1))
	if err != nil {
		t.Fatal(err)
	}
	text := string(line)
	if !strings.Contains(text, `"anomalies":[]`) || !strings.Contains(text, `"vitest_exit":null`) {
		t.Fatalf("expected empty anomalies list and null vitest_exit: %s", text)
	}
	if strings.Contains(text, `"error"`) {
		t.Fatalf("error must be omitted when tc did not fail: %s", text)
	}
}

func TestEncodeLineTrimsHugeArgv(t *testing.T) {
	rec := buildSampleRecord("r1", 1)
	for i := 0; i < 5000; i++ {
		rec.Argv = append(rec.Argv, strings.Repeat("x", 300))
	}
	line, err := EncodeLine(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(line) > maxLineBytes {
		t.Fatalf("line of %d bytes exceeds the %d byte cap", len(line), maxLineBytes)
	}
	var back Record
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatalf("trimmed line must stay valid JSON: %v", err)
	}
	if len(back.Argv) != maxArgvItems+1 || !strings.HasPrefix(back.Argv[maxArgvItems], "...(+") {
		t.Fatalf("unexpected trimmed argv: %d items, last %q", len(back.Argv), back.Argv[len(back.Argv)-1])
	}
}

func assertObjectKeys(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("not an object: %s", raw)
	}
	if len(obj) != len(want) {
		t.Errorf("expected keys %v, got %s", want, raw)
	}
	for _, key := range want {
		if _, ok := obj[key]; !ok {
			t.Errorf("missing key %q in %s", key, raw)
		}
	}
}

// TestHelperAppendProcess is not a test: it is the body of the child processes started by
// the concurrency tests below.
func TestHelperAppendProcess(t *testing.T) {
	dir := os.Getenv(envHelperDir)
	if dir == "" {
		t.Skip("helper process only")
	}
	lines, _ := strconv.Atoi(os.Getenv(envHelperLines))
	maxBytes, _ := strconv.ParseInt(os.Getenv(envHelperMax), 10, 64)
	log := Log{Dir: dir, MaxBytes: maxBytes}
	for seq := 0; seq < lines; seq++ {
		line, err := EncodeLine(buildSampleRecord(os.Getenv(envHelperID), seq))
		if err == nil {
			err = log.AppendLine(line)
		}
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

// appendFromProcesses starts procs copies of the test binary that each append lines records.
func appendFromProcesses(t *testing.T, log Log, procs, lines int) {
	t.Helper()
	var wg sync.WaitGroup
	failures := make(chan string, procs)
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^"+helperTestName+"$")
			cmd.Env = append(os.Environ(),
				envHelperDir+"="+log.Dir, envHelperID+"="+fmt.Sprintf("p%d", id),
				envHelperLines+"="+strconv.Itoa(lines), envHelperMax+"="+strconv.FormatInt(log.MaxBytes, 10))
			if out, err := cmd.CombinedOutput(); err != nil {
				failures <- fmt.Sprintf("helper %d: %v\n%s", id, err, out)
			}
		}(p)
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Fatal(f)
	}
}

func TestAppendLineIsAtomicAcrossProcesses(t *testing.T) {
	const procs, lines = 8, 120
	log := Log{Dir: t.TempDir(), MaxBytes: DefaultMaxBytes}
	appendFromProcesses(t, log, procs, lines)

	data, err := os.ReadFile(log.livePath())
	if err != nil {
		t.Fatal(err)
	}
	rawLines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(rawLines) != procs*lines {
		t.Fatalf("expected %d lines, got %d", procs*lines, len(rawLines))
	}
	nextSeq := map[string]int{}
	for i, raw := range rawLines {
		var rec Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("line %d is torn or interleaved: %v\n%s", i, err, raw)
		}
		// Each writer appends in order, so its own sequence numbers must come out in order.
		if got := int(rec.DurationMs); got != nextSeq[rec.RepoID] {
			t.Fatalf("writer %s: expected sequence %d at line %d, got %d", rec.RepoID, nextSeq[rec.RepoID], i, got)
		}
		nextSeq[rec.RepoID]++
	}
	if len(nextSeq) != procs {
		t.Fatalf("expected lines from %d writers, got %d", procs, len(nextSeq))
	}
}

func TestRotationUnderConcurrencyKeepsEveryLineWhole(t *testing.T) {
	const procs, lines = 6, 60
	log := Log{Dir: t.TempDir(), MaxBytes: 4 << 10}
	appendFromProcesses(t, log, procs, lines)

	entries, err := os.ReadDir(log.Dir)
	if err != nil {
		t.Fatal(err)
	}
	logFiles := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), FileName) {
			continue
		}
		logFiles++
		data, err := os.ReadFile(filepath.Join(log.Dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if !json.Valid([]byte(raw)) {
				t.Fatalf("%s holds a torn line: %q", e.Name(), raw)
			}
		}
	}
	if logFiles != rotatedFiles+1 {
		t.Fatalf("expected exactly %d log files after rotation, got %d", rotatedFiles+1, logFiles)
	}
}

func TestRotationKeepsThreeFilesOldestFirst(t *testing.T) {
	log := Log{Dir: t.TempDir(), MaxBytes: 600}
	const total = 40
	for seq := 0; seq < total; seq++ {
		line, err := EncodeLine(buildSampleRecord("r1", seq))
		if err != nil {
			t.Fatal(err)
		}
		if err := log.AppendLine(line); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= rotatedFiles; n++ {
		if _, err := os.Stat(log.rotatedPath(n)); err != nil {
			t.Fatalf("expected rotated file %d: %v", n, err)
		}
	}
	if _, err := os.Stat(log.rotatedPath(rotatedFiles + 1)); !os.IsNotExist(err) {
		t.Fatalf("a fourth log file must never exist, stat err=%v", err)
	}
	if info, err := os.Stat(log.rotatedPath(1)); err != nil || info.Size() <= log.MaxBytes {
		t.Fatalf("a file rotates only once it is over MaxBytes: size=%v err=%v", info, err)
	}

	runs, err := log.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 || len(runs) >= total {
		t.Fatalf("expected the oldest runs to be dropped, kept %d of %d", len(runs), total)
	}
	if last := runs[len(runs)-1].Record.DurationMs; last != total-1 {
		t.Fatalf("the newest run must come last, got sequence %d", last)
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].Record.DurationMs != runs[i-1].Record.DurationMs+1 {
			t.Fatalf("runs out of order at %d: %d after %d", i, runs[i].Record.DurationMs, runs[i-1].Record.DurationMs)
		}
	}
}

func TestReadRepoHistoryKeepsOnlyTheRepoAndSkipsGarbage(t *testing.T) {
	log := Log{Dir: t.TempDir(), MaxBytes: DefaultMaxBytes}
	if runs, err := log.ReadRepoHistory("r1"); err != nil || runs != nil {
		t.Fatalf("a missing log is empty, got %v err=%v", runs, err)
	}
	content := []byte("{not json\n\n")
	for seq, repo := range []string{"r1", "r2", "r1", "r10", "r1"} {
		line, err := EncodeLine(buildSampleRecord(repo, seq))
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, line...)
	}
	content = append(content, []byte(`{"repo_id":"r1","duration_ms":"torn`)...)
	if err := os.WriteFile(log.livePath(), content, 0o644); err != nil {
		t.Fatal(err)
	}
	runs, err := log.ReadRepoHistory("r1")
	if err != nil || len(runs) != 3 {
		t.Fatalf("expected the three valid runs of r1, got %+v err=%v", runs, err)
	}
	for i, wantSeq := range []int64{0, 2, 4} {
		if runs[i].RepoID != "r1" || runs[i].DurationMs != wantSeq {
			t.Fatalf("runs must come oldest first: %+v", runs)
		}
	}
}

func TestReadRepoHistoryLooksBackALimitedNumberOfRuns(t *testing.T) {
	log := Log{Dir: t.TempDir(), MaxBytes: DefaultMaxBytes}
	const total = historyLimit + 25
	var content []byte
	for seq := 0; seq < total; seq++ {
		line, err := EncodeLine(buildSampleRecord("r1", seq))
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, line...)
	}
	if err := os.MkdirAll(log.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log.livePath(), content, 0o644); err != nil {
		t.Fatal(err)
	}
	runs, err := log.ReadRepoHistory("r1")
	if err != nil || len(runs) != historyLimit {
		t.Fatalf("expected the last %d runs, got %d err=%v", historyLimit, len(runs), err)
	}
	if runs[0].DurationMs != total-historyLimit || runs[len(runs)-1].DurationMs != total-1 {
		t.Fatalf("expected the newest runs, oldest first: first=%d last=%d", runs[0].DurationMs, runs[len(runs)-1].DurationMs)
	}
}
