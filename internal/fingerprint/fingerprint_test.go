package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
)

func TestParseGitOutputs(t *testing.T) {
	lsData := []byte("100644 aaaa1111 0\tsrc/clean.ts\x00100644 bbbb2222 0\tsrc/dirty.ts\x00")
	blobs := ParseLsFilesZ(lsData)
	if blobs["src/clean.ts"] != "aaaa1111" || blobs["src/dirty.ts"] != "bbbb2222" {
		t.Fatalf("unexpected blobs: %+v", blobs)
	}

	statusData := []byte(" M src/dirty.ts\x00?? testdata/newdir/\x00R  src/new.ts\x00src/old.ts\x00")
	exact, prefixes := ParseStatusPorcelainZ(statusData)
	gs := &GitState{
		TrackedBlobs:  blobs,
		DirtyExact:    exact,
		DirtyPrefixes: prefixes,
	}

	if h, ok := gs.CleanBlobHash("src/clean.ts"); !ok || h != "aaaa1111" {
		t.Fatalf("expected clean blob hash for src/clean.ts, got %q ok=%v", h, ok)
	}
	if _, ok := gs.CleanBlobHash("src/dirty.ts"); ok {
		t.Fatalf("expected dirty file to skip blob fast-path")
	}
	if _, ok := gs.CleanBlobHash("testdata/newdir/file.ts"); ok {
		t.Fatalf("expected untracked dir child to skip blob fast-path")
	}
	if _, ok := exact["src/new.ts"]; !ok {
		t.Fatalf("expected renamed dest in DirtyExact")
	}
	if _, ok := exact["src/old.ts"]; !ok {
		t.Fatalf("expected renamed source in DirtyExact")
	}
}

func TestHasherCleanVsDirtyAndKeys(t *testing.T) {
	dir := t.TempDir()
	cleanFile := filepath.Join(dir, "clean.ts")
	dirtyFile := filepath.Join(dir, "dirty.ts")
	if err := os.WriteFile(cleanFile, []byte("export const a = 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dirtyFile, []byte("export const b = 2;"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Second)
	_ = os.Chtimes(dirtyFile, old, old)

	fc, err := cache.LoadFileHashCache(filepath.Join(dir, "fh.json"))
	if err != nil {
		t.Fatal(err)
	}
	gs := &GitState{
		RepoRoot:     dir,
		TrackedBlobs: map[string]string{"clean.ts": "blob123", "dirty.ts": "blob456"},
		DirtyExact:   map[string]struct{}{"dirty.ts": {}},
	}
	h := NewHasher(dir, gs, fc)

	// Clean or modified, a file hashes as the SHA-256 of its bytes: one content, one hash.
	for _, file := range []string{cleanFile, dirtyFile} {
		got, err := h.HashFile(file)
		if err != nil || !strings.HasPrefix(got, rawHashPrefix) || len(got) != len(rawHashPrefix)+64 {
			t.Fatalf("hash of %s = %q err=%v", file, got, err)
		}
	}
	gotMissing, err := h.HashFile(filepath.Join(dir, "nope.ts"))
	if err != nil || gotMissing != "missing" {
		t.Fatalf("missing hash = %q err=%v", gotMissing, err)
	}

	b1 := ComputeBaseKey(BaseKeyInputs{
		FormatVersion: cache.FormatVersion,
		Runner:        "vitest",
		RunnerVersion: "4.1.11",
		NodeVersion:   "v24.22.2",
		TestFile:      "tests/a.test.ts",
		TestFileHash:  "sha256:1",
		Env:           []EnvPair{{Key: "NODE_ENV", Value: "test", Set: true}},
	})
	b2 := ComputeBaseKey(BaseKeyInputs{
		FormatVersion: cache.FormatVersion,
		Runner:        "vitest",
		RunnerVersion: "4.1.11",
		NodeVersion:   "v24.22.2",
		TestFile:      "tests/a.test.ts",
		TestFileHash:  "sha256:1",
		Env:           []EnvPair{{Key: "NODE_ENV", Value: "production", Set: true}},
	})
	if b1 == b2 {
		t.Fatalf("expected env change to invalidate baseKey")
	}

	f1 := ComputeFullKey(b1, []cache.DepHash{{Path: "src/a.ts", Hash: "h1"}})
	f2 := ComputeFullKey(b1, []cache.DepHash{{Path: "src/a.ts", Hash: "h2"}})
	if f1 == f2 {
		t.Fatalf("expected dep hash change to invalidate fullKey")
	}
}

func TestHasherDirectoryAndEnvKeys(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "entries")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fc, err := cache.LoadFileHashCache(filepath.Join(dir, "fh.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHasher(dir, nil, fc)

	missingHash, err := h.HashFile(filepath.Join(dir, "absent"))
	if err != nil || missingHash != "missing" {
		t.Fatalf("absent hash = %q err=%v", missingHash, err)
	}
	emptyDirHash, err := h.HashFile(subDir)
	if err != nil || emptyDirHash == "missing" {
		t.Fatalf("empty dir hash = %q err=%v", emptyDirHash, err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	oneFileDirHash, err := h.HashFile(subDir)
	if err != nil || oneFileDirHash == emptyDirHash {
		t.Fatalf("expected dir hash change after adding entry: %q == %q", oneFileDirHash, emptyDirHash)
	}

	k1 := ComputeFullKeyWithEnv("base", nil, []EnvPair{{Key: "FOO", Set: false}})
	k2 := ComputeFullKeyWithEnv("base", nil, []EnvPair{{Key: "FOO", Value: "bar", Set: true}})
	if k1 == k2 {
		t.Fatalf("expected env set vs unset to produce different full keys")
	}
}
