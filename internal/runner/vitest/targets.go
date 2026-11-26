package vitest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/lock"
)

// DiscoverRequest configures test file discovery for Vitest runs and explain queries.
type DiscoverRequest struct {
	Env       *runEnv
	WorkDir   string
	Parsed    ParsedArgs
	AllowNode bool
}

// DiscoveryResult partitions discovered targets by cacheability and exclusion reason.
type DiscoveryResult struct {
	Cacheable        []string
	ExcludedByTC     []string
	ExcludedByVitest []string
	MissingTargets   []string
	FromCache        bool
	Source           string
}

// DiscoverTargets resolves test files using Vitest's effective config (cached on disk)
// and applies positional filters and testcache.json exclude globs.
func DiscoverTargets(ctx context.Context, req DiscoverRequest) (DiscoveryResult, error) {
	projRoot := req.Env.proj.ProjectRoot
	latest := req.Env.loadLatestDiscovery()
	includeAllJS := latest != nil && latest.CustomInclude

	candidates, err := discoverCandidateFiles(projRoot, includeAllJS)
	if err != nil {
		return DiscoveryResult{}, err
	}

	discKey := computeDiscoveryKey(req.Env, req.Parsed, candidates)
	req.Env.discovery = &discoveryMemo{key: discKey, candidates: candidates}
	disc, err := resolveVitestFileList(ctx, req, discKey, candidates)
	if err != nil {
		return DiscoveryResult{}, err
	}

	res := partitionDiscoveredFiles(req, disc)
	if req.Parsed.Shard.active() {
		res = applyShard(res, projRoot, req.Parsed.Shard, req.Env.proj.VitestVersion)
	}
	return res, nil
}

type discoveredSet struct {
	vitestFiles []string
	candidates  []string
	fromCache   bool
	source      string
}

func resolveVitestFileList(ctx context.Context, req DiscoverRequest, discKey string, candidates []string) (discoveredSet, error) {
	if rec := req.Env.loadDiscovery(discKey); rec != nil && allFilesExist(rec.Files) {
		if req.AllowNode {
			req.Env.store.TouchDiscovery(discKey)
		}
		return discoveredSet{vitestFiles: rec.Files, candidates: candidates, fromCache: true, source: "cache"}, nil
	}
	if !req.AllowNode {
		return discoveredSet{vitestFiles: fallbackExplainFiles(req.Env, candidates), candidates: candidates, source: "fallback"}, nil
	}

	guard, err := lock.Acquire(filepath.Join(req.Env.store.LocksDir(), "discover-"+discKey+".lock"), nil)
	if err != nil {
		return discoveredSet{}, fmt.Errorf("acquire discovery lock: %w", err)
	}
	defer guard.Release()

	if rec := req.Env.loadDiscovery(discKey); rec != nil && allFilesExist(rec.Files) {
		return discoveredSet{vitestFiles: rec.Files, candidates: candidates, fromCache: true, source: "cache"}, nil
	}

	files, err := queryVitestFiles(ctx, req.Env, req.Parsed)
	if err != nil {
		return discoveredSet{}, err
	}
	_ = req.Env.saveDiscovery(discKey, files)
	return discoveredSet{vitestFiles: files, candidates: candidates, source: "vitest list"}, nil
}

func fallbackExplainFiles(env *runEnv, candidates []string) []string {
	latest := env.loadLatestDiscovery()
	if latest == nil {
		return candidates
	}
	seen := make(map[string]struct{}, len(latest.Files))
	var out []string
	for _, f := range latest.Files {
		if fileExists(f) {
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	// Include newly added candidate files not present in the stale discovery snapshot.
	for _, c := range candidates {
		if _, ok := seen[c]; !ok {
			if meta, _ := env.store.LoadTestMeta(relSlashPath(env.proj.RepoRoot, c)); meta == nil {
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

func partitionDiscoveredFiles(req DiscoverRequest, disc discoveredSet) DiscoveryResult {
	res := DiscoveryResult{FromCache: disc.fromCache, Source: disc.source}
	vitestSet := makeSet(disc.vitestFiles)
	var nonVitest []string
	for _, c := range disc.candidates {
		if _, ok := vitestSet[c]; !ok {
			nonVitest = append(nonVitest, c)
		}
	}

	var selected []string
	if len(req.Parsed.Positionals) == 0 {
		selected = disc.vitestFiles
		res.ExcludedByVitest = nonVitest
	} else if direct, ok := tryDirectFiles(req.WorkDir, req.Parsed.Positionals); ok {
		for _, f := range direct {
			if _, ok := vitestSet[f]; ok {
				selected = append(selected, f)
			} else {
				res.ExcludedByVitest = append(res.ExcludedByVitest, f)
			}
		}
	} else {
		selected = filterByPositionals(req.Env.proj.ProjectRoot, req.WorkDir, disc.vitestFiles, req.Parsed.Positionals)
		res.ExcludedByVitest = filterByPositionals(req.Env.proj.ProjectRoot, req.WorkDir, nonVitest, req.Parsed.Positionals)
		if len(selected) == 0 && len(res.ExcludedByVitest) == 0 {
			res.MissingTargets = findMissingFileArgs(req.WorkDir, req.Parsed.Positionals)
		}
	}

	for _, f := range selected {
		rel := relSlashPath(req.Env.proj.ProjectRoot, f)
		if config.IsExcluded(req.Env.cfg.Exclude, rel) {
			res.ExcludedByTC = append(res.ExcludedByTC, f)
		} else {
			res.Cacheable = append(res.Cacheable, f)
		}
	}
	return res
}

func tryDirectFiles(workDir string, positionals []string) ([]string, bool) {
	if len(positionals) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(positionals))
	out := make([]string, 0, len(positionals))
	for _, pos := range positionals {
		p := pos
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			return nil, false
		}
		rp := resolveRealPath(p)
		if _, dup := seen[rp]; !dup {
			seen[rp] = struct{}{}
			out = append(out, rp)
		}
	}
	return out, true
}

func findMissingFileArgs(workDir string, positionals []string) []string {
	var missing []string
	for _, pos := range positionals {
		p := pos
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			missing = append(missing, resolveRealPath(p))
		}
	}
	return missing
}

func discoverCandidateFiles(projRoot string, includeAllJS bool) ([]string, error) {
	skipDirs := map[string]bool{
		string(dirNodeModules): true,
		".git":                 true,
		string(dirDist):        true,
		"build":                true,
		".next":                true,
		"coverage":             true,
		".turbo":               true,
		".cache":               true,
	}
	var out []string
	err := filepath.WalkDir(projRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if (includeAllJS && hasJSExt(p)) || isVitestFile(projRoot, p) {
			out = append(out, resolveRealPath(p))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover test files in %s: %w", projRoot, err)
	}
	sort.Strings(out)
	return out, nil
}

func filterByPositionals(projRoot, workDir string, allTests, positionals []string) []string {
	if len(positionals) == 0 {
		return allTests
	}
	var matched []string
	for _, tf := range allTests {
		rel := relSlashPath(projRoot, tf)
		for _, pos := range positionals {
			if strings.Contains(strings.ToLower(rel), strings.ToLower(filepath.ToSlash(pos))) {
				matched = append(matched, tf)
				break
			}
			absPos := pos
			if !filepath.IsAbs(absPos) {
				absPos = filepath.Join(workDir, pos)
			}
			rp := resolveRealPath(absPos)
			if tf == rp || strings.HasPrefix(tf, rp+string(filepath.Separator)) {
				matched = append(matched, tf)
				break
			}
		}
	}
	return matched
}

func isVitestFile(projRoot, absPath string) bool {
	rel := relSlashPath(projRoot, absPath)
	base, ok := trimJSExt(rel)
	if !ok {
		return false
	}
	if strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".spec") {
		return true
	}
	return strings.Contains("/"+rel, "/__tests__/")
}

func hasCustomTestInclude(projRoot string, files []string) bool {
	for _, f := range files {
		if !isVitestFile(projRoot, f) {
			return true
		}
	}
	return false
}

func hasJSExt(p string) bool {
	_, ok := trimJSExt(p)
	return ok
}

func trimJSExt(p string) (string, bool) {
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts"} {
		if strings.HasSuffix(p, ext) {
			return strings.TrimSuffix(p, ext), true
		}
	}
	return p, false
}

func allFilesExist(files []string) bool {
	for _, f := range files {
		if !fileExists(f) {
			return false
		}
	}
	return true
}

func makeSet(items []string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

func writeHashField(h interface{ Write([]byte) (int, error) }, tag, val string) {
	fmt.Fprintf(h, "%s:%d:%s\n", tag, len(val), val)
}

func firstLine(s string) string {
	if line, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(line)
	}
	return s
}

func resolveRealPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func relSlashPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
