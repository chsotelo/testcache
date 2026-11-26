package cache

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chsotelo/testcache/internal/lock"
)

const (
	gcLockName       = "gc.lock"
	gcStampName      = "gc.stamp"
	gcInterval       = 24 * time.Hour
	staleTempCutoff  = time.Hour
	runTempPrefix    = "run-"
	atomicTempPrefix = ".tmp-"
)

// GCOptions configures cache garbage collection for a repository store.
type GCOptions struct {
	MaxBytes int64
	MaxAge   time.Duration
	Now      time.Time
	Blocking bool
}

// GCResult summarizes files removed and retained by a GC pass.
type GCResult struct {
	RemovedFiles     int
	RemovedBytes     int64
	RemainingEntries int
	RemainingBytes   int64
	SkippedLocked    bool
}

type gcCandidate struct {
	path    string
	size    int64
	modTime time.Time
	isEntry bool
}

// RunGC enforces max age and max byte bounds for this repository's cache.
func (s *Store) RunGC(opts GCOptions) (GCResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	guard, ok, err := s.acquireGCLock(opts.Blocking)
	if err != nil || !ok {
		return GCResult{SkippedLocked: !ok && err == nil}, err
	}
	defer guard.Release()

	res := GCResult{}
	s.cleanStaleTempFiles(now)

	candidates, fixedBytes := s.collectGCCandidates()
	var survivors []gcCandidate
	var totalBytes int64 = fixedBytes

	for _, c := range candidates {
		if opts.MaxAge > 0 && now.Sub(c.modTime) > opts.MaxAge {
			if os.Remove(c.path) == nil {
				res.RemovedFiles++
				res.RemovedBytes += c.size
				continue
			}
		}
		survivors = append(survivors, c)
		totalBytes += c.size
	}

	if opts.MaxBytes > 0 && totalBytes > opts.MaxBytes {
		evictedFiles, evictedBytes, kept := evictLRU(survivors, totalBytes, opts.MaxBytes)
		res.RemovedFiles += evictedFiles
		res.RemovedBytes += evictedBytes
		survivors = kept
	}

	res.RemainingBytes = fixedBytes
	for _, c := range survivors {
		res.RemainingBytes += c.size
		if c.isEntry {
			res.RemainingEntries++
		}
	}
	_ = WriteFileAtomic(filepath.Join(s.RepoDir, gcStampName), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
	return res, nil
}

// MaybeRunOpportunisticGC runs non-blocking GC at most once every 24 hours.
func (s *Store) MaybeRunOpportunisticGC(maxBytes int64, maxAgeDays int) (GCResult, bool) {
	stampPath := filepath.Join(s.RepoDir, gcStampName)
	if info, err := os.Stat(stampPath); err == nil && time.Since(info.ModTime()) < gcInterval {
		return GCResult{}, false
	}
	res, err := s.RunGC(GCOptions{
		MaxBytes: maxBytes,
		MaxAge:   time.Duration(maxAgeDays) * 24 * time.Hour,
		Blocking: false,
	})
	if err != nil || res.SkippedLocked {
		return res, false
	}
	return res, true
}

func (s *Store) acquireGCLock(blocking bool) (*lock.Guard, bool, error) {
	lockPath := filepath.Join(s.LocksDir(), gcLockName)
	if blocking {
		g, err := lock.Acquire(lockPath, nil)
		return g, err == nil, err
	}
	return lock.TryAcquire(lockPath)
}

func (s *Store) collectGCCandidates() ([]gcCandidate, int64) {
	var out []gcCandidate
	subdirs := []storeSubdir{subdirEntries, subdirDeps, subdirDiscovery, subdirMeta}
	for _, sub := range subdirs {
		dir := filepath.Join(s.RepoDir, string(sub))
		items, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.IsDir() || strings.HasPrefix(item.Name(), atomicTempPrefix) {
				continue
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			out = append(out, gcCandidate{
				path:    filepath.Join(dir, item.Name()),
				size:    info.Size(),
				modTime: info.ModTime(),
				isEntry: sub == subdirEntries,
			})
		}
	}

	var fixedBytes int64
	for _, name := range []string{fileHashesName, fileStatsName} {
		if info, err := os.Stat(filepath.Join(s.RepoDir, name)); err == nil && !info.IsDir() {
			fixedBytes += info.Size()
		}
	}
	return out, fixedBytes
}

func evictLRU(survivors []gcCandidate, totalBytes, maxBytes int64) (int, int64, []gcCandidate) {
	sort.Slice(survivors, func(i, j int) bool {
		if !survivors[i].modTime.Equal(survivors[j].modTime) {
			return survivors[i].modTime.Before(survivors[j].modTime)
		}
		if survivors[i].isEntry != survivors[j].isEntry {
			return survivors[i].isEntry
		}
		return survivors[i].path < survivors[j].path
	})
	removedFiles := 0
	var removedBytes int64
	idx := 0
	for idx < len(survivors) && totalBytes > maxBytes {
		c := survivors[idx]
		idx++
		if os.Remove(c.path) == nil {
			removedFiles++
			removedBytes += c.size
			totalBytes -= c.size
		}
	}
	return removedFiles, removedBytes, survivors[idx:]
}

func (s *Store) cleanStaleTempFiles(now time.Time) {
	if items, err := os.ReadDir(s.RuntimeDir()); err == nil {
		for _, item := range items {
			if !item.IsDir() || !strings.HasPrefix(item.Name(), runTempPrefix) {
				continue
			}
			if info, err := item.Info(); err == nil && now.Sub(info.ModTime()) > staleTempCutoff {
				_ = os.RemoveAll(filepath.Join(s.RuntimeDir(), item.Name()))
			}
		}
	}
	dirs := []string{
		s.RepoDir,
		filepath.Join(s.RepoDir, string(subdirEntries)),
		filepath.Join(s.RepoDir, string(subdirDeps)),
		filepath.Join(s.RepoDir, string(subdirDiscovery)),
		filepath.Join(s.RepoDir, string(subdirMeta)),
		s.RuntimeDir(),
	}
	for _, dir := range dirs {
		items, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.IsDir() || !strings.HasPrefix(item.Name(), atomicTempPrefix) {
				continue
			}
			if info, err := item.Info(); err == nil && now.Sub(info.ModTime()) > staleTempCutoff {
				_ = os.Remove(filepath.Join(dir, item.Name()))
			}
		}
	}
}
