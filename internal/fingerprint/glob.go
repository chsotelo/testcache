package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
)

type (
	globPrefix   string
	skipDirName  string
	keySeparator string
)

const (
	globHashPrefix      globPrefix   = "glob:"
	globQueryHashPrefix globPrefix   = "globq:"
	dirNodeModules      skipDirName  = "node_modules"
	dirGit              skipDirName  = ".git"
	fieldSep            keySeparator = "\x00"
)

// FullKeyInputs holds all level-2 inputs for computing a test file's full cache key.
type FullKeyInputs struct {
	BaseKey string
	Deps    []cache.DepHash
	Globs   []cache.GlobHash
	Envs    []EnvPair
}

// ComputeFullKeyWithInputs combines baseKey, dependency hashes, glob hashes, and runtime env vars.
func ComputeFullKeyWithInputs(in FullKeyInputs) string {
	h := sha256.New()
	writeField(h, "base", in.BaseKey)
	writeSortedDeps(h, in.Deps)
	writeSortedGlobs(h, in.Globs)
	writeSortedEnvs(h, in.Envs)
	return hex.EncodeToString(h.Sum(nil))
}

func writeSortedDeps(h interface{ Write([]byte) (int, error) }, deps []cache.DepHash) {
	sorted := append([]cache.DepHash(nil), deps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, d := range sorted {
		writeField(h, "dep", d.Path+string(fieldSep)+d.Hash)
	}
}

func writeSortedGlobs(h interface{ Write([]byte) (int, error) }, globs []cache.GlobHash) {
	sorted := append([]cache.GlobHash(nil), globs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BaseDir != sorted[j].BaseDir {
			return sorted[i].BaseDir < sorted[j].BaseDir
		}
		if sorted[i].Glob != sorted[j].Glob {
			return sorted[i].Glob < sorted[j].Glob
		}
		return sorted[i].Importer < sorted[j].Importer
	})
	for _, g := range sorted {
		desc := fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%s\x00%s",
			g.Importer, g.Glob, g.BaseDir, g.Exhaustive, g.HasQuery, g.Query, g.Hash)
		writeField(h, "glob", desc)
	}
}

func writeSortedEnvs(h interface{ Write([]byte) (int, error) }, envs []EnvPair) {
	sorted := append([]EnvPair(nil), envs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	for _, ep := range sorted {
		if ep.Set {
			writeField(h, "env1", ep.Key+"="+ep.Value)
		} else {
			writeField(h, "env0", ep.Key)
		}
	}
}

// HashGlob hashes the sorted file listing under a glob's static base directory
// (including file content hashes when HasQuery is true).
func HashGlob(repoRoot string, g cache.GlobDep, hasher *Hasher) (string, error) {
	absBase := filepath.Join(repoRoot, filepath.FromSlash(g.BaseDir))
	info, err := os.Stat(absBase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(hashMissing), nil
		}
		return "", fmt.Errorf("stat glob base %s: %w", absBase, err)
	}
	if !info.IsDir() {
		return hashSingleGlobTarget(absBase, g.HasQuery, hasher)
	}
	relFiles, err := listGlobBaseFiles(absBase, g.Exhaustive)
	if err != nil {
		return "", err
	}
	if !g.HasQuery {
		sum := sha256.Sum256([]byte(strings.Join(relFiles, "\n")))
		return string(globHashPrefix) + hex.EncodeToString(sum[:]), nil
	}
	return hashGlobFilesWithContent(absBase, relFiles, hasher)
}

func hashSingleGlobTarget(absPath string, hasQuery bool, hasher *Hasher) (string, error) {
	if !hasQuery {
		sum := sha256.Sum256([]byte(filepath.Base(absPath)))
		return string(globHashPrefix) + hex.EncodeToString(sum[:]), nil
	}
	digest, err := hasher.HashFile(absPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Base(absPath) + string(fieldSep) + digest))
	return string(globQueryHashPrefix) + hex.EncodeToString(sum[:]), nil
}

func listGlobBaseFiles(absBase string, exhaustive bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(absBase, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if p != absBase && !exhaustive && (d.Name() == string(dirNodeModules) || d.Name() == string(dirGit)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(absBase, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk glob base %s: %w", absBase, err)
	}
	sort.Strings(files)
	return files, nil
}

func hashGlobFilesWithContent(absBase string, relFiles []string, hasher *Hasher) (string, error) {
	h := sha256.New()
	for _, rel := range relFiles {
		absFile := filepath.Join(absBase, filepath.FromSlash(rel))
		digest, err := hasher.HashFile(absFile)
		if err != nil {
			return "", fmt.Errorf("hash glob file %s: %w", absFile, err)
		}
		writeField(h, "file", rel+string(fieldSep)+digest)
	}
	return string(globQueryHashPrefix) + hex.EncodeToString(h.Sum(nil)), nil
}
