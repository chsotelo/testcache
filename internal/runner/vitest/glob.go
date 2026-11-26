package vitest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

type pathPrefix string

const parentPrefix pathPrefix = ".."

type rawAffirmedGlob struct {
	Glob       string `json:"glob"`
	Resolved   string `json:"resolved"`
	StaticBase string `json:"staticBase"`
}

type rawImportGlobRecord struct {
	Importer   string            `json:"importer"`
	Affirmed   []rawAffirmedGlob `json:"affirmed"`
	Negated    []string          `json:"negated"`
	Exhaustive bool              `json:"exhaustive"`
	HasQuery   bool              `json:"hasQuery"`
	Query      string            `json:"query"`
}

type importGlobIndex struct {
	mu         sync.RWMutex
	byImporter map[string][]rawImportGlobRecord
}

func (idx *importGlobIndex) hasRecords() bool {
	if idx == nil {
		return false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byImporter) > 0
}

func (idx *importGlobIndex) lookupImporter(importer string) []rawImportGlobRecord {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.byImporter[importer]
}

type collectGlobsRequest struct {
	repoRoot    string
	absTestPath string
	importers   []string
	globIndex   *importGlobIndex
}

type appendGlobRequest struct {
	repoRoot string
	imp      string
	rec      rawImportGlobRecord
}

func indexImportGlobs(rawJSON json.RawMessage) *importGlobIndex {
	if len(rawJSON) == 0 {
		return nil
	}
	var raw map[string][]rawImportGlobRecord
	if err := json.Unmarshal(rawJSON, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	out := make(map[string][]rawImportGlobRecord, len(raw))
	for k, v := range raw {
		out[resolveRealPath(k)] = v
	}
	return &importGlobIndex{byImporter: out}
}

func collectTestGlobs(req collectGlobsRequest) []cache.GlobDep {
	if !req.globIndex.hasRecords() {
		return nil
	}
	seenImporters := make(map[string]struct{}, len(req.importers)+1)
	seenGlobs := make(map[string]struct{})
	var out []cache.GlobDep

	candidates := append([]string{req.absTestPath}, req.importers...)
	for _, rawImp := range candidates {
		imp := resolveRealPath(rawImp)
		if _, dup := seenImporters[imp]; dup {
			continue
		}
		seenImporters[imp] = struct{}{}
		for _, rec := range req.globIndex.lookupImporter(imp) {
			out = appendRecordGlobs(out, seenGlobs, appendGlobRequest{
				repoRoot: req.repoRoot, imp: imp, rec: rec,
			})
		}
	}
	sortGlobDeps(out)
	return out
}

func appendRecordGlobs(out []cache.GlobDep, seen map[string]struct{}, req appendGlobRequest) []cache.GlobDep {
	relImp := relSlashPath(req.repoRoot, req.imp)
	negated := normalizeNegatedGlobs(req.repoRoot, req.rec.Negated)
	for _, aff := range req.rec.Affirmed {
		relBase := relSlashPath(req.repoRoot, resolveRealPath(aff.StaticBase))
		if strings.HasPrefix(relBase, string(parentPrefix)) {
			continue
		}
		gd := cache.GlobDep{
			Importer:   relImp,
			Glob:       aff.Glob,
			Negated:    negated,
			BaseDir:    relBase,
			Exhaustive: req.rec.Exhaustive,
			HasQuery:   req.rec.HasQuery,
			Query:      req.rec.Query,
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%s",
			gd.Importer, gd.Glob, gd.BaseDir, gd.Exhaustive, gd.HasQuery, gd.Query)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, gd)
	}
	return out
}

func normalizeNegatedGlobs(repoRoot string, negated []string) []string {
	if len(negated) == 0 {
		return nil
	}
	out := make([]string, 0, len(negated))
	for _, g := range negated {
		rel := relSlashPath(repoRoot, resolveRealPath(g))
		if !strings.HasPrefix(rel, string(parentPrefix)) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func sortGlobDeps(globs []cache.GlobDep) {
	sort.Slice(globs, func(i, j int) bool {
		if globs[i].BaseDir != globs[j].BaseDir {
			return globs[i].BaseDir < globs[j].BaseDir
		}
		if globs[i].Glob != globs[j].Glob {
			return globs[i].Glob < globs[j].Glob
		}
		return globs[i].Importer < globs[j].Importer
	})
}

func (e *runEnv) hashGlobDeps(globs []cache.GlobDep) ([]cache.GlobHash, bool) {
	if len(globs) == 0 {
		return nil, true
	}
	hashes := make([]cache.GlobHash, 0, len(globs))
	for _, g := range globs {
		digest, err := fingerprint.HashGlob(e.proj.RepoRoot, g, e.hasher)
		if err != nil {
			return nil, false
		}
		hashes = append(hashes, cache.GlobHash{GlobDep: g, Hash: digest})
	}
	return hashes, true
}
