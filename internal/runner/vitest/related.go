package vitest

import (
	"path/filepath"
	"strings"
	"sync"
)

// relatedSelection decides which cached files `vitest related <sources> --run` selects.
//
// Vitest runs a test when the test itself is a source or when a source is among the files it
// imports, outside node_modules. A cached file is replayed when a source is the test or one
// of the files its passing run executed or matched through import.meta.glob. A file without a
// cached pass is handed to Vitest with the sources, and Vitest makes that choice itself.
type relatedSelection struct {
	sources []string
	mu      sync.RWMutex
	set     map[string]struct{}
	// all is set when a source is one of Vitest's default rerun triggers (package.json, a
	// Vite or Vitest config): Vitest then runs every test.
	all bool
}

// newRelatedSelection resolves the sources the way Vitest does: against the project root.
func newRelatedSelection(projRoot string, sources []string) *relatedSelection {
	sel := &relatedSelection{set: make(map[string]struct{}, 2*len(sources))}
	for _, src := range sources {
		abs := src
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projRoot, abs)
		}
		abs = filepath.Clean(abs)
		sel.sources = append(sel.sources, abs)
		sel.set[abs] = struct{}{}
		sel.set[resolveRealPath(abs)] = struct{}{}
		if isDefaultRerunTrigger(abs) {
			sel.all = true
		}
	}
	return sel
}

// isDefaultRerunTrigger matches Vitest's default forceRerunTriggers:
// "**/package.json" and "**/{vitest,vite}.config.*".
func isDefaultRerunTrigger(p string) bool {
	base := filepath.Base(p)
	if base == string(manifestPkgJSON) {
		return true
	}
	return strings.HasPrefix(base, "vitest.config.") || strings.HasPrefix(base, "vite.config.")
}

func (s *relatedSelection) has(abs string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.set[abs]
	return ok
}

// selects reports whether Vitest would run the cached file st.
func (s *relatedSelection) selects(repoRoot string, st *targetState) bool {
	if s.all || s.has(st.absPath) {
		return true
	}
	if st.variant == nil {
		return false
	}
	for _, rel := range st.variant.Deps {
		if s.has(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
			return true
		}
	}
	for _, g := range st.variant.Globs {
		if s.underDir(filepath.Join(repoRoot, filepath.FromSlash(g.BaseDir))) {
			return true
		}
	}
	return false
}

// underDir reports whether a source lies in dir. An import.meta.glob counts as importing
// every file under its base directory: replaying one more cached file is harmless, missing
// one would hide a test Vitest runs.
func (s *relatedSelection) underDir(dir string) bool {
	for _, src := range s.sources {
		rel, err := filepath.Rel(dir, src)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// filter keeps the cached files Vitest would select.
func (s *relatedSelection) filter(repoRoot string, hits []*targetState) []*targetState {
	out := hits[:0:0]
	for _, st := range hits {
		if s.selects(repoRoot, st) {
			out = append(out, st)
		}
	}
	return out
}
