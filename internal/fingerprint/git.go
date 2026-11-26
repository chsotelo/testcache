package fingerprint

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const snapshotMargin = 2 * time.Second

// GitState holds tracked index blob hashes and dirty/untracked paths for a git repo.
type GitState struct {
	mu            sync.RWMutex
	RepoRoot      string
	TrackedBlobs  map[string]string
	DirtyExact    map[string]struct{}
	DirtyPrefixes []string
	// LoadedAt is taken before git is asked: a file changed later can still look clean here.
	LoadedAt time.Time
}

// FindRepoRoot walks up from startDir looking for a .git entry.
// If none is found, it returns the resolved startDir.
func FindRepoRoot(startDir string) string {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return startDir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	cur := abs
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		cur = parent
	}
}

// LoadGitState queries git ls-files and git status in repoRoot.
// If git is unavailable or repoRoot is not a git worktree, it returns nil.
func LoadGitState(repoRoot string) *GitState {
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		return nil
	}
	loadedAt := time.Now()
	var (
		lsOut     []byte
		statusOut []byte
		lsErr     error
		statusErr error
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		lsOut, lsErr = exec.Command("git", "-C", repoRoot, "ls-files", "-s", "-z").Output()
	}()
	go func() {
		defer wg.Done()
		statusOut, statusErr = exec.Command("git", "-C", repoRoot, "status", "--porcelain", "-z").Output()
	}()
	wg.Wait()

	if lsErr != nil || statusErr != nil {
		return nil
	}
	exact, prefixes := ParseStatusPorcelainZ(statusOut)
	return &GitState{
		LoadedAt:      loadedAt,
		RepoRoot:      repoRoot,
		TrackedBlobs:  ParseLsFilesZ(lsOut),
		DirtyExact:    exact,
		DirtyPrefixes: prefixes,
	}
}

// SnapshotCovers reports whether a file last modified at mtime was certainly seen by the git
// commands this state was loaded from: it must be older than the snapshot by more than the
// resolution of file timestamps.
func (g *GitState) SnapshotCovers(mtime time.Time) bool {
	return g != nil && !g.LoadedAt.IsZero() && mtime.Before(g.LoadedAt.Add(-snapshotMargin))
}

// CleanBlobHash returns the git index blob hash if relSlashPath is tracked and clean.
func (g *GitState) CleanBlobHash(relSlashPath string) (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, dirty := g.DirtyExact[relSlashPath]; dirty {
		return "", false
	}
	for _, prefix := range g.DirtyPrefixes {
		if strings.HasPrefix(relSlashPath, prefix) {
			return "", false
		}
	}
	blob, ok := g.TrackedBlobs[relSlashPath]
	return blob, ok
}

// ParseLsFilesZ parses `git ls-files -s -z` output into a map of slash path -> blob SHA.
func ParseLsFilesZ(data []byte) map[string]string {
	blobs := make(map[string]string)
	for _, rec := range bytes.Split(data, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab <= 0 || tab+1 >= len(rec) {
			continue
		}
		meta := strings.Fields(string(rec[:tab]))
		if len(meta) < 2 {
			continue
		}
		relPath := filepath.ToSlash(string(rec[tab+1:]))
		blobs[relPath] = meta[1]
	}
	return blobs
}

// ParseStatusPorcelainZ parses `git status --porcelain -z` output into dirty exact paths
// and dirty directory prefixes.
func ParseStatusPorcelainZ(data []byte) (map[string]struct{}, []string) {
	exact := make(map[string]struct{})
	var prefixes []string
	records := bytes.Split(data, []byte{0})
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 {
			continue
		}
		xy := string(rec[:2])
		p := filepath.ToSlash(string(rec[3:]))
		addDirtyPath(p, exact, &prefixes)
		// Renames and copies in -z mode emit an extra NUL-terminated original path.
		if (xy[0] == 'R' || xy[0] == 'C') && i+1 < len(records) {
			i++
			orig := filepath.ToSlash(string(records[i]))
			addDirtyPath(orig, exact, &prefixes)
		}
	}
	return exact, prefixes
}

func addDirtyPath(p string, exact map[string]struct{}, prefixes *[]string) {
	if p == "" {
		return
	}
	if strings.HasSuffix(p, "/") {
		*prefixes = append(*prefixes, p)
		return
	}
	exact[p] = struct{}{}
}
