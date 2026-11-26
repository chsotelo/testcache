package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const (
	gitEntryName     = ".git"
	gitdirPrefix     = "gitdir:"
	gitCommonDirFile = "commondir"
	checkoutIDLength = 8
	// EnvRepoID names the repository explicitly, for checkouts whose path changes from one run
	// to the next, such as a pool of CI runners that each clone into their own directory.
	EnvRepoID       = "TESTCACHE_REPO_ID"
	envRepoIDPrefix = "env:"
)

// ResolveRepoIdentity returns what identifies a repository whichever checkout tc runs in: the
// git directory shared by the main checkout and every `git worktree` of it. Cache keys are
// built from repo-relative paths and content hashes, so checkouts of one repository can share
// results; only the place they are stored has to be the same. Outside git, or when the git
// files cannot be read, the checkout path itself is the identity, as before.
func ResolveRepoIdentity(repoRoot string) string {
	if id := strings.TrimSpace(os.Getenv(EnvRepoID)); id != "" {
		return envRepoIDPrefix + id
	}
	dotGit := filepath.Join(repoRoot, gitEntryName)
	info, err := os.Lstat(dotGit)
	if err != nil {
		return repoRoot
	}
	if info.IsDir() {
		return resolveDir(dotGit, repoRoot)
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return repoRoot
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), gitdirPrefix)
	if !ok {
		return repoRoot
	}
	gitdir = absFrom(repoRoot, strings.TrimSpace(gitdir))
	common, err := os.ReadFile(filepath.Join(gitdir, gitCommonDirFile))
	if err != nil {
		// A submodule: its git directory is its own repository.
		return resolveDir(gitdir, repoRoot)
	}
	return resolveDir(absFrom(gitdir, strings.TrimSpace(string(common))), repoRoot)
}

func absFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

func resolveDir(dir, fallback string) string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fallback
	}
	return real
}

// ComputeCheckoutID identifies one checkout (the main one or a worktree) inside a repository,
// for the few things that must not be shared, such as the limit on concurrent Vitest runs.
func ComputeCheckoutID(absRepoRoot string) string {
	sum := sha256.Sum256([]byte(absRepoRoot))
	return hex.EncodeToString(sum[:])[:checkoutIDLength]
}
