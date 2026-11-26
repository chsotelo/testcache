package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRepoIdentity(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "main")
	gitDir := filepath.Join(main, ".git")
	worktreeGit := filepath.Join(gitDir, "worktrees", "feature")
	mustWrite(t, filepath.Join(worktreeGit, "commondir"), "../..\n")
	linked := filepath.Join(root, "linked")
	mustWrite(t, filepath.Join(linked, ".git"), "gitdir: "+worktreeGit+"\n")
	submodule := filepath.Join(main, "sub")
	mustWrite(t, filepath.Join(gitDir, "modules", "sub", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(submodule, ".git"), "gitdir: ../.git/modules/sub\n")
	plain := filepath.Join(root, "plain")
	mustWrite(t, filepath.Join(plain, "package.json"), "{}")
	broken := filepath.Join(root, "broken")
	mustWrite(t, filepath.Join(broken, ".git"), "not a git file\n")

	cases := map[string]struct{ repoRoot, want string }{
		"main checkout":    {main, gitDir},
		"linked worktree":  {linked, gitDir},
		"submodule":        {submodule, filepath.Join(gitDir, "modules", "sub")},
		"not a repository": {plain, plain},
		"unreadable .git":  {broken, broken},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ResolveRepoIdentity(tc.repoRoot); got != tc.want {
				t.Fatalf("want %s, got %s", tc.want, got)
			}
		})
	}
	if NewStore(root, main).RepoID != NewStore(root, linked).RepoID {
		t.Fatal("a worktree must use the cache directory of its repository")
	}
	if NewStore(root, main).CheckoutID == NewStore(root, linked).CheckoutID {
		t.Fatal("each checkout needs its own id for what is not shared")
	}
}

func TestDepVariantsAreKeptNewestFirstAndBounded(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	const baseKey = "base"
	save := func(dep string) {
		t.Helper()
		if err := store.SaveDeps(DepRecord{BaseKey: baseKey, TestFile: "a.test.ts", Deps: []string{dep}}); err != nil {
			t.Fatal(err)
		}
	}
	save("one")
	save("two")
	save("one")
	rec, err := store.LoadDeps(baseKey)
	if err != nil || rec == nil {
		t.Fatalf("load: %v %v", rec, err)
	}
	variants := rec.Variants()
	if len(variants) != 2 || variants[0].Deps[0] != "one" || variants[1].Deps[0] != "two" {
		t.Fatalf("want [one two] without duplicates, got %+v", variants)
	}
	for i := 0; i < maxOlderDepVariants+5; i++ {
		save(string(rune('a' + i)))
	}
	rec, _ = store.LoadDeps(baseKey)
	if got := len(rec.Variants()); got != maxOlderDepVariants+1 {
		t.Fatalf("want %d variants at most, got %d", maxOlderDepVariants+1, got)
	}
}
