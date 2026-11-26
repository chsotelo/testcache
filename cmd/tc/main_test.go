package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

func TestRunCLIBasics(t *testing.T) {
	t.Setenv("TESTCACHE_DIR", t.TempDir())
	var out, errBuf bytes.Buffer
	dir := t.TempDir()

	for _, arg := range []string{"version", "--version", "-v"} {
		out.Reset()
		errBuf.Reset()
		if code := runCLI([]string{arg}, dir, &out, &errBuf); code != 0 {
			t.Fatalf("%s exit=%d stderr=%s", arg, code, errBuf.String())
		}
		if out.String() != "tc "+version+"\n" {
			t.Fatalf("unexpected %s output: %q", arg, out.String())
		}
	}

	for _, arg := range []string{"help", "--help", "-h"} {
		out.Reset()
		if code := runCLI([]string{arg}, dir, &out, &errBuf); code != 0 {
			t.Fatalf("%s exit=%d stderr=%s", arg, code, errBuf.String())
		}
		if !strings.Contains(out.String(), "tc explain") || !strings.Contains(out.String(), "tc verify") || !strings.Contains(out.String(), "tc quarantine") || !strings.Contains(out.String(), "tc gc") {
			t.Fatalf("unexpected %s output: %q", arg, out.String())
		}
	}

	out.Reset()
	errBuf.Reset()
	if code := runCLI([]string{"stats"}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("stats exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "hits: 0") {
		t.Fatalf("unexpected stats output: %q", out.String())
	}

	out.Reset()
	if code := runCLI([]string{"gc"}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("gc exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "tc: gc removed 0 files") {
		t.Fatalf("unexpected gc output: %q", out.String())
	}

	out.Reset()
	if code := runCLI([]string{"clean"}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("clean exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "cleaned cache") {
		t.Fatalf("unexpected clean output: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runCLI([]string{"unknown-cmd"}, dir, &out, &errBuf); code != 2 {
		t.Fatalf("expected exit 2 on unknown command, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "unknown command") {
		t.Fatalf("unexpected error output: %q", errBuf.String())
	}
}

type (
	fixtureTargetPath string
	cliSubcmd         string
)

const cmdQuarantine cliSubcmd = "quarantine"

func TestRunCLIQuarantineCommands(t *testing.T) {
	t.Setenv("TESTCACHE_DIR", t.TempDir())
	dir := t.TempDir()
	var out, errBuf bytes.Buffer
	const flakyTestPath fixtureTargetPath = "tests/flaky.test.ts"

	if code := runCLI([]string{string(cmdQuarantine)}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("quarantine empty exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no quarantined test files") {
		t.Fatalf("unexpected empty quarantine output: %q", out.String())
	}

	baseDir, err := config.CacheBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir := resolveAbsPath(dir)
	repoRoot := fingerprint.FindRepoRoot(resolvedDir)
	store := cache.NewStore(baseDir, repoRoot)
	relTest := string(flakyTestPath)
	if rel, err := filepath.Rel(repoRoot, filepath.Join(resolvedDir, relTest)); err == nil {
		relTest = filepath.ToSlash(rel)
	}
	if err := store.AddQuarantine(cache.QuarantineEntry{
		TestFile:    relTest,
		Date:        time.Now().UTC().Format(time.RFC3339),
		FailedTests: []string{"suite > flaky case"},
	}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errBuf.Reset()
	if code := runCLI([]string{string(cmdQuarantine)}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("quarantine list exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), string(flakyTestPath)) || !strings.Contains(out.String(), "suite > flaky case") {
		t.Fatalf("unexpected quarantine list output: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runCLI([]string{string(cmdQuarantine), "--clear", string(flakyTestPath)}, dir, &out, &errBuf); code != 0 {
		t.Fatalf("quarantine --clear exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "cleared quarantine") {
		t.Fatalf("unexpected clear output: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runCLI([]string{string(cmdQuarantine), "--clear", string(flakyTestPath)}, dir, &out, &errBuf); code != 1 {
		t.Fatalf("expected exit 1 clearing non-quarantined file, got %d", code)
	}
}
