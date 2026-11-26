package vitest

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
)

type quarantineMsg string

const (
	quarantineReasonText quarantineMsg = "was cached as passing but failed on re-run with identical inputs; quarantined (likely flaky or depends on an untracked input)"
)

type quarantineIndex struct {
	mu     sync.RWMutex
	byFile map[string]cache.QuarantineEntry
}

func loadQuarantineIndex(store *cache.Store) *quarantineIndex {
	entries, err := store.LoadQuarantine()
	if err != nil || len(entries) == 0 {
		return &quarantineIndex{byFile: make(map[string]cache.QuarantineEntry)}
	}
	m := make(map[string]cache.QuarantineEntry, len(entries))
	for _, e := range entries {
		m[e.TestFile] = e
	}
	return &quarantineIndex{byFile: m}
}

func (idx *quarantineIndex) lookupEntry(relRepo string) (cache.QuarantineEntry, bool) {
	if idx == nil {
		return cache.QuarantineEntry{}, false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	e, ok := idx.byFile[relRepo]
	return e, ok
}

func (idx *quarantineIndex) recordEntry(entry cache.QuarantineEntry) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.byFile == nil {
		idx.byFile = make(map[string]cache.QuarantineEntry)
	}
	idx.byFile[entry.TestFile] = entry
}

func shouldRevalidate(rate float64, randFn func() float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	if randFn != nil {
		return randFn() < rate
	}
	v, err := cryptoRandFloat64()
	if err != nil {
		return false
	}
	return v < rate
}

func cryptoRandFloat64() (float64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	// Use top 53 bits to produce a uniform float64 in [0.0, 1.0).
	n := binary.BigEndian.Uint64(buf[:]) >> 11
	return float64(n) / float64(uint64(1)<<53), nil
}

type quarantineRequest struct {
	env         *runEnv
	st          *targetState
	failedTests []string
	stderr      io.Writer
}

func quarantineStaleTest(req quarantineRequest) error {
	if req.st.fullKey != "" {
		_ = req.env.store.DeleteEntry(req.st.fullKey)
	}
	failed := append([]string(nil), req.failedTests...)
	if len(failed) == 0 && req.st.relPath != "" {
		failed = []string{req.st.relPath}
	}
	relRepo := relSlashPath(req.env.proj.RepoRoot, req.st.absPath)
	entry := cache.QuarantineEntry{
		TestFile:    relRepo,
		Date:        time.Now().UTC().Format(time.RFC3339),
		FailedTests: failed,
		Reason:      string(quarantineReasonText),
	}
	if err := req.env.store.AddQuarantine(entry); err != nil {
		return err
	}
	req.env.quarantine.recordEntry(entry)
	req.env.trace.AddAnomaly(runlog.AnomalyStaleEntry, relRepo+" was cached as passing and failed with identical inputs")
	if req.stderr != nil {
		fmt.Fprintf(req.stderr, "tc: %s %s\n", req.st.relPath, quarantineReasonText)
	}
	return nil
}

func formatQuarantineExplainReason(entry cache.QuarantineEntry) string {
	base := "quarantined: " + string(quarantineReasonText)
	if len(entry.FailedTests) > 0 {
		return fmt.Sprintf("%s (failing tests: %s)", base, strings.Join(entry.FailedTests, ", "))
	}
	return base
}

// ListQuarantine returns all quarantined test files for the repository containing workDir.
func (r *Runner) ListQuarantine(workDir string) ([]cache.QuarantineEntry, error) {
	resolved := resolveRealPath(workDir)
	repoRoot := fingerprint.FindRepoRoot(resolved)
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		return nil, err
	}
	store := cache.NewStore(baseDir, repoRoot)
	entries, err := store.LoadQuarantine()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].TestFile = displayQuarantinePath(repoRoot, resolved, entries[i].TestFile)
	}
	return entries, nil
}

// ClearQuarantine removes target from quarantine for the repository containing workDir.
func (r *Runner) ClearQuarantine(workDir, target string) (string, bool, error) {
	resolved := resolveRealPath(workDir)
	repoRoot := fingerprint.FindRepoRoot(resolved)
	baseDir, err := config.CacheBaseDir()
	if err != nil {
		return "", false, err
	}
	store := cache.NewStore(baseDir, repoRoot)

	for _, cand := range quarantineTargetCandidates(repoRoot, resolved, target) {
		removed, err := store.RemoveQuarantine(cand)
		if err != nil {
			return "", false, err
		}
		if removed {
			return displayQuarantinePath(repoRoot, resolved, cand), true, nil
		}
	}
	return filepath.ToSlash(filepath.Clean(target)), false, nil
}

func displayQuarantinePath(repoRoot, workDir, relRepo string) string {
	abs := filepath.Join(repoRoot, filepath.FromSlash(relRepo))
	rel := relSlashPath(workDir, abs)
	if !strings.HasPrefix(rel, "..") && rel != "." {
		return rel
	}
	return relRepo
}

func quarantineTargetCandidates(repoRoot, workDir, target string) []string {
	absTarget := target
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(workDir, target)
	}
	cands := []string{
		relSlashPath(repoRoot, resolveRealPath(absTarget)),
		relSlashPath(repoRoot, filepath.Clean(absTarget)),
		filepath.ToSlash(filepath.Clean(target)),
	}
	seen := make(map[string]struct{}, len(cands))
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; !ok {
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}
