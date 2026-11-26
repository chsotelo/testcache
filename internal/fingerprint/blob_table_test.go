package fingerprint

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chsotelo/testcache/internal/cache"
)

// The same bytes hash the same whether git calls the file clean, modified or unknown, and a
// clean file is answered from the blob table without being read again.
func TestRawHashIsOneSchemeAndUsesTheBlobTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	files, err := cache.LoadFileHashCache(filepath.Join(dir, "filehashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	clean := &GitState{RepoRoot: dir, TrackedBlobs: map[string]string{"data.json": "blob-1"}, LoadedAt: time.Now()}
	dirty := &GitState{RepoRoot: dir, TrackedBlobs: map[string]string{"data.json": "blob-0"}, DirtyExact: map[string]struct{}{"data.json": {}}, LoadedAt: time.Now()}

	asClean, err := NewHasher(dir, clean, files).HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	asDirty, _ := NewHasher(dir, dirty, files).HashFile(path)
	asUnknown, _ := NewHasher(dir, nil, nil).HashFile(path)
	if asClean != asDirty || asClean != asUnknown {
		t.Fatalf("one content must have one hash: clean=%s dirty=%s no-git=%s", asClean, asDirty, asUnknown)
	}
	if digest, ok := files.LookupBlobDigest("blob-1"); !ok || rawHashPrefix+digest != asClean {
		t.Fatalf("the blob table must learn the digest of a clean file: %q %v", digest, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if again, err := NewHasher(dir, clean, files).HashFile(path); err == nil && again == asClean {
		t.Fatal("a missing file is missing, whatever the table says")
	}
}

// A file written after git reported it clean must not teach the table a wrong pair.
func TestBlobTableIgnoresFilesNewerThanTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte("written after the snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := cache.LoadFileHashCache(filepath.Join(dir, "filehashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	stale := &GitState{RepoRoot: dir, TrackedBlobs: map[string]string{"data.json": "blob-1"}, LoadedAt: time.Now().Add(-time.Hour)}
	if _, err := NewHasher(dir, stale, files).HashFile(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := files.LookupBlobDigest("blob-1"); ok {
		t.Fatal("nothing may be recorded for a file modified after the git snapshot")
	}
}
