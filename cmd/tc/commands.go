package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
	"github.com/chsotelo/testcache/internal/runner/vitest"
)

type cliFlag string

const (
	flagHelp            cliFlag = "--help"
	flagHelpShort       cliFlag = "-h"
	flagNoCache         cliFlag = "--no-cache"
	flagNoRevalidate    cliFlag = "--no-revalidate"
	flagClear           cliFlag = "--clear"
	flagAll             cliFlag = "--all"
	errClearMissingFile string  = "--clear requires a file path"
)

func isHelpFlag(arg string) bool {
	return arg == string(flagHelp) || arg == string(flagHelpShort)
}

func runVitest(args []string, workDir string, stdout, stderr io.Writer) int {
	return executeLoggedRun(runInvocation{
		command: cmdVitest, label: programName, mode: runlog.ModeCache,
		args: args, workDir: workDir, stdout: stdout, stderr: stderr,
		execute: vitest.New().RunTests,
	})
}

func runExplain(args []string, workDir string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if isHelpFlag(a) {
			fmt.Fprintln(stdout, "Usage: tc explain [vitest args] [files...]")
			return 0
		}
	}
	r := vitest.New()
	entries, err := r.Explain(context.Background(), vitest.ExplainOptions{
		WorkDir: workDir,
		Args:    args,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tc explain: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "tc: no matching test files")
		return 0
	}
	for _, e := range entries {
		if e.Cached {
			fmt.Fprintf(stdout, "%s: cached\n", e.Path)
		} else {
			fmt.Fprintf(stdout, "%s: not cached (%s)\n", e.Path, e.Reason)
		}
	}
	return 0
}

func runVerify(args []string, workDir string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if isHelpFlag(a) {
			fmt.Fprintln(stdout, "Usage: tc verify [vitest args] [files...]")
			return 0
		}
	}
	return executeLoggedRun(runInvocation{
		command: cmdVerify, label: programName + " " + string(cmdVerify), mode: runlog.ModeVerify,
		args: args, workDir: workDir, noCache: true, stdout: stdout, stderr: stderr,
		execute: func(ctx context.Context, opts runner.RunOptions) (int, error) {
			_, code, err := vitest.New().Verify(ctx, opts)
			return code, err
		},
	})
}

func runQuarantine(args []string, workDir string, stdout, stderr io.Writer) int {
	clearTarget, showHelp, errMsg := parseQuarantineArgs(args)
	if showHelp {
		fmt.Fprintln(stdout, "Usage: tc quarantine [--clear <file>]")
		return 0
	}
	if errMsg != "" {
		fmt.Fprintf(stderr, "tc quarantine: %s\n", errMsg)
		return 2
	}
	r := vitest.New()
	if clearTarget != "" {
		return clearQuarantineTarget(quarantineClearRequest{
			runner: r, workDir: workDir, target: clearTarget, stdout: stdout, stderr: stderr,
		})
	}
	return listQuarantinedTests(r, workDir, stdout, stderr)
}

// quarantineClearRequest groups what `tc quarantine --clear` needs.
type quarantineClearRequest struct {
	runner         *vitest.Runner
	workDir        string
	target         string
	stdout, stderr io.Writer
}

func clearQuarantineTarget(req quarantineClearRequest) int {
	cleared, ok, err := req.runner.ClearQuarantine(req.workDir, req.target)
	if err != nil {
		fmt.Fprintf(req.stderr, "tc quarantine: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(req.stderr, "tc quarantine: %s is not quarantined\n", req.target)
		return 1
	}
	fmt.Fprintf(req.stdout, "tc: cleared quarantine for %s\n", cleared)
	return 0
}

func listQuarantinedTests(r *vitest.Runner, workDir string, stdout, stderr io.Writer) int {
	entries, err := r.ListQuarantine(workDir)
	if err != nil {
		fmt.Fprintf(stderr, "tc quarantine: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "tc: no quarantined test files")
		return 0
	}
	for _, e := range entries {
		if len(e.FailedTests) > 0 {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", e.TestFile, e.Date, strings.Join(e.FailedTests, ", "))
		} else {
			fmt.Fprintf(stdout, "%s\t%s\n", e.TestFile, e.Date)
		}
	}
	return 0
}

func parseQuarantineArgs(args []string) (clearTarget string, showHelp bool, errMsg string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isHelpFlag(arg) {
			return "", true, ""
		}
		if arg == string(flagClear) {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", false, errClearMissingFile
			}
			clearTarget = args[i+1]
			i++
			continue
		}
		if val, ok := strings.CutPrefix(arg, string(flagClear)+"="); ok {
			if val == "" {
				return "", false, errClearMissingFile
			}
			clearTarget = val
			continue
		}
		return "", false, fmt.Sprintf("unexpected argument %q", arg)
	}
	return clearTarget, false, ""
}

func runGC(args []string, workDir string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if isHelpFlag(a) {
			fmt.Fprintln(stdout, "Usage: tc gc")
			return 0
		}
		fmt.Fprintf(stderr, "tc gc: unknown flag %q\n", a)
		return 2
	}
	repoRoot := resolveRepoRoot(workDir)
	store, err := openStoreForRepo(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "tc gc: %v\n", err)
		return 1
	}
	cfg, _, err := config.Load(repoRoot, resolveAbsPath(workDir))
	if err != nil {
		fmt.Fprintf(stderr, "tc gc: %v\n", err)
		return 1
	}
	res, err := store.RunGC(cache.GCOptions{
		MaxBytes: cfg.MaxCacheBytes,
		MaxAge:   time.Duration(cfg.MaxAgeDays) * 24 * time.Hour,
		Blocking: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tc gc: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "tc: gc removed %d files (%s), %d entries remaining (%s)\n",
		res.RemovedFiles, formatBytes(res.RemovedBytes), res.RemainingEntries, formatBytes(res.RemainingBytes))
	return 0
}

func runClean(args []string, workDir string, stdout, stderr io.Writer) int {
	all := false
	for _, a := range args {
		switch {
		case a == string(flagAll):
			all = true
		case isHelpFlag(a):
			fmt.Fprintln(stdout, "Usage: tc clean [--all]")
			return 0
		default:
			fmt.Fprintf(stderr, "tc clean: unknown flag %q\n", a)
			return 2
		}
	}
	store, err := openStoreForRepo(resolveRepoRoot(workDir))
	if err != nil {
		fmt.Fprintf(stderr, "tc clean: %v\n", err)
		return 1
	}
	if err := store.Clean(all); err != nil {
		fmt.Fprintf(stderr, "tc clean: %v\n", err)
		return 1
	}
	if all {
		fmt.Fprintf(stdout, "tc: cleaned all cached repos under %s\n", store.BaseDir)
	} else {
		fmt.Fprintf(stdout, "tc: cleaned cache for repo %s (%s)\n", store.RepoID, store.RepoDir)
	}
	return 0
}

func runStats(args []string, workDir string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if isHelpFlag(a) {
			fmt.Fprintln(stdout, "Usage: tc stats")
			return 0
		}
		fmt.Fprintf(stderr, "tc stats: unknown flag %q\n", a)
		return 2
	}
	store, err := openStoreForRepo(resolveRepoRoot(workDir))
	if err != nil {
		fmt.Fprintf(stderr, "tc stats: %v\n", err)
		return 1
	}
	st, count, totalBytes, err := store.Summary()
	if err != nil {
		fmt.Fprintf(stderr, "tc stats: %v\n", err)
		return 1
	}
	savedSec := float64(st.SavedMs) / 1000.0
	fmt.Fprintf(stdout, "repo: %s\n", store.RepoID)
	fmt.Fprintf(stdout, "cache dir: %s\n", store.RepoDir)
	fmt.Fprintf(stdout, "hits: %d\n", st.Hits)
	fmt.Fprintf(stdout, "misses: %d\n", st.Misses)
	fmt.Fprintf(stdout, "time saved: %.2fs\n", savedSec)
	fmt.Fprintf(stdout, "entries: %d (%s)\n", count, formatBytes(totalBytes))
	return 0
}

func resolveAbsPath(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return workDir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func resolveRepoRoot(workDir string) string {
	return fingerprint.FindRepoRoot(resolveAbsPath(workDir))
}

func openStoreForRepo(repoRoot string) (*cache.Store, error) {
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		return nil, err
	}
	return cache.NewStore(baseDir, repoRoot), nil
}

func formatBytes(n int64) string {
	switch {
	case n >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1024*1024*1024))
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
