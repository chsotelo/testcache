package runlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRepoID = "repo-a"

// buildRun returns a successful cached run of testRepoID that executed runFiles files.
func buildRun(runFiles int, duration time.Duration) Record {
	return Record{
		Mode: ModeCache, RepoID: testRepoID, DurationMs: duration.Milliseconds(),
		Files: FileCounts{Discovered: runFiles + 10, Run: runFiles, Cached: 10},
	}
}

func repeatRun(n int, rec Record) []Record {
	out := make([]Record, n)
	for i := range out {
		out[i] = rec
	}
	return out
}

func collectCodes(anomalies []Anomaly) string {
	codes := make([]string, len(anomalies))
	for i, a := range anomalies {
		codes[i] = string(a.Code)
	}
	return strings.Join(codes, ",")
}

func TestDetectAnomaliesSlowRun(t *testing.T) {
	fast := buildRun(5, 10*time.Second)
	slowBaseline := buildRun(5, 60*time.Second)
	otherRepo := buildRun(5, time.Second)
	otherRepo.RepoID = "repo-b"
	failed := buildRun(5, time.Second)
	failed.TCExit = 1
	errored := buildRun(5, time.Second)
	errored.Error = "boom"

	cases := []struct {
		name    string
		rec     Record
		history []Record
		want    string
	}{
		{"over 3x median and over the floor", buildRun(5, 130*time.Second), repeatRun(6, fast), "slow_run"},
		{"over 3x median but under the 120s floor", buildRun(5, 100*time.Second), repeatRun(6, fast), ""},
		{"over the floor but under 3x median", buildRun(5, 170*time.Second), repeatRun(6, slowBaseline), ""},
		{"over both with a slow baseline", buildRun(5, 190*time.Second), repeatRun(6, slowBaseline), "slow_run"},
		{"too little history", buildRun(5, 500*time.Second), repeatRun(slowRunMinSamples-1, fast), ""},
		{"no history", buildRun(5, 500*time.Second), nil, ""},
		{"other repos are not a baseline", buildRun(5, 500*time.Second), repeatRun(10, otherRepo), ""},
		{"failed runs are not a baseline", buildRun(5, 500*time.Second), repeatRun(10, failed), ""},
		{"errored runs are not a baseline", buildRun(5, 500*time.Second), repeatRun(10, errored), ""},
		{"runs of a different size are not a baseline", buildRun(200, 500*time.Second), repeatRun(10, fast), ""},
		{"a similar file count is a baseline", buildRun(9, 500*time.Second), repeatRun(10, fast), "slow_run"},
		{"only the last 20 similar runs count", buildRun(5, 130*time.Second),
			append(repeatRun(30, slowBaseline), repeatRun(slowRunWindow, fast)...), "slow_run"},
		{"passthrough runs are never slow", passthroughRun(900 * time.Second), repeatRun(10, fast), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectCodes(DetectAnomalies(tc.rec, tc.history)); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func passthroughRun(duration time.Duration) Record {
	rec := buildRun(0, duration)
	rec.Mode = ModePassthrough
	rec.Files = FileCounts{}
	return rec
}

func TestDetectAnomaliesSlowHitPathAndLockWait(t *testing.T) {
	hitOnly := func(total, lockWait time.Duration) Record {
		rec := buildRun(0, total)
		rec.PhasesMs.LockWait = lockWait.Milliseconds()
		return rec
	}
	withError := hitOnly(8*time.Second, 0)
	withError.Error = "boom"
	nothingFound := hitOnly(8*time.Second, 0)
	nothingFound.Files = FileCounts{}

	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"0 files run in 6s", hitOnly(6*time.Second, 0), "slow_hit_path"},
		{"0 files run in 4s", hitOnly(4*time.Second, 0), ""},
		{"the time went into waiting for a lock", hitOnly(9*time.Second, 5*time.Second), ""},
		{"files were run", buildRun(1, 30*time.Second), ""},
		{"tc failed", withError, ""},
		{"no test files at all", nothingFound, ""},
		{"passthrough", passthroughRun(30 * time.Second), ""},
		{"61s behind another run", hitOnly(62*time.Second, 61*time.Second), "long_lock_wait"},
		{"59s behind another run", hitOnly(60*time.Second, 59*time.Second), ""},
		{"a long wait and a slow hit path", hitOnly(70*time.Second, 61*time.Second), "long_lock_wait,slow_hit_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectCodes(DetectAnomalies(tc.rec, nil)); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestTraceCollectsEveryRunTimeAnomalyOnce(t *testing.T) {
	trace := NewTrace(Record{Mode: ModeCache})
	codes := []AnomalyCode{
		AnomalyStaleEntry, AnomalyQuarantined, AnomalyInterrupted, AnomalyInternalError, AnomalyNodeVersionMismatch,
	}
	for _, code := range codes {
		trace.AddAnomaly(code, "first")
		trace.AddAnomaly(code, "second")
	}
	rec := trace.CopyRecord()
	if len(rec.Anomalies) != len(codes) {
		t.Fatalf("expected one anomaly per code, got %+v", rec.Anomalies)
	}
	for i, code := range codes {
		if rec.Anomalies[i].Code != code || rec.Anomalies[i].Detail != "first" || !rec.HasAnomaly(code) {
			t.Fatalf("unexpected anomaly %d: %+v", i, rec.Anomalies[i])
		}
	}
}

func TestTraceIsSafeWhenNilAndAccumulates(t *testing.T) {
	var none *Trace
	none.SetRepo("r", "id")
	none.SetMode(ModeVerify)
	none.SetFiles(FileCounts{Run: 1})
	none.AddUncacheable(1)
	none.AddPhase(PhaseRun, time.Second)
	none.AddAnomaly(AnomalyStaleEntry, "x")
	none.SetVitestExit(1)
	none.SetSavedMs(5)
	if rec := none.CopyRecord(); rec.Anomalies == nil || len(rec.Anomalies) != 0 {
		t.Fatalf("a nil trace yields an empty record, got %+v", rec)
	}

	trace := NewTrace(Record{})
	trace.AddUncacheable(2)
	trace.SetFiles(FileCounts{Discovered: 5, Cached: 3, Run: 2, Quarantined: 1})
	for _, phase := range []Phase{PhaseDiscover, PhaseHashing, PhaseLookup, PhaseLockWait, PhaseRun, PhasePersist, PhaseGC} {
		trace.AddPhase(phase, 20*time.Millisecond)
		trace.AddPhase(phase, 30*time.Millisecond)
	}
	trace.SetVitestExit(3)
	trace.SetSavedMs(1500)
	rec := trace.CopyRecord()
	if rec.Files != (FileCounts{Discovered: 5, Cached: 3, Run: 2, Uncacheable: 2, Quarantined: 1}) {
		t.Fatalf("unexpected files: %+v", rec.Files)
	}
	if rec.PhasesMs != (PhaseDurations{Discover: 50, Hashing: 50, Lookup: 50, LockWait: 50, Run: 50, Persist: 50, GC: 50}) {
		t.Fatalf("phases must accumulate: %+v", rec.PhasesMs)
	}
	if rec.VitestExit == nil || *rec.VitestExit != 3 || rec.SavedS != 1.5 {
		t.Fatalf("unexpected exit/saved: %+v", rec)
	}
}

func TestShouldAlertIgnoresInterruptedOnly(t *testing.T) {
	cases := []struct {
		codes []AnomalyCode
		want  bool
	}{
		{nil, false},
		{[]AnomalyCode{AnomalyInterrupted}, false},
		{[]AnomalyCode{AnomalySlowRun}, true},
		{[]AnomalyCode{AnomalyInterrupted, AnomalyInternalError}, true},
	}
	for _, tc := range cases {
		var rec Record
		for _, code := range tc.codes {
			rec.Anomalies = append(rec.Anomalies, Anomaly{Code: code})
		}
		if got := ShouldAlert(rec); got != tc.want {
			t.Errorf("codes %v: expected alert=%v, got %v", tc.codes, tc.want, got)
		}
	}
}

func TestReadInvokerOnlyTrustsKnownValues(t *testing.T) {
	for value, want := range map[string]Invoker{
		"": InvokerDirect, "script-shell": InvokerScriptShell, "claude-hook": InvokerClaudeHook, "something": InvokerDirect,
	} {
		t.Setenv(EnvInvokedBy, value)
		if got := ReadInvoker(); got != want {
			t.Errorf("%s=%q: expected %q, got %q", EnvInvokedBy, value, want, got)
		}
	}
}

func TestExecuteAlertFeedsTheLineOnStdin(t *testing.T) {
	log := Log{Dir: t.TempDir()}
	received := filepath.Join(t.TempDir(), "received.json")
	line := []byte(`{"anomalies":[{"code":"slow_run"}]}` + "\n")
	err := log.ExecuteAlert(AlertRequest{Command: "cat > '" + received + "'", Line: line, Timeout: AlertTimeout})
	if err != nil {
		t.Fatalf("alert failed: %v", err)
	}
	got, err := os.ReadFile(received)
	if err != nil || !bytes.Equal(got, line) {
		t.Fatalf("the alert command must read the log line on stdin, got %q err=%v", got, err)
	}
	var stderr bytes.Buffer
	log.ReportAlertFailure(&stderr)
	if stderr.Len() != 0 {
		t.Fatalf("a successful alert reports nothing, got %q", stderr.String())
	}
}

func TestExecuteAlertTimesOutAndReportsOnce(t *testing.T) {
	log := Log{Dir: t.TempDir()}
	started := time.Now()
	// The command would outlive the test by far: only the timeout can end it.
	err := log.ExecuteAlert(AlertRequest{Command: "sleep 300", Line: []byte("{}\n"), Timeout: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("the timeout did not stop the command: %s", elapsed)
	}

	var first, second bytes.Buffer
	log.ReportAlertFailure(&first)
	log.ReportAlertFailure(&second)
	if !strings.Contains(first.String(), "tc: alertCommand failed: timed out") {
		t.Fatalf("the next run must report the failure, got %q", first.String())
	}
	if second.Len() != 0 {
		t.Fatalf("the failure must be reported once, got %q again", second.String())
	}
}

func TestExecuteAlertReportsAFailingCommand(t *testing.T) {
	log := Log{Dir: t.TempDir()}
	if err := log.ExecuteAlert(AlertRequest{Command: "exit 3", Line: []byte("{}\n"), Timeout: AlertTimeout}); err == nil {
		t.Fatal("expected an error for a failing alert command")
	}
	var stderr bytes.Buffer
	log.ReportAlertFailure(&stderr)
	if !strings.Contains(stderr.String(), "alertCommand failed: exit status 3") {
		t.Fatalf("unexpected report: %q", stderr.String())
	}
}
