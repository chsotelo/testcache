package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCorruptedCacheFilesAreMissedAndRemoved(t *testing.T) {
	store := NewStore(t.TempDir(), "/tmp/corrupt-repo")
	garbagePayloads := [][]byte{
		[]byte("{truncated"),
		[]byte("\x00\xff\xfe"),
		[]byte("null"),
		[]byte("{}"),
	}

	for _, payload := range garbagePayloads {
		entryPath := store.entryPath("k1")
		mustWriteCorrupt(t, entryPath, payload)
		if got, err := store.LoadEntry("k1"); err != nil || got != nil {
			t.Fatalf("LoadEntry corrupt=%q got=%v err=%v", payload, got, err)
		}
		assertRemoved(t, entryPath)

		depsPath := store.depsPath("b1")
		mustWriteCorrupt(t, depsPath, payload)
		if got, err := store.LoadDeps("b1"); err != nil || got != nil {
			t.Fatalf("LoadDeps corrupt=%q got=%v err=%v", payload, got, err)
		}
		assertRemoved(t, depsPath)

		discPath := store.discoveryPath("d1")
		mustWriteCorrupt(t, discPath, payload)
		if got := store.LoadDiscovery("d1"); got != nil {
			t.Fatalf("LoadDiscovery corrupt=%q got=%v", payload, got)
		}
		assertRemoved(t, discPath)

		metaPath := store.metaPath("tests/a.test.ts")
		mustWriteCorrupt(t, metaPath, payload)
		if got, oldVer := store.LoadTestMeta("tests/a.test.ts"); got != nil || oldVer != "" {
			t.Fatalf("LoadTestMeta corrupt=%q got=%v oldVer=%q", payload, got, oldVer)
		}
		assertRemoved(t, metaPath)
	}

	// Corrupt filehashes.json, node-versions.json, and stats.json.
	fhPath := filepath.Join(store.RepoDir, fileHashesName)
	mustWriteCorrupt(t, fhPath, []byte("{bad-json"))
	hc, err := LoadFileHashCache(fhPath)
	if err != nil || hc == nil {
		t.Fatalf("LoadFileHashCache corrupt: hc=%v err=%v", hc, err)
	}
	assertRemoved(t, fhPath)

	nvPath := filepath.Join(store.BaseDir, nodeVersionsFile)
	mustWriteCorrupt(t, nvPath, []byte("null"))
	fakeBin := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(fakeBin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupCachedNodeVersion(store.BaseDir, NodeBinary{ResolvedPath: fakeBin}); ok {
		t.Fatalf("expected LookupCachedNodeVersion false on corrupt file")
	}
	assertRemoved(t, nvPath)

	statsPath := filepath.Join(store.RepoDir, fileStatsName)
	mustWriteCorrupt(t, statsPath, []byte("{broken"))
	if st, err := store.LoadStats(); err != nil || st.Hits != 0 {
		t.Fatalf("LoadStats corrupt: st=%+v err=%v", st, err)
	}
	assertRemoved(t, statsPath)
}

func TestVersionMismatchInvalidatesAndRemoves(t *testing.T) {
	store := NewStore(t.TempDir(), "/tmp/ver-repo")

	// Write an entry and meta with an old formatVersion.
	if err := writeJSONAtomic(store.entryPath("oldk"), Entry{
		FormatVersion: "tc/v0",
		BaseKey:       "oldb",
		FullKey:       "oldk",
		TestFile:      "tests/a.test.ts",
		Passed:        true,
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadEntry("oldk"); err != nil || got != nil {
		t.Fatalf("expected old format entry ignored, got=%v err=%v", got, err)
	}
	assertRemoved(t, store.entryPath("oldk"))

	if err := writeJSONAtomic(store.metaPath("tests/a.test.ts"), TestFileMeta{
		FormatVersion: "tc/v0",
		TestFile:      "tests/a.test.ts",
		BaseKey:       "oldb",
		Passed:        true,
	}); err != nil {
		t.Fatal(err)
	}
	gotMeta, oldVer := store.LoadTestMeta("tests/a.test.ts")
	if gotMeta != nil || oldVer != "tc/v0" {
		t.Fatalf("expected oldVer=tc/v0 and nil meta, got meta=%v oldVer=%q", gotMeta, oldVer)
	}
	assertRemoved(t, store.metaPath("tests/a.test.ts"))
}

func mustWriteCorrupt(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected corrupt/stale file %s to be removed, stat err=%v", path, err)
	}
}
