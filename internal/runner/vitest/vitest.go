package vitest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/lock"
	"github.com/chsotelo/testcache/internal/runlog"
	"github.com/chsotelo/testcache/internal/runner"
)

// Runner implements runner.Runner for Vitest.
type Runner struct{}

// New returns a Vitest runner.
func New() *Runner {
	return &Runner{}
}

// Name returns the runner identifier used in cache keys.
func (r *Runner) Name() string {
	return string(pkgVitest)
}

type targetState struct {
	absPath      string
	relPath      string
	fileHash     string
	baseKey      string
	fullKey      string
	fallbackDeps bool
	revalidating bool
	quarantined  bool
	hit          *cache.Entry
	// variant is the dependency list the hit was found under.
	variant *cache.DepVariant
}

// RunTests executes or replays Vitest tests with per-file coverage-aware caching.
func (r *Runner) RunTests(ctx context.Context, opts runner.RunOptions) (int, error) {
	parsed := ParseArgs(opts.Args)
	workDir := resolveRealPath(opts.WorkDir)
	if parsed.BypassCache {
		return r.runPassthrough(ctx, workDir, opts)
	}
	env, err := r.prepareEnv(workDir, parsed, true, opts.Stderr)
	if err != nil {
		return 1, err
	}
	if parsed.Changed {
		sources, err := changedFiles(ctx, env.proj.ProjectRoot, parsed.ChangedSince)
		if err != nil {
			// Vitest reports a missing repository or an unknown commit itself.
			logVerbose(env.verbose, opts.Stderr, "changed: %v", err)
			return r.runPassthrough(ctx, workDir, opts)
		}
		parsed.RelatedSources = sources
	}
	defer func() { _ = env.fileCache.Save() }()
	env.attachTrace(opts.Trace)

	discStart := time.Now()
	disc, err := DiscoverTargets(ctx, DiscoverRequest{Env: env, WorkDir: workDir, Parsed: parsed, AllowNode: true})
	if err != nil {
		return 1, err
	}
	env.trace.AddPhase(runlog.PhaseDiscover, time.Since(discStart))
	logVerbose(env.verbose, opts.Stderr, "discover: %dms (%s, %d files)",
		time.Since(discStart).Milliseconds(), disc.Source, len(disc.Cacheable)+len(disc.ExcludedByTC))
	if len(disc.Cacheable) == 0 && len(disc.ExcludedByTC) == 0 {
		return r.runPassthrough(ctx, workDir, opts)
	}

	plan, err := r.buildExecutionPlan(env, parsed, opts, disc)
	if err != nil {
		return 1, err
	}
	return r.executeAndReport(ctx, plan)
}

func (r *Runner) buildExecutionPlan(env *runEnv, parsed ParsedArgs, opts runner.RunOptions, disc DiscoveryResult) (executionPlan, error) {
	hashStart := time.Now()
	states, err := r.buildTargetStates(env, parsed, disc.Cacheable)
	if err != nil {
		return executionPlan{}, err
	}
	env.trace.AddPhase(runlog.PhaseHashing, time.Since(hashStart))
	logVerbose(env.verbose, opts.Stderr, "hashing: %dms (%d files)", time.Since(hashStart).Milliseconds(), len(states))

	lookupStart := time.Now()
	noCache := opts.NoCache || parsed.NoCache
	noRevalidate := opts.NoRevalidate || parsed.NoRevalidate
	hits, misses := partitionHits(env, states, partitionOptions{
		noCache:      noCache,
		noRevalidate: noRevalidate,
		randFn:       opts.RandFloat64,
	})
	env.trace.AddPhase(runlog.PhaseLookup, time.Since(lookupStart))
	logVerbose(env.verbose, opts.Stderr, "lookup: %dms (%d hits, %d misses)",
		time.Since(lookupStart).Milliseconds(), len(hits), len(misses))

	plan := executionPlan{
		env: env, parsed: parsed, opts: opts, noCache: noCache,
		totalFiles: len(disc.Cacheable) + len(disc.ExcludedByTC),
		hits:       hits, misses: misses, excluded: disc.ExcludedByTC,
	}
	if parsed.Related {
		plan.related = newRelatedSelection(env.proj.ProjectRoot, parsed.RelatedSources)
		if len(parsed.RelatedSources) == 0 {
			// Vitest selects nothing when no source is given.
			plan.misses, plan.excluded = nil, nil
		}
	}
	return plan, nil
}

type executionPlan struct {
	env        *runEnv
	parsed     ParsedArgs
	opts       runner.RunOptions
	noCache    bool
	totalFiles int
	hits       []*targetState
	misses     []*targetState
	excluded   []string
	// related is set for `vitest related`: cached files are kept only when Vitest would select
	// them, and the files without a cached pass go to Vitest, which picks among them.
	related *relatedSelection
}

func (r *Runner) executeAndReport(ctx context.Context, plan executionPlan) (int, error) {
	hits, misses := plan.hits, plan.misses
	var guards []*lock.Guard
	defer releaseAll(guards)
	if !plan.noCache && len(misses) > 0 {
		var err error
		guards, misses, hits, err = r.lockAndRecheck(plan)
		if err != nil {
			return 1, err
		}
	}

	if plan.related != nil {
		hits = plan.related.filter(plan.env.proj.RepoRoot, hits)
	}
	toRun := collectPathsToRun(misses, plan.excluded)
	counts := traceCounts{discovered: plan.totalFiles, cached: len(hits), run: len(toRun), misses: misses}
	if plan.related == nil {
		plan.env.traceFileCounts(counts)
	}
	exitCode, ran := 0, resultTally{known: true}
	if len(toRun) > 0 {
		res, err := r.executeMissed(ctx, missedRunRequest{
			env: plan.env, parsed: plan.parsed, toRun: toRun, misses: misses, opts: plan.opts,
			related: plan.related,
		})
		if err != nil {
			return maxInt(res.exitCode, 1), err
		}
		exitCode, ran = res.exitCode, res.ran
	} else {
		logVerbose(plan.env.verbose, plan.opts.Stderr, "run: 0ms (0 files, skipped)")
		logVerbose(plan.env.verbose, plan.opts.Stderr, "persist: 0ms (0 saved, skipped)")
	}
	releaseAll(guards)
	guards = nil

	totalFiles, runFiles := plan.totalFiles, len(toRun)
	if plan.related != nil {
		// Vitest ran only the files it found related: count those, not the ones it was given.
		runFiles = ran.files
		totalFiles = len(hits) + runFiles
		counts.discovered, counts.run = totalFiles, runFiles
		plan.env.traceFileCounts(counts)
	}
	savedMs, cached := replayCachedHits(plan.opts.Stdout, hits)
	printCombinedTotals(plan.opts.Stdout, cached, ran)
	plan.env.trace.SetSavedMs(savedMs)
	recordCacheActivity(plan, hits, len(toRun) > 0)
	_ = plan.env.store.RecordStats(int64(len(hits)), int64(runFiles), savedMs)
	printRunSummary(plan.opts.Stdout, runSummary{
		totalFiles: totalFiles, cachedFiles: len(hits), runFiles: runFiles, savedMs: savedMs,
		shard: plan.parsed.Shard, noReport: runFiles > 0 && !ran.known,
	})
	printCachedHint(plan.opts.Stdout, len(hits))
	return exitCode, nil
}

func recordCacheActivity(plan executionPlan, hits []*targetState, ranTests bool) {
	gcStart := time.Now()
	defer func() { plan.env.trace.AddPhase(runlog.PhaseGC, time.Since(gcStart)) }()
	if !plan.noCache {
		for _, st := range hits {
			plan.env.store.TouchHit(st.baseKey, st.fullKey)
		}
		if ranTests {
			if res, ran := plan.env.store.MaybeRunOpportunisticGC(plan.env.cfg.MaxCacheBytes, plan.env.cfg.MaxAgeDays); ran {
				logVerbose(plan.env.verbose, plan.opts.Stderr, "gc: %dms (%d removed, %d remaining)",
					time.Since(gcStart).Milliseconds(), res.RemovedFiles, res.RemainingEntries)
				return
			}
		}
	}
	logVerbose(plan.env.verbose, plan.opts.Stderr, "gc: %dms (0 removed, skipped)", time.Since(gcStart).Milliseconds())
}

type partitionOptions struct {
	noCache      bool
	noRevalidate bool
	randFn       func() float64
}

func partitionHits(env *runEnv, states []*targetState, opts partitionOptions) (hits, misses []*targetState) {
	for _, st := range states {
		relRepo := relSlashPath(env.proj.RepoRoot, st.absPath)
		if _, quarantined := env.quarantine.lookupEntry(relRepo); quarantined {
			st.quarantined = true
			misses = append(misses, st)
			continue
		}
		if !opts.noCache && env.lookupCachedEntry(st) {
			if !opts.noRevalidate && shouldRevalidate(env.cfg.RevalidateRate, opts.randFn) {
				st.revalidating = true
				misses = append(misses, st)
			} else {
				hits = append(hits, st)
			}
			continue
		}
		misses = append(misses, st)
	}
	return hits, misses
}

func (r *Runner) buildTargetStates(env *runEnv, parsed ParsedArgs, files []string) ([]*targetState, error) {
	out := make([]*targetState, 0, len(files))
	for _, abs := range files {
		fileHash, err := env.hasher.HashDependency(abs)
		if err != nil {
			return nil, fmt.Errorf("hash test file %s: %w", abs, err)
		}
		relProj := relSlashPath(env.proj.ProjectRoot, abs)
		baseKey := computeTargetBaseKey(env, parsed, baseKeyTarget{absPath: abs, fileHash: fileHash})
		out = append(out, &targetState{
			absPath:  abs,
			relPath:  relProj,
			fileHash: fileHash,
			baseKey:  baseKey,
		})
	}
	return out, nil
}

func (e *runEnv) lookupCachedEntry(st *targetState) bool {
	rec, err := e.store.LoadDeps(st.baseKey)
	if err != nil || rec == nil {
		return false
	}
	// The newest list first; an older one matches when another checkout or branch, whose
	// sources make the test execute other files, stored its result under it.
	for i, variant := range rec.Variants() {
		fullKey, ok := e.computeVariantKey(st.baseKey, variant)
		if !ok {
			continue
		}
		if i == 0 {
			st.fullKey = fullKey
		}
		if entry, err := e.store.LoadEntry(fullKey); err == nil && entry != nil {
			st.fullKey = fullKey
			st.hit = entry
			st.variant = &variant
			return true
		}
	}
	return false
}

func (e *runEnv) computeVariantKey(baseKey string, variant cache.DepVariant) (string, bool) {
	depHashes, ok := e.hashDepPaths(variant.Deps)
	if !ok {
		return "", false
	}
	globHashes, ok := e.hashGlobDeps(variant.Globs)
	if !ok {
		return "", false
	}
	return fingerprint.ComputeFullKeyWithInputs(fingerprint.FullKeyInputs{
		BaseKey: baseKey,
		Deps:    depHashes,
		Globs:   globHashes,
		Envs:    fingerprint.LookupEnvPairs(variant.EnvVars),
	}), true
}

func (e *runEnv) hashDepPaths(relPaths []string) ([]cache.DepHash, bool) {
	hashes := make([]cache.DepHash, 0, len(relPaths))
	for _, rel := range relPaths {
		abs := filepath.Join(e.proj.RepoRoot, filepath.FromSlash(rel))
		digest, err := e.hasher.HashDependency(abs)
		if err != nil {
			return nil, false
		}
		hashes = append(hashes, cache.DepHash{Path: rel, Hash: digest})
	}
	return hashes, true
}

func (r *Runner) lockAndRecheck(plan executionPlan) ([]*lock.Guard, []*targetState, []*targetState, error) {
	env, hits := plan.env, plan.hits
	sorted := append([]*targetState(nil), plan.misses...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].baseKey < sorted[j].baseKey })

	var guards []*lock.Guard
	var stillMissed []*targetState
	for _, st := range sorted {
		lockPath := filepath.Join(env.store.LocksDir(), "key-"+st.baseKey+".lock")
		waited := false
		lockStart := time.Now()
		g, err := lock.Acquire(lockPath, func() {
			waited = true
			fmt.Fprintf(plan.opts.Stderr, "tc: queued waiting for in-progress run of %s\n", st.relPath)
		})
		env.trace.AddPhase(runlog.PhaseLockWait, time.Since(lockStart))
		if err != nil {
			releaseAll(guards)
			return nil, nil, nil, fmt.Errorf("acquire key lock for %s: %w", st.relPath, err)
		}
		if waited {
			// The run we waited for may have recorded new main-process inputs: key like it did.
			env.reloadConfigInputs(plan.parsed, sorted)
		}
		if !st.revalidating && !st.quarantined && env.lookupCachedEntry(st) {
			_ = g.Release()
			hits = append(hits, st)
			continue
		}
		guards = append(guards, g)
		stillMissed = append(stillMissed, st)
	}
	return guards, stillMissed, hits, nil
}

func collectPathsToRun(misses []*targetState, excluded []string) []string {
	out := make([]string, 0, len(misses)+len(excluded))
	for _, st := range misses {
		out = append(out, st.absPath)
	}
	out = append(out, excluded...)
	return out
}

func releaseAll(guards []*lock.Guard) {
	for _, g := range guards {
		if g != nil {
			_ = g.Release()
		}
	}
}

func hashPathHex(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:12])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
