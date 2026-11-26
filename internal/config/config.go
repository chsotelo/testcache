package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

type configFileName string

const (
	// DefaultMaxConcurrentRuns limits concurrent Vitest processes per repo.
	DefaultMaxConcurrentRuns = 1
	// DefaultMaxCacheBytes caps repo cache size at 1 GiB by default.
	DefaultMaxCacheBytes int64 = 1 << 30
	// DefaultMaxAgeDays expires unused cache entries after 30 days by default.
	DefaultMaxAgeDays = 30
	// DefaultNormalizeHash enables comment-insensitive source hashing by default.
	DefaultNormalizeHash = true
	// DefaultRevalidateRate sets the probability of re-running a cache hit to detect flakiness.
	DefaultRevalidateRate                = 0.02
	defaultConfigFile     configFileName = "testcache.json"
	userConfigFile        configFileName = "config.json"
	// EnvDisable is the environment variable that turns every tc integration into a no-op.
	EnvDisable = "TC_DISABLE"
)

// Config holds user and repository configuration for testcache.
type Config struct {
	Exclude           []string `json:"exclude"`
	Env               []string `json:"env"`
	MaxConcurrentRuns int      `json:"maxConcurrentRuns"`
	MaxWorkers        int      `json:"maxWorkers"`
	MaxCacheBytes     int64    `json:"maxCacheBytes"`
	MaxAgeDays        int      `json:"maxAgeDays"`
	NormalizeHash     bool     `json:"normalizeHash"`
	RevalidateRate    float64  `json:"revalidateRate"`
	// AlertCommand runs through /bin/sh -c with the run-log line on stdin when a run has anomalies.
	AlertCommand string `json:"alertCommand"`
}

type rawConfig struct {
	Exclude           []string `json:"exclude"`
	Env               []string `json:"env"`
	MaxConcurrentRuns int      `json:"maxConcurrentRuns"`
	MaxWorkers        int      `json:"maxWorkers"`
	MaxCacheBytes     int64    `json:"maxCacheBytes"`
	MaxAgeDays        int      `json:"maxAgeDays"`
	NormalizeHash     *bool    `json:"normalizeHash"`
	RevalidateRate    *float64 `json:"revalidateRate"`
	AlertCommand      *string  `json:"alertCommand"`
}

// IsDisabled reports whether TC_DISABLE asks tc to step aside (any value except empty and "0").
func IsDisabled() bool {
	v := os.Getenv(EnvDisable)
	return v != "" && v != "0"
}

// DefaultMaxWorkers returns a sane default Vitest worker cap for this machine.
func DefaultMaxWorkers() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// Load reads and merges config files from user config dir, repo root, and project root.
// It also returns a deterministic hash of the effective configuration.
func Load(repoRoot, projectRoot string) (Config, string, error) {
	cfg := Config{
		MaxConcurrentRuns: DefaultMaxConcurrentRuns,
		MaxWorkers:        DefaultMaxWorkers(),
		MaxCacheBytes:     DefaultMaxCacheBytes,
		MaxAgeDays:        DefaultMaxAgeDays,
		NormalizeHash:     DefaultNormalizeHash,
		RevalidateRate:    DefaultRevalidateRate,
	}

	var paths []string
	if userDir, err := UserConfigDir(); err == nil && userDir != "" {
		paths = append(paths, filepath.Join(userDir, string(userConfigFile)))
		paths = append(paths, filepath.Join(userDir, string(defaultConfigFile)))
	}
	if repoRoot != "" {
		paths = append(paths, filepath.Join(repoRoot, string(defaultConfigFile)))
	}
	if projectRoot != "" && projectRoot != repoRoot {
		paths = append(paths, filepath.Join(projectRoot, string(defaultConfigFile)))
	}

	for _, p := range paths {
		loaded, err := readConfigFile(p)
		if err != nil {
			return Config{}, "", err
		}
		if loaded != nil {
			cfg = mergeConfigs(cfg, *loaded)
		}
	}

	cfg.Exclude = dedupSorted(cfg.Exclude)
	cfg.Env = dedupSorted(cfg.Env)
	return cfg, hashConfig(cfg), nil
}

// UserConfigDir returns ~/.config/testcache unless TESTCACHE_CONFIG_DIR is set.
func UserConfigDir() (string, error) {
	if v := os.Getenv("TESTCACHE_CONFIG_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "testcache"), nil
}

// CacheBaseDir returns ~/.cache/testcache unless TESTCACHE_DIR is set.
func CacheBaseDir() (string, error) {
	if v := os.Getenv("TESTCACHE_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "testcache"), nil
}

func readConfigFile(path string) (*rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c rawConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &c, nil
}

func mergeConfigs(base Config, over rawConfig) Config {
	out := base
	out.Exclude = append(append([]string(nil), base.Exclude...), over.Exclude...)
	out.Env = append(append([]string(nil), base.Env...), over.Env...)
	if over.MaxConcurrentRuns > 0 {
		out.MaxConcurrentRuns = over.MaxConcurrentRuns
	}
	if over.MaxWorkers > 0 {
		out.MaxWorkers = over.MaxWorkers
	}
	if over.MaxCacheBytes > 0 {
		out.MaxCacheBytes = over.MaxCacheBytes
	}
	if over.MaxAgeDays > 0 {
		out.MaxAgeDays = over.MaxAgeDays
	}
	if over.NormalizeHash != nil {
		out.NormalizeHash = *over.NormalizeHash
	}
	if over.RevalidateRate != nil {
		out.RevalidateRate = clampRate(*over.RevalidateRate)
	}
	if over.AlertCommand != nil {
		out.AlertCommand = *over.AlertCommand
	}
	return out
}

func clampRate(rate float64) float64 {
	if rate < 0 {
		return 0
	}
	if rate > 1 {
		return 1
	}
	return rate
}

func dedupSorted(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func hashConfig(cfg Config) string {
	// GC bounds do not affect test execution semantics, so exclude them from baseKey.
	keyCfg := struct {
		Exclude           []string `json:"exclude"`
		Env               []string `json:"env"`
		MaxConcurrentRuns int      `json:"maxConcurrentRuns"`
		MaxWorkers        int      `json:"maxWorkers"`
		NormalizeHash     bool     `json:"normalizeHash"`
	}{
		Exclude:           cfg.Exclude,
		Env:               cfg.Env,
		MaxConcurrentRuns: cfg.MaxConcurrentRuns,
		MaxWorkers:        cfg.MaxWorkers,
		NormalizeHash:     cfg.NormalizeHash,
	}
	b, _ := json.Marshal(keyCfg)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
