package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreQuarantineLifecycleAndDeleteEntry(t *testing.T) {
	store := NewStore(t.TempDir(), "/tmp/q-repo")

	entry := Entry{
		BaseKey:   "base1",
		FullKey:   "full1",
		Runner:    "vitest",
		TestFile:  "tests/flaky.test.ts",
		Passed:    true,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveEntry(entry); err != nil {
		t.Fatalf("SaveEntry: %v", err)
	}
	if err := store.DeleteEntry("full1"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if got, err := store.LoadEntry("full1"); err != nil || got != nil {
		t.Fatalf("expected deleted entry to be absent, got=%v err=%v", got, err)
	}
	if err := store.DeleteEntry("missing-key"); err != nil {
		t.Fatalf("DeleteEntry missing should be nil, got %v", err)
	}

	if err := store.AddQuarantine(QuarantineEntry{
		TestFile:    "tests/b.test.ts",
		Date:        "2026-11-20T00:00:00Z",
		FailedTests: []string{"suite > flaky case"},
		Reason:      "stale",
	}); err != nil {
		t.Fatalf("AddQuarantine b: %v", err)
	}
	if err := store.AddQuarantine(QuarantineEntry{
		TestFile:    "tests/a.test.ts",
		Date:        "2026-11-20T01:00:00Z",
		FailedTests: []string{"test a"},
	}); err != nil {
		t.Fatalf("AddQuarantine a: %v", err)
	}

	list, err := store.LoadQuarantine()
	if err != nil || len(list) != 2 {
		t.Fatalf("LoadQuarantine got=%+v err=%v", list, err)
	}
	if list[0].TestFile != "tests/a.test.ts" || list[1].TestFile != "tests/b.test.ts" {
		t.Fatalf("expected sorted quarantine list, got %+v", list)
	}

	removed, err := store.RemoveQuarantine("tests/a.test.ts")
	if err != nil || !removed {
		t.Fatalf("RemoveQuarantine a got removed=%v err=%v", removed, err)
	}
	// Invalid transition: releasing a file that is not quarantined must be
	// rejected (removed=false) and leave the list unchanged.
	removedAgain, err := store.RemoveQuarantine("tests/a.test.ts")
	if err != nil || removedAgain {
		t.Fatalf("invalid release of a non-quarantined file: got removed=%v err=%v", removedAgain, err)
	}

	list2, err := store.LoadQuarantine()
	if err != nil || len(list2) != 1 || list2[0].TestFile != "tests/b.test.ts" {
		t.Fatalf("unexpected remaining quarantine: %+v err=%v", list2, err)
	}

	// Corrupt quarantine file recovers cleanly.
	if err := os.WriteFile(filepath.Join(store.RepoDir, fileQuarantineName), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	listCorrupt, err := store.LoadQuarantine()
	if err != nil || len(listCorrupt) != 0 {
		t.Fatalf("expected empty list on corrupt quarantine, got %+v err=%v", listCorrupt, err)
	}
}
