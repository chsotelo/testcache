package vitest

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// traceCounts is what a run knows about its files once the plan is settled.
type traceCounts struct {
	discovered int
	cached     int
	run        int
	misses     []*targetState
}

// attachTrace connects the run log trace to this run: repository identity and corrupt cache
// files found from now on.
func (e *runEnv) attachTrace(trace *runlog.Trace) {
	e.trace = trace
	trace.SetRepo(e.proj.RepoRoot, e.store.RepoID)
	if trace == nil {
		return
	}
	repoDir := e.store.RepoDir
	e.store.OnCorrupt = func(path string) {
		trace.AddAnomaly(runlog.AnomalyInternalError, "corrupt cache file removed: "+relSlashPath(repoDir, path))
	}
}

// traceFileCounts records the file counts and flags files that ran because they are quarantined.
func (e *runEnv) traceFileCounts(counts traceCounts) {
	quarantined, example := 0, ""
	for _, st := range counts.misses {
		relRepo := relSlashPath(e.proj.RepoRoot, st.absPath)
		if _, held := e.quarantine.lookupEntry(relRepo); held {
			quarantined++
			if example == "" {
				example = relRepo
			}
		}
	}
	e.trace.SetFiles(runlog.FileCounts{
		Discovered: counts.discovered, Cached: counts.cached, Run: counts.run, Quarantined: quarantined,
	})
	if quarantined > 0 {
		e.trace.AddAnomaly(runlog.AnomalyQuarantined,
			fmt.Sprintf("%d quarantined file(s) ran uncached, e.g. %s", quarantined, example))
	}
}

// traceUnstoredPass counts a passing file whose result was not stored.
func (e *runEnv) traceUnstoredPass(unstored bool) {
	if unstored {
		e.trace.AddUncacheable(1)
	}
}

// buildChildEnv returns the environment for a Vitest child. TC_DISABLE is set so a test that
// itself runs a package script or tc gets plain Vitest instead of queueing behind this run.
// tc's own bookkeeping variables are dropped: a test must not see how tc was reached.
func buildChildEnv() []string {
	own := map[string]struct{}{}
	for _, k := range fingerprint.TCOwnEnvKeys() {
		own[k] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, mine := own[name]; !mine {
			env = append(env, entry)
		}
	}
	return append(env, config.EnvDisable+"=1")
}

// LocateBinary returns the Vitest executable tc would run from workDir, or "" when the
// project does not resolve one.
func LocateBinary(workDir string) string {
	projRoot := resolveRealPath(workDir)
	bin := findVitestBin(projRoot, fingerprint.FindRepoRoot(projRoot))
	if bin == "" {
		return ""
	}
	return resolveRealPath(bin)
}

// RunPlain runs the project's Vitest once with the given tc arguments and none of tc's
// machinery: no cache, no hooks, no locks. It serves TC_DISABLE and the fallback after a tc
// panic. Arguments that select another Vitest mode (watch, UI, ...) are passed as they are.
func RunPlain(ctx context.Context, opts runner.RunOptions) (int, error) {
	parsed := ParseArgs(opts.Args)
	if !parsed.BypassCache && !parsed.Related && !startsWithRunSubcommand(opts.Args) {
		opts.Args = append([]string{string(subcommandRun)}, opts.Args...)
	}
	return runVitestUncached(ctx, resolveRealPath(opts.WorkDir), opts)
}

func startsWithRunSubcommand(args []string) bool {
	for _, a := range args {
		if a == string(tcFlagNoCache) || a == string(tcFlagNoRevalidate) {
			continue
		}
		return a == string(subcommandRun)
	}
	return false
}
