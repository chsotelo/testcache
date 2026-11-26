package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCAgeAndSizeBounds(t *testing.T) {
	store := NewStore(t.TempDir(), "/tmp/gc-repo")
	now := time.Now()

	// Create 3 entries: one expired (>30d), one old-LRU (5d), one recent (hit touched).
	saveTestEntry := func(key, payload string, age time.Duration) string {
		e := Entry{
			BaseKey:   "base-" + key,
			FullKey:   key,
			Runner:    "vitest",
			TestFile:  "tests/" + key + ".test.ts",
			Passed:    true,
			Output:    payload,
			CreatedAt: now.Add(-age),
		}
		if err := store.SaveEntry(e); err != nil {
			t.Fatal(err)
		}
		p := store.entryPath(key)
		ts := now.Add(-age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
		return p
	}

	expiredPath := saveTestEntry("expired", strings.Repeat("x", 200), 35*24*time.Hour)
	oldPath := saveTestEntry("old", strings.Repeat("y", 400), 5*24*time.Hour)
	freshPath := saveTestEntry("fresh", strings.Repeat("z", 400), 2*24*time.Hour)

	// Simulate hit touching fresh entry so its mtime is updated to now without rewriting.
	store.TouchHit("base-fresh", "fresh")

	// Set MaxBytes so only one 400+ byte entry fits after expired is removed.
	infoFresh, err := os.Stat(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.RunGC(GCOptions{
		MaxBytes: infoFresh.Size() + 50,
		MaxAge:   30 * 24 * time.Hour,
		Now:      now,
		Blocking: true,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if res.RemovedFiles != 2 || res.RemainingEntries != 1 {
		t.Fatalf("unexpected GC result: %+v", res)
	}
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Fatalf("expected expired entry removed")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected LRU old entry removed")
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("expected touched fresh entry kept: %v", err)
	}
}

func TestGCOpportunisticStampAndStaleTemps(t *testing.T) {
	store := NewStore(t.TempDir(), "/tmp/gc-op")
	staleRun := filepath.Join(store.RuntimeDir(), "run-killed")
	if err := os.MkdirAll(staleRun, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(staleRun, old, old); err != nil {
		t.Fatal(err)
	}

	store.MaybeRunOpportunisticGC(1<<30, 30)
	if _, err := os.Stat(staleRun); !os.IsNotExist(err) {
		t.Fatalf("expected stale run-* dir cleaned by opportunistic GC")
	}

	// Second call within 24h should be skipped by gc.stamp.
	staleRun2 := filepath.Join(store.RuntimeDir(), "run-killed2")
	if err := os.MkdirAll(staleRun2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleRun2, old, old); err != nil {
		t.Fatal(err)
	}
	store.MaybeRunOpportunisticGC(1<<30, 30)
	if _, err := os.Stat(staleRun2); err != nil {
		t.Fatalf("expected opportunistic GC skipped while stamp is fresh: %v", err)
	}
}
