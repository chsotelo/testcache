package vitest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

const (
	hashMissing     = "missing"
	unsetEnvDisplay = "<unset>"
)

// ExplainOptions configures a cache explanation query.
type ExplainOptions struct {
	WorkDir string
	Args    []string
}

// ExplainEntry describes the cache status and miss reason for one test file.
type ExplainEntry struct {
	Path   string
	Cached bool
	Reason string
}

// Explain inspects cacheability and miss causes for test files without starting Node.
func (r *Runner) Explain(ctx context.Context, opts ExplainOptions) ([]ExplainEntry, error) {
	parsed := ParseArgs(opts.Args)
	workDir := resolveRealPath(opts.WorkDir)
	env, err := r.prepareEnv(workDir, parsed, false, os.Stderr)
	if err != nil {
		return nil, err
	}

	disc, err := DiscoverTargets(ctx, DiscoverRequest{
		Env: env, WorkDir: workDir, Parsed: parsed, AllowNode: false,
	})
	if err != nil {
		return nil, err
	}

	entries := make([]ExplainEntry, 0, len(disc.Cacheable)+len(disc.ExcludedByTC)+len(disc.ExcludedByVitest)+len(disc.MissingTargets))
	for _, abs := range disc.MissingTargets {
		entries = append(entries, ExplainEntry{Path: relSlashPath(env.proj.ProjectRoot, abs), Reason: "missing file"})
	}
	if len(parsed.Positionals) > 0 {
		for _, abs := range disc.ExcludedByVitest {
			entries = append(entries, ExplainEntry{Path: relSlashPath(env.proj.ProjectRoot, abs), Reason: "excluded by vitest config"})
		}
	}
	for _, abs := range disc.ExcludedByTC {
		entries = append(entries, ExplainEntry{Path: relSlashPath(env.proj.ProjectRoot, abs), Reason: "excluded by testcache.json"})
	}

	states, err := r.buildTargetStates(env, parsed, disc.Cacheable)
	if err != nil {
		return nil, err
	}
	for _, st := range states {
		relRepo := relSlashPath(env.proj.RepoRoot, st.absPath)
		if qEntry, quarantined := env.quarantine.lookupEntry(relRepo); quarantined {
			entries = append(entries, ExplainEntry{
				Path:   st.relPath,
				Cached: false,
				Reason: formatQuarantineExplainReason(qEntry),
			})
			continue
		}
		if env.lookupCachedEntry(st) {
			entries = append(entries, ExplainEntry{Path: st.relPath, Cached: true, Reason: "cached"})
			continue
		}
		entries = append(entries, ExplainEntry{Path: st.relPath, Cached: false, Reason: diagnoseMiss(env, parsed, st)})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func diagnoseMiss(env *runEnv, parsed ParsedArgs, st *targetState) string {
	relRepo := relSlashPath(env.proj.RepoRoot, st.absPath)
	meta, oldFmt := env.store.LoadTestMeta(relRepo)
	if oldFmt != "" {
		return fmt.Sprintf("cache format version changed (%s→%s)", oldFmt, cache.FormatVersion)
	}
	if meta == nil {
		return "first run"
	}

	var reasons []string
	if !meta.Passed {
		reasons = append(reasons, "previous run failed")
	}
	baseDiffs := diffBaseInputs(env, parsed, st, meta)
	reasons = append(reasons, baseDiffs...)
	if meta.UncacheableReason != "" && len(baseDiffs) == 0 {
		reasons = append(reasons, meta.UncacheableReason)
	}
	reasons = append(reasons, diffDepsAndEnv(env, meta)...)
	if len(reasons) == 0 {
		if rec, _ := env.store.LoadDeps(st.baseKey); rec == nil {
			return "dependency record missing or evicted"
		}
		return "cache entry missing or evicted"
	}
	return strings.Join(dedupStrings(reasons), "; ")
}

func diffBaseInputs(env *runEnv, parsed ParsedArgs, st *targetState, meta *cache.TestFileMeta) []string {
	var reasons []string
	if env.nodeUncached && meta.NodeVersion != "" {
		reasons = append(reasons, fmt.Sprintf("node version changed or uncached (was %s)", meta.NodeVersion))
	} else if meta.NodeVersion != env.nodeVer {
		reasons = append(reasons, fmt.Sprintf("node version changed (%s→%s)", meta.NodeVersion, env.nodeVer))
	}
	if meta.RunnerVersion != env.proj.VitestVersion {
		reasons = append(reasons, fmt.Sprintf("vitest version changed (%s→%s)", meta.RunnerVersion, env.proj.VitestVersion))
	}
	if meta.ConfigHash != env.cfgHash {
		reasons = append(reasons, "testcache.json config changed")
	}
	reasons = append(reasons, diffDepSlice(env, "config/lockfile", meta.ProjectFiles, env.proj.ProjectFiles)...)
	if !stringSlicesEqual(meta.Args, parsed.CacheableFlags) {
		reasons = append(reasons, fmt.Sprintf("vitest flags changed (%v→%v)", meta.Args, parsed.CacheableFlags))
	}
	if meta.TestFileHash != st.fileHash {
		reasons = append(reasons, fmt.Sprintf("test file changed: %s (%s→%s)", st.relPath, shortHash(meta.TestFileHash), shortHash(st.fileHash)))
	}
	reasons = append(reasons, diffEnvSnapshots(meta.AllowEnv, toEnvSnapshots(env.envPairs))...)
	return reasons
}

func diffDepsAndEnv(env *runEnv, meta *cache.TestFileMeta) []string {
	var reasons []string
	for _, oldDep := range meta.LastPassDeps {
		abs := filepath.Join(env.proj.RepoRoot, filepath.FromSlash(oldDep.Path))
		curHash, err := env.hasher.HashDependency(abs)
		shown := displayDepPath(env, oldDep.Path)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("dependency unreadable: %s", shown))
			continue
		}
		if curHash == oldDep.Hash {
			continue
		}
		switch {
		case oldDep.Hash == hashMissing && curHash != hashMissing:
			reasons = append(reasons, fmt.Sprintf("new file: %s (%s→%s)", shown, hashMissing, shortHash(curHash)))
		case oldDep.Hash != hashMissing && curHash == hashMissing:
			reasons = append(reasons, fmt.Sprintf("missing file: %s (%s→%s)", shown, shortHash(oldDep.Hash), hashMissing))
		default:
			reasons = append(reasons, fmt.Sprintf("dependency changed: %s (%s→%s)", shown, shortHash(oldDep.Hash), shortHash(curHash)))
		}
	}
	for _, oldEnv := range meta.LastPassDynamicEnv {
		curVal, curSet := fingerprint.LookupEnvValue(oldEnv.Key)
		if oldEnv.Set != curSet || (curSet && oldEnv.Value != curVal) {
			reasons = append(reasons, formatEnvChange(oldEnv.Key, formatEnvVal(oldEnv.Value, oldEnv.Set), formatEnvVal(curVal, curSet)))
		}
	}
	reasons = append(reasons, diffGlobDeps(env, meta.LastPassGlobs)...)
	return reasons
}

func diffGlobDeps(env *runEnv, globs []cache.GlobHash) []string {
	var reasons []string
	for _, g := range globs {
		curHash, err := fingerprint.HashGlob(env.proj.RepoRoot, g.GlobDep, env.hasher)
		shownBase := displayDepPath(env, g.BaseDir)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("glob unreadable: %s (base: %s)", g.Glob, shownBase))
			continue
		}
		if curHash != g.Hash {
			reasons = append(reasons, fmt.Sprintf("glob changed: %s (base: %s) (%s→%s)", g.Glob, shownBase, shortHash(g.Hash), shortHash(curHash)))
		}
	}
	return reasons
}

func diffDepSlice(env *runEnv, label string, oldList, curList []cache.DepHash) []string {
	oldMap := make(map[string]string, len(oldList))
	for _, d := range oldList {
		oldMap[d.Path] = d.Hash
	}
	curMap := make(map[string]string, len(curList))
	for _, d := range curList {
		curMap[d.Path] = d.Hash
	}
	var reasons []string
	for p, curH := range curMap {
		shown := displayDepPath(env, p)
		oldH, ok := oldMap[p]
		if !ok {
			reasons = append(reasons, fmt.Sprintf("new %s: %s (%s)", label, shown, shortHash(curH)))
		} else if oldH != curH {
			reasons = append(reasons, fmt.Sprintf("%s changed: %s (%s→%s)", label, shown, shortHash(oldH), shortHash(curH)))
		}
	}
	for p, oldH := range oldMap {
		if _, ok := curMap[p]; !ok {
			reasons = append(reasons, fmt.Sprintf("missing %s: %s (was %s)", label, displayDepPath(env, p), shortHash(oldH)))
		}
	}
	sort.Strings(reasons)
	return reasons
}

func displayDepPath(env *runEnv, repoRelPath string) string {
	if env.proj.ProjectRoot != "" && env.proj.ProjectRoot != env.proj.RepoRoot {
		abs := filepath.Join(env.proj.RepoRoot, filepath.FromSlash(repoRelPath))
		projRel := relSlashPath(env.proj.ProjectRoot, abs)
		if !strings.HasPrefix(projRel, "..") {
			return projRel
		}
	}
	return repoRelPath
}

func diffEnvSnapshots(oldList, curList []cache.EnvSnapshot) []string {
	oldMap := make(map[string]cache.EnvSnapshot, len(oldList))
	for _, e := range oldList {
		oldMap[e.Key] = e
	}
	curMap := make(map[string]cache.EnvSnapshot, len(curList))
	for _, e := range curList {
		curMap[e.Key] = e
	}
	var reasons []string
	for k, curE := range curMap {
		oldE, ok := oldMap[k]
		if !ok {
			if curE.Set {
				reasons = append(reasons, formatEnvChange(k, unsetEnvDisplay, formatEnvVal(curE.Value, true)))
			}
			continue
		}
		if oldE.Set != curE.Set || (curE.Set && oldE.Value != curE.Value) {
			reasons = append(reasons, formatEnvChange(k, formatEnvVal(oldE.Value, oldE.Set), formatEnvVal(curE.Value, curE.Set)))
		}
	}
	for k, oldE := range oldMap {
		if _, ok := curMap[k]; !ok && oldE.Set {
			reasons = append(reasons, formatEnvChange(k, formatEnvVal(oldE.Value, true), unsetEnvDisplay))
		}
	}
	sort.Strings(reasons)
	return reasons
}

func formatEnvChange(key, oldDisplay, curDisplay string) string {
	if key == fingerprint.EnvNamesKey {
		return "the set of environment variables changed (the test lists process.env): " +
			fingerprint.DescribeEnvNamesChange(strings.Trim(oldDisplay, `"`), strings.Trim(curDisplay, `"`))
	}
	return fmt.Sprintf("env var changed: %s (%s→%s)", key, oldDisplay, curDisplay)
}

func shortHash(h string) string {
	if h == "" || h == hashMissing {
		return h
	}
	if tag, hexPart, ok := strings.Cut(h, ":"); ok && len(hexPart) > 8 {
		return tag + ":" + hexPart[:8]
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func formatEnvVal(val string, set bool) string {
	if !set {
		return unsetEnvDisplay
	}
	return fmt.Sprintf("%q", val)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dedupStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	return out
}
