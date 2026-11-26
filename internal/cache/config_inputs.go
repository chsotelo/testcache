package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const configInputsPrefix = "config-"

// ConfigInputs lists what the Vite main process read while loading the project config in the
// last run: files and env vars tc cannot name without starting Node. They join every base key.
type ConfigInputs struct {
	FormatVersion string   `json:"formatVersion"`
	Key           string   `json:"key"`
	Files         []string `json:"files"`
	EnvDir        string   `json:"envDir,omitempty"`
	EnvKeys       []string `json:"envKeys,omitempty"`
}

// ConfigInputsKey identifies the record for one project root, config file and Vite mode. Both
// paths are relative to the repository root, so every checkout of a repository finds it.
func ConfigInputsKey(projectRoot, configFile, mode string) string {
	return shortHashHex(projectRoot + "\x00" + configFile + "\x00" + mode)
}

// LoadConfigInputs loads the record for key, returning nil if missing, corrupt or outdated.
func (s *Store) LoadConfigInputs(key string) *ConfigInputs {
	p := s.configInputsPath(key)
	var rec ConfigInputs
	found, err := readJSON(p, &rec)
	if err != nil || (found && (rec.FormatVersion != FormatVersion || rec.Key != key)) {
		_ = os.Remove(p)
		return nil
	}
	if !found {
		return nil
	}
	touchIfStale(p, time.Now())
	return &rec
}

// SaveConfigInputs atomically writes the record observed by the latest run.
func (s *Store) SaveConfigInputs(rec ConfigInputs) error {
	rec.FormatVersion = FormatVersion
	if err := writeJSONAtomic(s.configInputsPath(rec.Key), rec); err != nil {
		return fmt.Errorf("save config inputs %s: %w", rec.Key, err)
	}
	return nil
}

func (s *Store) configInputsPath(key string) string {
	return filepath.Join(s.RepoDir, string(subdirMeta), configInputsPrefix+key+string(extJSON))
}
