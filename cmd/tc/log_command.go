package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runlog"
)

const (
	flagAnomalies  cliFlag = "--anomalies"
	flagRepo       cliFlag = "--repo"
	flagJSON       cliFlag = "--json"
	flagCount      cliFlag = "-n"
	defaultLogRuns         = 20
	logUsage               = "Usage: tc log [-n <runs>] [--anomalies] [--repo <path>] [--json]"
)

// logQuery is the parsed `tc log` command line.
type logQuery struct {
	limit         int
	onlyAnomalies bool
	repoPath      string
	rawJSON       bool
	showHelp      bool
}

func parseLogArgs(args []string) (logQuery, error) {
	q := logQuery{limit: defaultLogRuns}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case isHelpFlag(arg):
			q.showHelp = true
		case arg == string(flagAnomalies):
			q.onlyAnomalies = true
		case arg == string(flagJSON):
			q.rawJSON = true
		case arg == string(flagRepo) || arg == string(flagCount):
			if i+1 >= len(args) {
				return q, fmt.Errorf("%s requires a value", arg)
			}
			i++
			if err := q.applyValueFlag(arg, args[i]); err != nil {
				return q, err
			}
		default:
			return q, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return q, nil
}

func (q *logQuery) applyValueFlag(flag, value string) error {
	if flag == string(flagRepo) {
		q.repoPath = value
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fmt.Errorf("%s needs a positive number, got %q", flag, value)
	}
	q.limit = n
	return nil
}

func runLog(args []string, workDir string, stdout, stderr io.Writer) int {
	q, err := parseLogArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "tc log: %v\n%s\n", err, logUsage)
		return exitCodeUsage
	}
	if q.showHelp {
		fmt.Fprintln(stdout, logUsage)
		return 0
	}
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		fmt.Fprintf(stderr, "tc log: %v\n", err)
		return exitCodeFailure
	}
	runs, err := runlog.OpenLog(baseDir).ReadAll()
	if err != nil {
		fmt.Fprintf(stderr, "tc log: %v\n", err)
		return exitCodeFailure
	}
	repoRoot := ""
	if q.repoPath != "" {
		repoRoot = resolveRepoRoot(filepath.Join(workDir, q.repoPath))
		if filepath.IsAbs(q.repoPath) {
			repoRoot = resolveRepoRoot(q.repoPath)
		}
	}
	selected := selectLoggedRuns(runs, q, repoRoot)
	if q.rawJSON {
		for _, r := range selected {
			fmt.Fprintf(stdout, "%s\n", r.Raw)
		}
		return 0
	}
	printRunTable(stdout, selected)
	return 0
}

// selectLoggedRuns keeps the runs the query asks for and returns the last q.limit of them.
func selectLoggedRuns(runs []runlog.LoggedRun, q logQuery, repoRoot string) []runlog.LoggedRun {
	var out []runlog.LoggedRun
	for _, r := range runs {
		if q.onlyAnomalies && len(r.Record.Anomalies) == 0 {
			continue
		}
		if repoRoot != "" && r.Record.RepoRoot != repoRoot {
			continue
		}
		out = append(out, r)
	}
	if len(out) > q.limit {
		out = out[len(out)-q.limit:]
	}
	return out
}

func printRunTable(w io.Writer, runs []runlog.LoggedRun) {
	if len(runs) == 0 {
		fmt.Fprintln(w, "tc: no logged runs")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME (UTC)\tREPO\tVIA\tMODE\tCACHED/RUN/ALL\tDURATION\tEXIT\tSAVED\tANOMALIES")
	for _, r := range runs {
		rec := r.Record
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d/%d\t%s\t%d\t%.1fs\t%s\n",
			rec.Time, filepath.Base(rec.RepoRoot), rec.InvokedBy, rec.Mode,
			rec.Files.Cached, rec.Files.Run, rec.Files.Discovered,
			formatLogDuration(rec.DurationMs), rec.TCExit, rec.SavedS, joinAnomalyCodes(rec.Anomalies))
	}
	_ = tw.Flush()
}

func formatLogDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func joinAnomalyCodes(anomalies []runlog.Anomaly) string {
	if len(anomalies) == 0 {
		return "-"
	}
	codes := make([]string, len(anomalies))
	for i, a := range anomalies {
		codes[i] = string(a.Code)
	}
	return strings.Join(codes, ",")
}
