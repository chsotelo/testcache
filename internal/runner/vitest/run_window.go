package vitest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

// freshInputMargin is how long before the run start an input must have been last modified
// for its post-run hash to be trusted as the content the test saw (covers coarse mtimes).
const freshInputMargin = 2 * time.Second

type inputKind string

const (
	inputProjectFile inputKind = "proj"
	inputDep         inputKind = "dep"
	inputGlob        inputKind = "glob"
)

// runWindow records when a Vitest run started and what every already-known input hashed to
// before it, so a pass is never stored under content the test did not see.
type runWindow struct {
	start  time.Time
	mu     sync.Mutex
	before map[string]string
}

func openRunWindow(env *runEnv, misses []*targetState) *runWindow {
	win := &runWindow{start: time.Now(), before: make(map[string]string)}
	for _, pf := range env.proj.ProjectFiles {
		win.rememberHash(inputProjectFile, pf.Path, pf.Hash)
	}
	for _, st := range misses {
		win.rememberHash(inputDep, relSlashPath(env.proj.RepoRoot, st.absPath), st.fileHash)
		deps, globs := env.hashKnownInputs(st)
		for _, d := range deps {
			win.rememberHash(inputDep, d.Path, d.Hash)
		}
		for _, g := range globs {
			win.rememberHash(inputGlob, describeGlobDep(g.GlobDep), g.Hash)
		}
	}
	return win
}

func (w *runWindow) rememberHash(kind inputKind, id, hash string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.before[string(kind)+"\x00"+id] = hash
}

func (w *runWindow) lookupHash(kind inputKind, id string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	hash, ok := w.before[string(kind)+"\x00"+id]
	return hash, ok
}

// hashKnownInputs hashes the deps and globs recorded by the test file's last passing run.
func (e *runEnv) hashKnownInputs(st *targetState) ([]cache.DepHash, []cache.GlobHash) {
	var paths []string
	var globs []cache.GlobDep
	if rec, _ := e.store.LoadDeps(st.baseKey); rec != nil {
		paths, globs = rec.Deps, rec.Globs
	} else if meta, _ := e.store.LoadTestMeta(relSlashPath(e.proj.RepoRoot, st.absPath)); meta != nil {
		for _, d := range meta.LastPassDeps {
			paths = append(paths, d.Path)
		}
		for _, g := range meta.LastPassGlobs {
			globs = append(globs, g.GlobDep)
		}
	}
	depHashes, _ := e.hashDepPaths(paths)
	globHashes, _ := e.hashGlobDeps(globs)
	return depHashes, globHashes
}

// rejectFile explains why the file behind dep must not back a cached pass, or returns "".
func (w *runWindow) rejectFile(env *runEnv, kind inputKind, dep cache.DepHash) string {
	if prev, ok := w.lookupHash(kind, dep.Path); ok && prev != dep.Hash {
		return fmt.Sprintf("changed during the run: %s", displayDepPath(env, dep.Path))
	}
	abs := filepath.Join(env.proj.RepoRoot, filepath.FromSlash(dep.Path))
	info, err := os.Stat(abs)
	if err == nil && !info.ModTime().Before(w.start.Add(-freshInputMargin)) {
		return fmt.Sprintf("modified during the run or less than %s before it: %s",
			freshInputMargin, displayDepPath(env, dep.Path))
	}
	return ""
}

// rejectProjectFiles checks the config, lockfile and other base-key files after the run.
func (w *runWindow) rejectProjectFiles(env *runEnv) string {
	for _, pf := range env.proj.ProjectFiles {
		if reason := w.rejectFile(env, inputProjectFile, pf); reason != "" {
			return reason
		}
	}
	return ""
}

type moduleInputs struct {
	st    *targetState
	deps  []cache.DepHash
	globs []cache.GlobHash
}

// rejectModule checks the test file, its deps and its globs after the run.
func (w *runWindow) rejectModule(env *runEnv, in moduleInputs) string {
	relTest := relSlashPath(env.proj.RepoRoot, in.st.absPath)
	testHash, err := env.hasher.HashDependency(in.st.absPath)
	if err != nil {
		return fmt.Sprintf("unreadable after the run: %s", displayDepPath(env, relTest))
	}
	if reason := w.rejectFile(env, inputDep, cache.DepHash{Path: relTest, Hash: testHash}); reason != "" {
		return reason
	}
	for _, d := range in.deps {
		if reason := w.rejectFile(env, inputDep, d); reason != "" {
			return reason
		}
	}
	for _, g := range in.globs {
		if prev, ok := w.lookupHash(inputGlob, describeGlobDep(g.GlobDep)); ok && prev != g.Hash {
			return fmt.Sprintf("glob changed during the run: %s", g.Glob)
		}
	}
	return ""
}

func describeGlobDep(g cache.GlobDep) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%s", g.Importer, g.Glob, g.BaseDir, g.Exhaustive, g.HasQuery, g.Query)
}

// reloadGitState makes later hashes read the working tree as it is now, not as it was at startup.
func (e *runEnv) reloadGitState() {
	e.hasher = e.hasher.WithGitState(fingerprint.LoadGitState(e.proj.RepoRoot))
}
