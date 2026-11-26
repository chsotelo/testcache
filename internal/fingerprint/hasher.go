package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
)

const (
	// NormalizeVersion versions the normalization algorithm: comments, whitespace and, since
	// v2, type-only statements; since v3, documentation comments and comment-only JSX children.
	NormalizeVersion = "v3"
	normCachePrefix  = "norm-" + NormalizeVersion
	normHashPrefix   = normCachePrefix + ":"
	rawHashPrefix    = "sha256:"
	gitHashPrefix    = "git:"
)

// HasherOptions configures optional normalization and logging behavior on Hasher.
type HasherOptions struct {
	NormalizeHash bool
	VerboseLog    func(string, ...any)
}

// Hasher computes content hashes for files using git blob hashes when clean and
// a size/mtime-cached SHA-256 fallback for dirty or untracked files.
type Hasher struct {
	repoRoot   string
	git        *GitState
	files      *cache.FileHashCache
	normalize  bool
	verboseLog func(string, ...any)
}

// NewHasher constructs a Hasher for repoRoot.
func NewHasher(repoRoot string, git *GitState, fileCache *cache.FileHashCache) *Hasher {
	return &Hasher{
		repoRoot:  repoRoot,
		git:       git,
		files:     fileCache,
		normalize: true,
	}
}

// WithGitState returns a copy of h that reads clean blob hashes from git, leaving h untouched.
func (h *Hasher) WithGitState(git *GitState) *Hasher {
	return &Hasher{
		repoRoot:   h.repoRoot,
		git:        git,
		files:      h.files,
		normalize:  h.normalize,
		verboseLog: h.verboseLog,
	}
}

// Configure updates normalization and verbose logging options on Hasher.
func (h *Hasher) Configure(opts HasherOptions) {
	h.normalize = opts.NormalizeHash
	h.verboseLog = opts.VerboseLog
}

type pathHashTag string

const hashMissing pathHashTag = "missing"

// HashFile returns a tagged raw content hash ("git:<sha1>", "sha256:<hex>", or "dir:<hex>")
// for absPath. If the path does not exist, it returns ("missing", nil).
func (h *Hasher) HashFile(absPath string) (string, error) {
	if tag, done, err := resolveNonRegularHash(absPath); done || err != nil {
		return tag, err
	}
	return h.hashRawFile(absPath)
}

// HashDependency returns a comment/whitespace-normalized hash for JS/TS/JSX/CSS files when
// normalization is enabled, falling back to raw hash on unsupported extensions or lex errors.
func (h *Hasher) HashDependency(absPath string) (string, error) {
	if tag, done, err := resolveNonRegularHash(absPath); done || err != nil {
		return tag, err
	}
	if !h.normalize || !IsNormalizableExt(absPath) {
		return h.hashRawFile(absPath)
	}
	return h.hashNormalizedFile(absPath)
}

func resolveNonRegularHash(absPath string) (string, bool, error) {
	info, err := os.Lstat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(hashMissing), true, nil
		}
		return "", false, fmt.Errorf("lstat %s: %w", absPath, err)
	}
	if info.IsDir() {
		tag, err := hashDirectory(absPath)
		return tag, true, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		targetInfo, statErr := os.Stat(absPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return string(hashMissing), true, nil
			}
			return "", false, fmt.Errorf("stat %s: %w", absPath, statErr)
		}
		if targetInfo.IsDir() {
			tag, err := hashDirectory(absPath)
			return tag, true, err
		}
	}
	return "", false, nil
}

// hashRawFile returns the SHA-256 of a file's bytes, always: one content, one hash. A file
// used to hash as "git:<blob id>" while tracked and unmodified and as "sha256:..." otherwise,
// so committing a config or data file, or having it clean in one checkout and edited to the
// same bytes in another, changed every key that depended on it.
// The git index still saves the work: for an unmodified tracked file the digest is looked up
// by blob id in a table kept next to the stat cache, so the file is read once per content
// and never stat-ed again while it stays clean.
func (h *Hasher) hashRawFile(absPath string) (string, error) {
	if h.files == nil {
		data, err := os.ReadFile(absPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return string(hashMissing), nil
			}
			return "", fmt.Errorf("read %s: %w", absPath, err)
		}
		sum := sha256.Sum256(data)
		return rawHashPrefix + hex.EncodeToString(sum[:]), nil
	}
	blob, clean := h.cleanGitBlob(absPath)
	if clean {
		if digest, ok := h.files.LookupBlobDigest(blob); ok {
			return rawHashPrefix + digest, nil
		}
	}
	digest, err := h.files.HashFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(hashMissing), nil
		}
		return "", err
	}
	if clean {
		h.rememberBlobDigest(absPath, blob, digest)
	}
	return rawHashPrefix + digest, nil
}

// rememberBlobDigest records that the blob has this digest, unless the file may have been
// written after git reported it unmodified: a wrong pair would outlive this run.
func (h *Hasher) rememberBlobDigest(absPath, blob, digest string) {
	info, err := os.Stat(absPath)
	if err != nil || !h.git.SnapshotCovers(info.ModTime()) {
		return
	}
	h.files.StoreBlobDigest(blob, digest)
}

func (h *Hasher) hashNormalizedFile(absPath string) (string, error) {
	if h.files != nil {
		digest, err := h.files.HashFileTransformed(absPath, normCachePrefix, func(data []byte) (string, error) {
			return h.computeNormalizedDigest(absPath, data), nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return string(hashMissing), nil
			}
			return "", err
		}
		return digest, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(hashMissing), nil
		}
		return "", fmt.Errorf("read %s: %w", absPath, err)
	}
	return h.computeNormalizedDigest(absPath, data), nil
}

func (h *Hasher) computeNormalizedDigest(absPath string, data []byte) string {
	res := NormalizeSource(absPath, data)
	if res.OK {
		sum := sha256.Sum256(res.Data)
		return normHashPrefix + hex.EncodeToString(sum[:])
	}
	if h.verboseLog != nil {
		h.verboseLog("normalize fallback to raw hash for %s: %s", h.displayRelPath(absPath), res.Reason)
	}
	sum := sha256.Sum256(data)
	return rawHashPrefix + hex.EncodeToString(sum[:])
}

func (h *Hasher) displayRelPath(absPath string) string {
	if h.repoRoot == "" {
		return absPath
	}
	if rel, err := filepath.Rel(h.repoRoot, absPath); err == nil {
		return filepath.ToSlash(rel)
	}
	return absPath
}

func (h *Hasher) cleanGitBlob(absPath string) (string, bool) {
	if h.git == nil {
		return "", false
	}
	rel, err := filepath.Rel(h.repoRoot, absPath)
	if err != nil {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)
	if strings.HasPrefix(relSlash, "..") {
		return "", false
	}
	return h.git.CleanBlobHash(relSlash)
}

func hashDirectory(absPath string) (string, error) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(hashMissing), nil
		}
		return "", fmt.Errorf("read dir %s: %w", absPath, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		writeField(h, "entry", name)
	}
	return "dir:" + hex.EncodeToString(h.Sum(nil)), nil
}

// EnvPair captures an allowlisted environment variable state.
type EnvPair struct {
	Key   string
	Value string
	Set   bool
}

// BaseKeyInputs holds all static and environment inputs for a test file's base key.
type BaseKeyInputs struct {
	FormatVersion    string
	NormalizeVersion string
	Runner           string
	RunnerVersion    string
	NodeVersion      string
	ConfigHash       string
	ProjectFiles     []cache.DepHash
	Args             []string
	TestFile         string
	TestFileHash     string
	Env              []EnvPair
}

// ComputeBaseKey produces the level-1 SHA-256 key identifying the test file, configs,
// runner environment, flags, and allowlisted env vars.
func ComputeBaseKey(in BaseKeyInputs) string {
	h := sha256.New()
	writeField(h, "fmt", in.FormatVersion)
	if in.NormalizeVersion != "" {
		writeField(h, "normVer", in.NormalizeVersion)
	}
	writeField(h, "runner", in.Runner)
	writeField(h, "runnerVersion", in.RunnerVersion)
	writeField(h, "nodeVersion", in.NodeVersion)
	writeField(h, "tcConfig", in.ConfigHash)

	proj := append([]cache.DepHash(nil), in.ProjectFiles...)
	sort.Slice(proj, func(i, j int) bool { return proj[i].Path < proj[j].Path })
	for _, pf := range proj {
		writeField(h, "projFile", pf.Path+"\x00"+pf.Hash)
	}
	for _, arg := range in.Args {
		writeField(h, "arg", arg)
	}
	writeField(h, "testFile", in.TestFile)
	writeField(h, "testFileHash", in.TestFileHash)

	envs := append([]EnvPair(nil), in.Env...)
	sort.Slice(envs, func(i, j int) bool { return envs[i].Key < envs[j].Key })
	for _, ep := range envs {
		if ep.Set {
			writeField(h, "env1", ep.Key+"="+ep.Value)
		} else {
			writeField(h, "env0", ep.Key)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeFullKey combines baseKey with the sorted list of executed dependency hashes.
func ComputeFullKey(baseKey string, deps []cache.DepHash) string {
	return ComputeFullKeyWithEnv(baseKey, deps, nil)
}

// ComputeFullKeyWithEnv combines baseKey, dependency hashes, and runtime-observed env vars.
func ComputeFullKeyWithEnv(baseKey string, deps []cache.DepHash, envs []EnvPair) string {
	return ComputeFullKeyWithInputs(FullKeyInputs{
		BaseKey: baseKey,
		Deps:    deps,
		Envs:    envs,
	})
}

// LookupEnvPairs reads the current process environment values for keys, sorted by key.
func LookupEnvPairs(keys []string) []EnvPair {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	pairs := make([]EnvPair, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		v, ok := LookupEnvValue(k)
		pairs = append(pairs, EnvPair{Key: k, Value: v, Set: ok})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	return pairs
}

func writeField(h interface{ Write([]byte) (int, error) }, tag, val string) {
	fmt.Fprintf(h, "%s:%d:%s\n", tag, len(val), val)
}
