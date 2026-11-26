package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/runlog"
)

// seedLog writes four runs: three of the project's own repo and one of another.
func seedLog(t *testing.T, p *fakeProject) []string {
	t.Helper()
	exit := 1
	records := []runlog.Record{
		{Time: "2026-11-20T10:00:00Z", RepoRoot: p.dir, Mode: runlog.ModeCache, InvokedBy: runlog.InvokerDirect,
			Files: runlog.FileCounts{Discovered: 5, Cached: 5}, DurationMs: 12, SavedS: 3.25},
		{Time: "2026-11-20T10:01:00Z", RepoRoot: "/other/repo", Mode: runlog.ModeCache, InvokedBy: runlog.InvokerClaudeHook,
			Files: runlog.FileCounts{Discovered: 9, Cached: 8, Run: 1}, DurationMs: 2400,
			Anomalies: []runlog.Anomaly{{Code: runlog.AnomalyStaleEntry}, {Code: runlog.AnomalyNodeVersionMismatch}}},
		{Time: "2026-11-20T10:02:00Z", RepoRoot: p.dir, Mode: runlog.ModeVerify, InvokedBy: runlog.InvokerScriptShell,
			Files: runlog.FileCounts{Discovered: 5, Run: 5}, DurationMs: 130000, VitestExit: &exit, TCExit: 1,
			Anomalies: []runlog.Anomaly{{Code: runlog.AnomalySlowRun, Detail: "took 130s"}}},
		{Time: "2026-11-20T10:03:00Z", RepoRoot: p.dir, Mode: runlog.ModePassthrough, InvokedBy: runlog.InvokerDirect, DurationMs: 900},
	}
	log := runlog.OpenLog(p.cacheDir)
	lines := make([]string, len(records))
	for i, rec := range records {
		line, err := runlog.EncodeLine(rec)
		if err == nil {
			err = log.AppendLine(line)
		}
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(line)
	}
	return lines
}

func runLogCommand(t *testing.T, p *fakeProject, args ...string) string {
	t.Helper()
	var out, errBuf bytes.Buffer
	if code := runCLI(append([]string{"log"}, args...), p.dir, &out, &errBuf); code != 0 {
		t.Fatalf("tc log %v exited %d: %s", args, code, errBuf.String())
	}
	return out.String()
}

func TestLogCommandPrintsACompactTable(t *testing.T) {
	p := newFakeProject(t, 0)
	if out := runLogCommand(t, p); !strings.Contains(out, "tc: no logged runs") {
		t.Fatalf("unexpected output for an empty log: %q", out)
	}
	seedLog(t, p)

	rows := strings.Split(strings.TrimSpace(runLogCommand(t, p)), "\n")
	if len(rows) != 5 {
		t.Fatalf("expected a header and 4 runs:\n%s", strings.Join(rows, "\n"))
	}
	if got := strings.Join(strings.Fields(rows[0]), " "); got != "TIME (UTC) REPO VIA MODE CACHED/RUN/ALL DURATION EXIT SAVED ANOMALIES" {
		t.Fatalf("unexpected header: %q", got)
	}
	wantRows := []string{
		"2026-11-20T10:00:00Z proj direct cache 5/0/5 12ms 0 3.2s -",
		"2026-11-20T10:01:00Z repo claude-hook cache 8/1/9 2.4s 0 0.0s stale_entry,node_version_mismatch",
		"2026-11-20T10:02:00Z proj script-shell verify 0/5/5 130.0s 1 0.0s slow_run",
		"2026-11-20T10:03:00Z proj direct passthrough 0/0/0 900ms 0 0.0s -",
	}
	for i, want := range wantRows {
		if got := strings.Join(strings.Fields(rows[i+1]), " "); got != want {
			t.Errorf("row %d:\n got %q\nwant %q", i+1, got, want)
		}
	}
}

func TestLogCommandFilters(t *testing.T) {
	p := newFakeProject(t, 0)
	lines := seedLog(t, p)

	if out := runLogCommand(t, p, "--json"); out != strings.Join(lines, "") {
		t.Fatalf("--json must pass the lines through verbatim:\n%s", out)
	}
	if out := runLogCommand(t, p, "--json", "--anomalies"); out != lines[1]+lines[2] {
		t.Fatalf("--anomalies must keep only the anomalous runs:\n%s", out)
	}
	if out := runLogCommand(t, p, "--json", "--repo", "."); out != lines[0]+lines[2]+lines[3] {
		t.Fatalf("--repo . must keep only this repository:\n%s", out)
	}
	if out := runLogCommand(t, p, "--json", "--repo", ".", "--anomalies"); out != lines[2] {
		t.Fatalf("filters must combine:\n%s", out)
	}
	if out := runLogCommand(t, p, "--json", "-n", "1"); out != lines[3] {
		t.Fatalf("-n 1 must keep the last run:\n%s", out)
	}
	if out := runLogCommand(t, p, "--json", "-n", "2", "--repo", p.dir); out != lines[2]+lines[3] {
		t.Fatalf("-n counts after filtering, and --repo takes an absolute path:\n%s", out)
	}
	if out := runLogCommand(t, p, "--help"); !strings.Contains(out, "Usage: tc log") {
		t.Fatalf("unexpected help: %q", out)
	}

	for _, bad := range [][]string{{"--nope"}, {"-n"}, {"-n", "0"}, {"-n", "x"}, {"--repo"}} {
		var out, errBuf bytes.Buffer
		if code := runCLI(append([]string{"log"}, bad...), p.dir, &out, &errBuf); code != exitCodeUsage {
			t.Errorf("tc log %v: expected exit %d, got %d", bad, exitCodeUsage, code)
		}
	}
}
