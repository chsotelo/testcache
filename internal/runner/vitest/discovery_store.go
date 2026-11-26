package vitest

import (
	"time"

	"github.com/chsotelo/testcache/internal/cache"
)

// Discovery records are stored with paths relative to the repository root, so every checkout
// of a repository (the main one and its git worktrees) reads the same record and a new
// worktree does not have to start Node to learn which files are tests.

func (e *runEnv) relProjectRoot() string {
	return relSlashPath(e.proj.RepoRoot, e.proj.ProjectRoot)
}

func (e *runEnv) absolutizeDiscovery(rec *cache.DiscoveryRecord) *cache.DiscoveryRecord {
	if rec == nil {
		return nil
	}
	files := make([]string, 0, len(rec.Files))
	for _, rel := range rec.Files {
		files = append(files, joinRepoPath(e.proj.RepoRoot, rel))
	}
	out := *rec
	out.Files = files
	return &out
}

// loadDiscovery returns the record for key with absolute paths in this checkout.
func (e *runEnv) loadDiscovery(key string) *cache.DiscoveryRecord {
	return e.absolutizeDiscovery(e.store.LoadDiscovery(key))
}

// loadLatestDiscovery returns the newest record of this project, whatever its key.
func (e *runEnv) loadLatestDiscovery() *cache.DiscoveryRecord {
	return e.absolutizeDiscovery(e.store.LoadLatestDiscovery(e.relProjectRoot()))
}

func (e *runEnv) saveDiscovery(key string, absFiles []string) error {
	rel := make([]string, 0, len(absFiles))
	for _, f := range absFiles {
		rel = append(rel, relSlashPath(e.proj.RepoRoot, f))
	}
	return e.store.SaveDiscovery(cache.DiscoveryRecord{
		Key:           key,
		ProjectRoot:   e.relProjectRoot(),
		Files:         rel,
		CustomInclude: hasCustomTestInclude(e.proj.ProjectRoot, absFiles),
		UpdatedAt:     time.Now().UTC(),
	})
}
