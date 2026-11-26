package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const latestPrefix = "latest-"

// DiscoveryRecord caches Vitest's effective test file list for a project configuration.
type DiscoveryRecord struct {
	FormatVersion string    `json:"formatVersion"`
	Key           string    `json:"key"`
	ProjectRoot   string    `json:"projectRoot"`
	Files         []string  `json:"files"`
	CustomInclude bool      `json:"customInclude,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// EnvSnapshot records the observed state of one environment variable for explain diagnostics.
type EnvSnapshot struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	Set   bool   `json:"set"`
}

// GlobDep records an affirmed import.meta.glob dependency attributed to a test file.
type GlobDep struct {
	Importer   string   `json:"importer,omitempty"`
	Glob       string   `json:"glob"`
	Negated    []string `json:"negated,omitempty"`
	BaseDir    string   `json:"baseDir"`
	Exhaustive bool     `json:"exhaustive,omitempty"`
	HasQuery   bool     `json:"hasQuery,omitempty"`
	Query      string   `json:"query,omitempty"`
}

// GlobHash pairs a recorded GlobDep with its computed directory/content hash.
type GlobHash struct {
	GlobDep
	Hash string `json:"hash"`
}

// TestFileMeta stores the last run's base inputs and last passing deps/env for tc explain.
type TestFileMeta struct {
	FormatVersion      string        `json:"formatVersion"`
	TestFile           string        `json:"testFile"`
	BaseKey            string        `json:"baseKey"`
	FullKey            string        `json:"fullKey,omitempty"`
	Passed             bool          `json:"passed"`
	UncacheableReason  string        `json:"uncacheableReason,omitempty"`
	UpdatedAt          time.Time     `json:"updatedAt"`
	RunnerVersion      string        `json:"runnerVersion"`
	NodeVersion        string        `json:"nodeVersion"`
	ConfigHash         string        `json:"configHash"`
	ProjectFiles       []DepHash     `json:"projectFiles"`
	Args               []string      `json:"args,omitempty"`
	TestFileHash       string        `json:"testFileHash"`
	AllowEnv           []EnvSnapshot `json:"allowEnv,omitempty"`
	LastPassFullKey    string        `json:"lastPassFullKey,omitempty"`
	LastPassDeps       []DepHash     `json:"lastPassDeps,omitempty"`
	LastPassGlobs      []GlobHash    `json:"lastPassGlobs,omitempty"`
	LastPassDynamicEnv []EnvSnapshot `json:"lastPassDynamicEnv,omitempty"`
}

// LoadDiscovery loads a cached test file discovery list by key, returning nil if missing or corrupt.
func (s *Store) LoadDiscovery(key string) *DiscoveryRecord {
	p := s.discoveryPath(key)
	var rec DiscoveryRecord
	found, err := readJSON(p, &rec)
	if err != nil || (found && (rec.FormatVersion != FormatVersion || rec.Key != key || rec.ProjectRoot == "")) {
		_ = os.Remove(p)
		return nil
	}
	if !found {
		return nil
	}
	return &rec
}

// SaveDiscovery atomically writes a discovery record and updates the per-project latest snapshot.
func (s *Store) SaveDiscovery(rec DiscoveryRecord) error {
	rec.FormatVersion = FormatVersion
	if err := writeJSONAtomic(s.discoveryPath(rec.Key), rec); err != nil {
		return fmt.Errorf("save discovery %s: %w", rec.Key, err)
	}
	if rec.ProjectRoot != "" {
		_ = writeJSONAtomic(s.latestDiscoveryPath(rec.ProjectRoot), rec)
	}
	return nil
}

// TouchDiscovery updates the discovery record's last-used mtime if stale.
func (s *Store) TouchDiscovery(key string) {
	touchIfStale(s.discoveryPath(key), time.Now())
}

// LoadLatestDiscovery loads the most recent discovery list for projectRoot without running Vitest.
func (s *Store) LoadLatestDiscovery(projectRoot string) *DiscoveryRecord {
	p := s.latestDiscoveryPath(projectRoot)
	var rec DiscoveryRecord
	found, err := readJSON(p, &rec)
	if err != nil || (found && (rec.FormatVersion != FormatVersion || rec.ProjectRoot != projectRoot)) {
		_ = os.Remove(p)
		return nil
	}
	if !found {
		return nil
	}
	return &rec
}

// LoadTestMeta loads the last run metadata for testFile.
// If the on-disk record has a different FormatVersion, it removes the file and returns ("", oldVersion).
func (s *Store) LoadTestMeta(testFile string) (*TestFileMeta, string) {
	p := s.metaPath(testFile)
	var meta TestFileMeta
	found, err := readJSON(p, &meta)
	if err != nil {
		_ = os.Remove(p)
		return nil, ""
	}
	if !found {
		return nil, ""
	}
	if meta.FormatVersion != FormatVersion {
		oldVer := meta.FormatVersion
		_ = os.Remove(p)
		return nil, oldVer
	}
	if meta.TestFile == "" || meta.TestFile != testFile {
		_ = os.Remove(p)
		return nil, ""
	}
	return &meta, ""
}

// SaveTestMeta atomically saves the last run metadata for a test file.
func (s *Store) SaveTestMeta(meta TestFileMeta) error {
	meta.FormatVersion = FormatVersion
	p := s.metaPath(meta.TestFile)
	if err := writeJSONAtomic(p, meta); err != nil {
		return fmt.Errorf("save test meta %s: %w", meta.TestFile, err)
	}
	return nil
}

func (s *Store) discoveryPath(key string) string {
	return filepath.Join(s.RepoDir, string(subdirDiscovery), key+string(extJSON))
}

func (s *Store) latestDiscoveryPath(projectRoot string) string {
	return filepath.Join(s.RepoDir, string(subdirDiscovery), latestPrefix+shortHashHex(projectRoot)+string(extJSON))
}

func (s *Store) metaPath(testFile string) string {
	return filepath.Join(s.RepoDir, string(subdirMeta), shortHashHex(testFile)+string(extJSON))
}

func shortHashHex(val string) string {
	sum := sha256.Sum256([]byte(val))
	return hex.EncodeToString(sum[:12])
}
