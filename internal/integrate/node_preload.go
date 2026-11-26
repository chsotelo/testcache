package integrate

import (
	_ "embed"
	"os"
	"path/filepath"
)

const (
	// NodePreloadFile is the file name scripts/install.sh gives the preload.
	NodePreloadFile = "node-preload.cjs"
	// NodePreloadErrorLog collects, under the cache root, every run the preload could not
	// hand to tc.
	NodePreloadErrorLog = "preload-errors.log"
	// EnvNodeOptions is where the shell names the preload for every Node process.
	EnvNodeOptions = "NODE_OPTIONS"
	// EnvNodePreloadPath is exported next to NODE_OPTIONS by the installer.
	EnvNodePreloadPath = "TC_NODE_PRELOAD"
	nodePreloadLibDir  = "lib"
	nodePreloadPkgDir  = "testcache"
)

//go:embed assets/node-preload.cjs
var nodePreloadSource []byte

// NodePreloadSource returns the preload this tc was built with.
func NodePreloadSource() []byte {
	return append([]byte(nil), nodePreloadSource...)
}

// NodePreloadPath is where the installer puts the preload for a tc binary installed at
// <prefix>/bin/tc: <prefix>/lib/testcache/node-preload.cjs.
func NodePreloadPath(tcBinary string) string {
	prefix := filepath.Dir(filepath.Dir(tcBinary))
	return filepath.Join(prefix, nodePreloadLibDir, nodePreloadPkgDir, NodePreloadFile)
}

// ReadInstalledNodePreload returns the preload installed for tcBinary, or an error when it
// is missing or unreadable.
func ReadInstalledNodePreload(tcBinary string) ([]byte, error) {
	return os.ReadFile(NodePreloadPath(tcBinary))
}
