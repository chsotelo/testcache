package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileHashCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "filehashes.json")
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}

	hc, err := LoadFileHashCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := hc.HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := hc.Save(); err != nil {
		t.Fatal(err)
	}

	hc2, err := LoadFileHashCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hc2.HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash mismatch: %s != %s", h1, h2)
	}

	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	h3, err := hc2.HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatalf("expected updated content hash to change")
	}

	calls := 0
	t1, err := hc2.HashFileTransformed(target, "norm-v1", func(data []byte) (string, error) {
		calls++
		return "norm:" + string(data), nil
	})
	if err != nil || t1 != "norm:hello world" || calls != 1 {
		t.Fatalf("transformed hash = %q calls=%d err=%v", t1, calls, err)
	}
}

func TestLookupNodeVersion(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "node")
	if err := os.WriteFile(fakeBin, []byte("bin1"), 0o755); err != nil {
		t.Fatal(err)
	}

	calls := 0
	resolve := func() (string, error) {
		calls++
		return "v24.22.2", nil
	}

	v1, err := LookupNodeVersion(dir, NodeBinary{ResolvedPath: fakeBin}, resolve)
	if err != nil || v1 != "v24.22.2" || calls != 1 {
		t.Fatalf("first lookup: v=%q calls=%d err=%v", v1, calls, err)
	}
	v2, err := LookupNodeVersion(dir, NodeBinary{ResolvedPath: fakeBin}, resolve)
	if err != nil || v2 != "v24.22.2" || calls != 1 {
		t.Fatalf("expected cached lookup without calling resolve, v=%q calls=%d err=%v", v2, calls, err)
	}
}
