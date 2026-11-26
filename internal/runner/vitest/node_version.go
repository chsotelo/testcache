package vitest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/runlog"
)

const nodeBinaryName = "node"

type nodeVersionRequest struct {
	baseCacheDir string
	projectRoot  string
	allowNode    bool
}

type nodeVersionInfo struct {
	version  string
	uncached bool
	binary   cache.NodeBinary
}

// resolveNodeVersion returns the version of the node that would run Vitest in projectRoot.
// A real binary is identified by path, size and mtime. A version-manager shim (volta, asdf,
// mise) is one file standing for many versions, so the pins that select the version join the
// identity and `node --version` is asked again whenever they change.
func resolveNodeVersion(req nodeVersionRequest) (nodeVersionInfo, error) {
	nodeBin, err := exec.LookPath(nodeBinaryName)
	if err != nil {
		if !req.allowNode {
			return nodeVersionInfo{uncached: true}, nil
		}
		return nodeVersionInfo{}, fmt.Errorf("locate node binary: %w", err)
	}
	bin := cache.NodeBinary{ResolvedPath: resolveRealPath(nodeBin)}
	if isNodeShim(bin.ResolvedPath) {
		bin.Pins = hashNodeVersionPins(req.projectRoot)
	}
	if !req.allowNode {
		ver, ok := cache.LookupCachedNodeVersion(req.baseCacheDir, bin)
		return nodeVersionInfo{version: ver, uncached: !ok, binary: bin}, nil
	}
	ver, err := cache.LookupNodeVersion(req.baseCacheDir, bin, func() (string, error) {
		// Run the PATH entry, not its resolved target, from the project: shims pick by both.
		cmd := exec.Command(nodeBin, "--version")
		cmd.Dir = req.projectRoot
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("run node --version: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	})
	return nodeVersionInfo{version: ver, binary: bin}, err
}

// isNodeShim reports whether the executable behind `node` is a launcher rather than Node
// itself: a script (asdf) or a binary with another name (volta-shim, mise).
func isNodeShim(resolvedPath string) bool {
	if filepath.Base(resolvedPath) != nodeBinaryName {
		return true
	}
	f, err := os.Open(resolvedPath)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 2)
	n, _ := f.Read(head)
	return string(head[:n]) == "#!"
}

// hashNodeVersionPins digests everything a shim consults to pick a Node version for
// projectRoot: pin files in it and its ancestors, and the env overrides of asdf and mise.
func hashNodeVersionPins(projectRoot string) string {
	h := sha256.New()
	pinFiles := []string{".nvmrc", ".node-version", ".tool-versions", "mise.toml", ".mise.toml", string(manifestPkgJSON)}
	for _, dir := range ancestorDirs(projectRoot, string(filepath.Separator)) {
		for _, name := range pinFiles {
			p := filepath.Join(dir, name)
			if data, err := os.ReadFile(p); err == nil {
				writeHashField(h, "pin", p)
				writeHashField(h, "content", string(data))
			}
		}
	}
	for _, key := range []string{"ASDF_NODEJS_VERSION", "MISE_NODE_VERSION", "VOLTA_HOME"} {
		writeHashField(h, key, os.Getenv(key))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// rejectNodeMismatch compares the version in the cache keys with the one Vitest reported it
// ran on. A mismatch means the cached version is stale: forget it and cache nothing.
func (e *runEnv) rejectNodeMismatch(reported string) string {
	if reported == "" || reported == e.nodeVer {
		return ""
	}
	cache.ForgetNodeVersion(e.baseCacheDir, e.node.binary)
	reason := fmt.Sprintf("node version mismatch: cache keys assume %s, vitest ran on %s", e.nodeVer, reported)
	e.trace.AddAnomaly(runlog.AnomalyNodeVersionMismatch, reason)
	return reason
}
