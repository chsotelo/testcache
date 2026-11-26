package fingerprint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chsotelo/testcache/internal/cache"
)

func TestHashGlobListingAndQueryContent(t *testing.T) {
	root := t.TempDir()
	modsDir := filepath.Join(root, "src", "mods")
	if err := os.MkdirAll(filepath.Join(modsDir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(modsDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(modsDir, "a.ts"), "export const a = 1;\n")
	writeTestFile(t, filepath.Join(modsDir, "node_modules", "ignored.js"), "ignored\n")
	writeTestFile(t, filepath.Join(modsDir, ".git", "HEAD"), "ref: refs/heads/main\n")

	h := NewHasher(root, nil, nil)
	baseGlob := cache.GlobDep{Glob: "./mods/*.ts", BaseDir: "src/mods"}
	h1, err := HashGlob(root, baseGlob, h)
	if err != nil {
		t.Fatalf("HashGlob initial: %v", err)
	}

	// Non-exhaustive glob ignores node_modules and .git.
	writeTestFile(t, filepath.Join(modsDir, "node_modules", "new.js"), "still-ignored\n")
	hSame, err := HashGlob(root, baseGlob, h)
	if err != nil || hSame != h1 {
		t.Fatalf("expected node_modules change ignored when exhaustive=false: %q vs %q err=%v", hSame, h1, err)
	}

	// Exhaustive glob includes node_modules and .git.
	exGlob := cache.GlobDep{Glob: "./mods/**", BaseDir: "src/mods", Exhaustive: true}
	hEx1, err := HashGlob(root, exGlob, h)
	if err != nil {
		t.Fatalf("HashGlob exhaustive: %v", err)
	}
	writeTestFile(t, filepath.Join(modsDir, "node_modules", "new2.js"), "tracked\n")
	hEx2, err := HashGlob(root, exGlob, h)
	if err != nil || hEx2 == hEx1 {
		t.Fatalf("expected exhaustive glob hash to change on node_modules add")
	}

	// Non-query glob ignores content edit of existing file, while HasQuery includes content hash.
	writeTestFile(t, filepath.Join(modsDir, "a.ts"), "export const a = 2;\n")
	hAfterContentEdit, err := HashGlob(root, baseGlob, h)
	if err != nil || hAfterContentEdit != h1 {
		t.Fatalf("expected non-query glob listing hash unchanged on content edit: %q vs %q", hAfterContentEdit, h1)
	}

	qGlob := cache.GlobDep{Glob: "./mods/*.ts", BaseDir: "src/mods", HasQuery: true, Query: "?raw"}
	hq1, err := HashGlob(root, qGlob, h)
	if err != nil {
		t.Fatalf("HashGlob query: %v", err)
	}
	writeTestFile(t, filepath.Join(modsDir, "a.ts"), "export const a = 3;\n")
	hq2, err := HashGlob(root, qGlob, h)
	if err != nil || hq2 == hq1 {
		t.Fatalf("expected HasQuery glob hash to change on content edit")
	}

	// Adding and removing a file under baseDir changes non-query glob hash.
	bFile := filepath.Join(modsDir, "b.ts")
	writeTestFile(t, bFile, "export const b = 1;\n")
	hAdd, err := HashGlob(root, baseGlob, h)
	if err != nil || hAdd == h1 {
		t.Fatalf("expected adding file to change glob hash")
	}
	if err := os.Remove(bFile); err != nil {
		t.Fatal(err)
	}
	hRem, err := HashGlob(root, baseGlob, h)
	if err != nil || hRem != h1 {
		t.Fatalf("expected removing added file to restore glob hash: %q vs %q", hRem, h1)
	}
}

func TestHashGlobMissingBaseDir(t *testing.T) {
	root := t.TempDir()
	h := NewHasher(root, nil, nil)
	g := cache.GlobDep{Glob: "./absent/*.ts", BaseDir: "src/absent"}
	got, err := HashGlob(root, g, h)
	if err != nil || got != string(hashMissing) {
		t.Fatalf("expected missing for absent baseDir, got=%q err=%v", got, err)
	}

	k1 := ComputeFullKeyWithInputs(FullKeyInputs{
		BaseKey: "b1",
		Globs:   []cache.GlobHash{{GlobDep: g, Hash: got}},
	})
	k2 := ComputeFullKeyWithInputs(FullKeyInputs{
		BaseKey: "b1",
		Globs:   []cache.GlobHash{{GlobDep: g, Hash: "glob:1234"}},
	})
	if k1 == k2 {
		t.Fatalf("expected glob hash change to invalidate fullKey")
	}
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
