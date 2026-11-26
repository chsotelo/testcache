package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/chsotelo/testcache/internal/lock"
)

const (
	fileQuarantineName = "quarantine.json"
	quarantineLockName = "quarantine.lock"
)

// QuarantineEntry records a test file that failed revalidation or verification with identical inputs.
type QuarantineEntry struct {
	TestFile    string   `json:"testFile"`
	Date        string   `json:"date"`
	FailedTests []string `json:"failedTests,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type quarantineFile struct {
	FormatVersion string            `json:"formatVersion"`
	Entries       []QuarantineEntry `json:"entries"`
}

// LoadQuarantine reads quarantined test entries for this repository, ignoring corrupt records.
func (s *Store) LoadQuarantine() ([]QuarantineEntry, error) {
	p := s.quarantinePath()
	var qf quarantineFile
	found, err := readJSON(p, &qf)
	if err != nil || (found && qf.FormatVersion != FormatVersion) {
		_ = os.Remove(p)
		return nil, nil
	}
	if !found {
		return nil, nil
	}
	sortQuarantineEntries(qf.Entries)
	return qf.Entries, nil
}

// AddQuarantine records or updates a quarantined test file under an advisory file lock.
func (s *Store) AddQuarantine(entry QuarantineEntry) error {
	if entry.TestFile == "" {
		return errors.New("quarantine entry missing testFile")
	}
	guard, err := lock.Acquire(filepath.Join(s.LocksDir(), quarantineLockName), nil)
	if err != nil {
		return fmt.Errorf("lock quarantine: %w", err)
	}
	defer guard.Release()

	entries, err := s.LoadQuarantine()
	if err != nil {
		return err
	}
	entries = upsertQuarantineEntry(entries, entry)
	return s.saveQuarantineLocked(entries)
}

// RemoveQuarantine removes a test file from quarantine under an advisory file lock.
func (s *Store) RemoveQuarantine(testFile string) (bool, error) {
	guard, err := lock.Acquire(filepath.Join(s.LocksDir(), quarantineLockName), nil)
	if err != nil {
		return false, fmt.Errorf("lock quarantine: %w", err)
	}
	defer guard.Release()

	entries, err := s.LoadQuarantine()
	if err != nil {
		return false, err
	}
	kept := make([]QuarantineEntry, 0, len(entries))
	removed := false
	for _, e := range entries {
		if e.TestFile == testFile {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return false, nil
	}
	if err := s.saveQuarantineLocked(kept); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) saveQuarantineLocked(entries []QuarantineEntry) error {
	sortQuarantineEntries(entries)
	qf := quarantineFile{
		FormatVersion: FormatVersion,
		Entries:       entries,
	}
	if err := writeJSONAtomic(s.quarantinePath(), qf); err != nil {
		return fmt.Errorf("save quarantine: %w", err)
	}
	return nil
}

func (s *Store) quarantinePath() string {
	return filepath.Join(s.RepoDir, fileQuarantineName)
}

func upsertQuarantineEntry(entries []QuarantineEntry, entry QuarantineEntry) []QuarantineEntry {
	for i := range entries {
		if entries[i].TestFile == entry.TestFile {
			entries[i] = entry
			return entries
		}
	}
	return append(entries, entry)
}

func sortQuarantineEntries(entries []QuarantineEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TestFile < entries[j].TestFile
	})
}
