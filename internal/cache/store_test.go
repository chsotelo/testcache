package cache

import (
	"testing"
	"time"
)

func TestStoreSaveAndLoad(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base, "/tmp/my-repo")

	rec := DepRecord{
		BaseKey:  "base123",
		TestFile: "tests/a.test.ts",
		Deps:     []string{"src/a.ts"},
	}
	if err := store.SaveDeps(rec); err != nil {
		t.Fatalf("SaveDeps: %v", err)
	}
	gotRec, err := store.LoadDeps("base123")
	if err != nil || gotRec == nil {
		t.Fatalf("LoadDeps: rec=%v err=%v", gotRec, err)
	}
	if len(gotRec.Deps) != 1 || gotRec.Deps[0] != "src/a.ts" {
		t.Fatalf("unexpected deps: %+v", gotRec)
	}

	failEntry := Entry{
		BaseKey:  "base123",
		FullKey:  "failkey",
		TestFile: "tests/a.test.ts",
		Passed:   false,
	}
	if err := store.SaveEntry(failEntry); err != nil {
		t.Fatalf("SaveEntry fail: %v", err)
	}
	if gotFail, err := store.LoadEntry("failkey"); err != nil || gotFail != nil {
		t.Fatalf("failing entry must never be cached, got=%v err=%v", gotFail, err)
	}

	passEntry := Entry{
		BaseKey:     "base123",
		FullKey:     "passkey",
		Runner:      "vitest",
		TestFile:    "tests/a.test.ts",
		Passed:      true,
		DurationMs:  42,
		TestsPassed: 2,
		TestsTotal:  2,
		Output:      "ok",
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.SaveEntry(passEntry); err != nil {
		t.Fatalf("SaveEntry pass: %v", err)
	}
	gotPass, err := store.LoadEntry("passkey")
	if err != nil || gotPass == nil {
		t.Fatalf("LoadEntry pass: got=%v err=%v", gotPass, err)
	}
	if gotPass.DurationMs != 42 || gotPass.Output != "ok" {
		t.Fatalf("unexpected entry: %+v", gotPass)
	}

	if err := store.RecordStats(2, 1, 500); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}
	st, count, totalBytes, err := store.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if st.Hits != 2 || st.Misses != 1 || st.SavedMs != 500 || count != 1 || totalBytes == 0 {
		t.Fatalf("unexpected summary: st=%+v count=%d bytes=%d", st, count, totalBytes)
	}

	if err := store.Clean(false); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if afterClean, err := store.LoadEntry("passkey"); err != nil || afterClean != nil {
		t.Fatalf("expected entry removed after clean, got=%v err=%v", afterClean, err)
	}
}
