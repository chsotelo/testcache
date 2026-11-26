package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chsotelo/testcache/internal/lock"
)

const FormatVersion = "tc/v11"

type storeSubdir string

const (
	subdirDeps      storeSubdir = "deps"
	subdirEntries   storeSubdir = "entries"
	subdirLocks     storeSubdir = "locks"
	subdirRuntime   storeSubdir = "runtime"
	subdirDiscovery storeSubdir = "discovery"
	subdirMeta      storeSubdir = "meta"
)

type storeFileExt string

const (
	extJSON             storeFileExt = ".json"
	fileStatsName       string       = "stats.json"
	fileHashesName      string       = "filehashes.json"
	touchStaleThreshold              = time.Minute
)

// DepHash pairs a dependency path (relative to repo/project root) with its content hash.
type DepHash struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// DepRecord stores the dependency paths, globs, and env keys observed in the last passing run for a baseKey.
type DepRecord struct {
	FormatVersion string    `json:"formatVersion"`
	BaseKey       string    `json:"baseKey"`
	TestFile      string    `json:"testFile"`
	Deps          []string  `json:"deps"`
	Globs         []GlobDep `json:"globs,omitempty"`
	EnvVars       []string  `json:"envVars,omitempty"`
	FallbackDeps  bool      `json:"fallbackDeps"`
	UpdatedAt     string    `json:"updatedAt"`
	// Older holds the dependency lists earlier passing runs observed for this base key,
	// newest first. Two checkouts or two branches can give one test different lists; each
	// keeps finding its own entries instead of overwriting the other's list on every run.
	Older []DepVariant `json:"older,omitempty"`
}

// Entry stores the cached outcome for a single test file at a specific fullKey.
type Entry struct {
	FormatVersion string `json:"formatVersion"`
	BaseKey       string `json:"baseKey"`
	FullKey       string `json:"fullKey"`
	Runner        string `json:"runner"`
	TestFile      string `json:"testFile"`
	Passed        bool   `json:"passed"`
	DurationMs    int64  `json:"durationMs"`
	// WallShareMs is this file's part of the wall-clock time of the run that stored it.
	WallShareMs  int64     `json:"wallShareMs,omitempty"`
	TestsPassed  int       `json:"testsPassed"`
	TestsFailed  int       `json:"testsFailed"`
	TestsSkipped int       `json:"testsSkipped"`
	TestsTotal   int       `json:"testsTotal"`
	Output       string    `json:"output"`
	FallbackDeps bool      `json:"fallbackDeps"`
	Deps         []DepHash `json:"deps"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Stats tracks cumulative cache hits, misses, and time saved for a repository.
type Stats struct {
	Hits        int64  `json:"hits"`
	Misses      int64  `json:"misses"`
	SavedMs     int64  `json:"savedMs"`
	LastUpdated string `json:"lastUpdated,omitempty"`
}

// Store manages cached test entries, dependency records, locks, and stats for one repository.
type Store struct {
	BaseDir string
	RepoID  string
	RepoDir string
	// CheckoutID tells the checkouts of one repository apart (see ComputeCheckoutID).
	CheckoutID string
	// OnCorrupt, when set, is told about every unreadable entry or dependency record removed.
	OnCorrupt func(path string)
}

// NewStore returns a Store rooted under baseDir for the given resolved repoRoot.
func NewStore(baseDir, repoRoot string) *Store {
	id := ComputeRepoID(ResolveRepoIdentity(repoRoot))
	return &Store{
		BaseDir:    baseDir,
		RepoID:     id,
		RepoDir:    filepath.Join(baseDir, id),
		CheckoutID: ComputeCheckoutID(repoRoot),
	}
}

// ComputeRepoID returns the cache directory name for a repository identity (see ResolveRepoIdentity).
func ComputeRepoID(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}

// LocksDir returns the directory used for per-key and semaphore lock files.
func (s *Store) LocksDir() string {
	return filepath.Join(s.RepoDir, string(subdirLocks))
}

// RuntimeDir returns the directory used for embedded runner scripts and temp run outputs.
func (s *Store) RuntimeDir() string {
	return filepath.Join(s.RepoDir, string(subdirRuntime))
}

// LoadDeps loads the dependency list for baseKey, returning (nil, nil) if absent or corrupt.
func (s *Store) LoadDeps(baseKey string) (*DepRecord, error) {
	p := s.depsPath(baseKey)
	var rec DepRecord
	found, err := readJSON(p, &rec)
	if err != nil || (found && !isValidDepRecord(rec, baseKey)) {
		s.reportCorrupt(p, err)
		_ = os.Remove(p)
		return nil, nil
	}
	if !found {
		return nil, nil
	}
	return &rec, nil
}

// SaveDeps atomically writes the dependency list for baseKey.
func (s *Store) SaveDeps(rec DepRecord) error {
	rec.FormatVersion = FormatVersion
	previous, _ := s.LoadDeps(rec.BaseKey)
	rec.Older = keepOlderVariants(previous, rec)
	p := s.depsPath(rec.BaseKey)
	if err := writeJSONAtomic(p, rec); err != nil {
		return fmt.Errorf("save deps %s: %w", rec.BaseKey, err)
	}
	return nil
}

// LoadEntry loads the cached test result for fullKey, returning (nil, nil) if absent or corrupt.
func (s *Store) LoadEntry(fullKey string) (*Entry, error) {
	p := s.entryPath(fullKey)
	var e Entry
	found, err := readJSON(p, &e)
	if err != nil || (found && !isValidEntry(e, fullKey)) {
		s.reportCorrupt(p, err)
		_ = os.Remove(p)
		return nil, nil
	}
	if !found {
		return nil, nil
	}
	return &e, nil
}

// SaveEntry persists a passing test result atomically. Failures are never cached.
func (s *Store) SaveEntry(e Entry) error {
	if !e.Passed {
		return nil
	}
	e.FormatVersion = FormatVersion
	p := s.entryPath(e.FullKey)
	if err := writeJSONAtomic(p, e); err != nil {
		return fmt.Errorf("save entry %s: %w", e.FullKey, err)
	}
	return nil
}

// DeleteEntry removes a cached test entry by fullKey if it exists.
func (s *Store) DeleteEntry(fullKey string) error {
	if fullKey == "" {
		return nil
	}
	err := os.Remove(s.entryPath(fullKey))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete entry %s: %w", fullKey, err)
	}
	return nil
}

// TouchHit updates the last-used mtime on an entry and its dep record without rewriting contents.
func (s *Store) TouchHit(baseKey, fullKey string) {
	now := time.Now()
	touchIfStale(s.entryPath(fullKey), now)
	touchIfStale(s.depsPath(baseKey), now)
}

func (s *Store) depsPath(baseKey string) string {
	return filepath.Join(s.RepoDir, string(subdirDeps), baseKey+string(extJSON))
}

func (s *Store) entryPath(fullKey string) string {
	return filepath.Join(s.RepoDir, string(subdirEntries), fullKey+string(extJSON))
}

func isValidDepRecord(rec DepRecord, wantBaseKey string) bool {
	return rec.FormatVersion == FormatVersion && rec.BaseKey == wantBaseKey && rec.TestFile != ""
}

func isValidEntry(e Entry, wantFullKey string) bool {
	return e.Passed && e.FormatVersion == FormatVersion && e.FullKey == wantFullKey &&
		e.BaseKey != "" && e.TestFile != ""
}

func touchIfStale(path string, now time.Time) {
	info, err := os.Stat(path)
	if err != nil || now.Sub(info.ModTime()) < touchStaleThreshold {
		return
	}
	_ = os.Chtimes(path, now, now)
}

// RecordStats atomically increments hits, misses, and savedMs under an advisory lock.
func (s *Store) RecordStats(hits, misses int64, savedMs int64) error {
	if hits == 0 && misses == 0 && savedMs == 0 {
		return nil
	}
	guard, err := lock.Acquire(filepath.Join(s.LocksDir(), "stats.lock"), nil)
	if err != nil {
		return fmt.Errorf("lock stats: %w", err)
	}
	defer guard.Release()

	st, err := s.LoadStats()
	if err != nil {
		return err
	}
	st.Hits += hits
	st.Misses += misses
	st.SavedMs += savedMs
	st.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if err := writeJSONAtomic(filepath.Join(s.RepoDir, fileStatsName), st); err != nil {
		return fmt.Errorf("save stats: %w", err)
	}
	return nil
}

// LoadStats reads cumulative stats for this repository, resetting corrupt stats files.
func (s *Store) LoadStats() (Stats, error) {
	var st Stats
	p := filepath.Join(s.RepoDir, fileStatsName)
	_, err := readJSON(p, &st)
	if err != nil {
		_ = os.Remove(p)
		return Stats{}, nil
	}
	return st, nil
}

// Summary returns stats, cached entry count, and total cache size in bytes for this repository.
func (s *Store) Summary() (Stats, int, int64, error) {
	st, err := s.LoadStats()
	if err != nil {
		return Stats{}, 0, 0, err
	}
	count, entryBytes := sumDirFiles(filepath.Join(s.RepoDir, string(subdirEntries)))
	totalBytes := entryBytes
	for _, sub := range []storeSubdir{subdirDeps, subdirDiscovery, subdirMeta} {
		_, b := sumDirFiles(filepath.Join(s.RepoDir, string(sub)))
		totalBytes += b
	}
	for _, name := range []string{fileHashesName, fileStatsName, fileQuarantineName} {
		if info, err := os.Stat(filepath.Join(s.RepoDir, name)); err == nil && !info.IsDir() {
			totalBytes += info.Size()
		}
	}
	return st, count, totalBytes, nil
}

func sumDirFiles(dir string) (int, int64) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	var count int
	var total int64
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		count++
		if info, err := item.Info(); err == nil {
			total += info.Size()
		}
	}
	return count, total
}

// Clean removes cached data for this repository (or all repositories when all is true).
func (s *Store) Clean(all bool) error {
	target := s.RepoDir
	if all {
		target = s.BaseDir
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("clean cache %s: %w", target, err)
	}
	return nil
}

// reportCorrupt tells OnCorrupt about a file that could not be read or parsed. A record that
// parses but belongs to another format version or key is an ordinary miss, not corruption.
func (s *Store) reportCorrupt(path string, readErr error) {
	if readErr != nil && s.OnCorrupt != nil {
		s.OnCorrupt(path)
	}
}

func readJSON(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSONAtomic(path string, src any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o644)
}

// WriteFileAtomic writes data to path atomically via a same-directory temp file, fsync, and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
