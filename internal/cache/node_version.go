package cache

import (
	"os"
	"path/filepath"
)

const nodeVersionsFile = "node-versions.json"

// NodeVersionEntry caches `node --version` output for one binary (size, mtime) and, for
// version-manager shims, the pinned-version files that decide which Node the shim runs.
type NodeVersionEntry struct {
	Size      int64  `json:"size"`
	MtimeNano int64  `json:"mtimeNano"`
	Pins      string `json:"pins,omitempty"`
	Version   string `json:"version"`
}

// NodeBinary identifies the node executable found on PATH. Pins is empty for a real binary and
// a digest of the version pins in force (.nvmrc, .tool-versions, ...) for a shim.
type NodeBinary struct {
	ResolvedPath string
	Pins         string
}

// LookupNodeVersion returns the cached node version when the binary and its pins are unchanged.
// If missing or stale, resolveFn is called to query the version and persist it.
func LookupNodeVersion(baseDir string, bin NodeBinary, resolveFn func() (string, error)) (string, error) {
	info, err := os.Stat(bin.ResolvedPath)
	if err != nil {
		return resolveFn()
	}
	if ver, ok := LookupCachedNodeVersion(baseDir, bin); ok {
		return ver, nil
	}
	ver, err := resolveFn()
	if err != nil {
		return "", err
	}
	cachePath := filepath.Join(baseDir, nodeVersionsFile)
	entries := readNodeVersions(cachePath)
	entries[bin.ResolvedPath] = NodeVersionEntry{
		Size:      info.Size(),
		MtimeNano: info.ModTime().UnixNano(),
		Pins:      bin.Pins,
		Version:   ver,
	}
	_ = writeJSONAtomic(cachePath, entries)
	return ver, nil
}

// LookupCachedNodeVersion reads the cached node version without invoking node.
func LookupCachedNodeVersion(baseDir string, bin NodeBinary) (string, bool) {
	info, err := os.Stat(bin.ResolvedPath)
	if err != nil {
		return "", false
	}
	entries := readNodeVersions(filepath.Join(baseDir, nodeVersionsFile))
	e, ok := entries[bin.ResolvedPath]
	if !ok || e.Size != info.Size() || e.MtimeNano != info.ModTime().UnixNano() || e.Pins != bin.Pins || e.Version == "" {
		return "", false
	}
	return e.Version, true
}

// ForgetNodeVersion drops the cached version of a binary that turned out to be wrong.
func ForgetNodeVersion(baseDir string, bin NodeBinary) {
	cachePath := filepath.Join(baseDir, nodeVersionsFile)
	entries := readNodeVersions(cachePath)
	if _, ok := entries[bin.ResolvedPath]; !ok {
		return
	}
	delete(entries, bin.ResolvedPath)
	_ = writeJSONAtomic(cachePath, entries)
}

func readNodeVersions(cachePath string) map[string]NodeVersionEntry {
	var loaded map[string]NodeVersionEntry
	found, err := readJSON(cachePath, &loaded)
	if err != nil || (found && loaded == nil) {
		_ = os.Remove(cachePath)
		return make(map[string]NodeVersionEntry)
	}
	if loaded == nil {
		return make(map[string]NodeVersionEntry)
	}
	return loaded
}
