package vitest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
)

const (
	dirPatches   = "patches"
	envFileName  = ".env"
	extJSONFile  = ".json"
	tsconfigStem = "tsconfig"
)

type projectFilesRequest struct {
	proj   ProjectInfo
	inputs *cache.ConfigInputs
	hasher *fingerprint.Hasher
}

// collectProjectFiles hashes every file that shapes the whole run rather than one test: the
// config and what it imports, manifests and lockfiles, the node_modules install marker,
// tsconfig/postcss/tailwind/babel/browserslist files, patches, .env files and globalSetup files.
func collectProjectFiles(req projectFilesRequest) ([]cache.DepHash, error) {
	found := make(map[string]struct{})
	addExistingFile(found, req.proj.BaseConfigFile)
	for _, dir := range ancestorDirs(req.proj.ProjectRoot, req.proj.RepoRoot) {
		collectDirConfigFiles(found, dir)
	}
	collectEnvFiles(found, req.proj.ProjectRoot)
	if req.inputs != nil {
		if req.inputs.EnvDir != "" {
			collectEnvFiles(found, joinRepoPath(req.proj.RepoRoot, req.inputs.EnvDir))
		}
		for _, rel := range req.inputs.Files {
			// Kept even when deleted since: a vanished input hashes as "missing" and changes the key.
			found[joinRepoPath(req.proj.RepoRoot, rel)] = struct{}{}
		}
	}

	out := make([]cache.DepHash, 0, len(found))
	for p := range found {
		digest, err := hashProjectFile(req.hasher, p)
		if err != nil {
			return nil, fmt.Errorf("hash project file %s: %w", p, err)
		}
		out = append(out, cache.DepHash{Path: relSlashPath(req.proj.RepoRoot, p), Hash: digest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// installMarkerNames lists the files package managers rewrite inside node_modules on install.
func installMarkerNames() []string {
	return []string{".modules.yaml", ".package-lock.json", ".yarn-state.yml", ".yarn-integrity"}
}

// hashProjectFile hashes one project file. An install marker is hashed by what it says, not
// by its bytes: two installs of the same lockfile write the same packages with another
// timestamp and key order, and every checkout of a repository has its own node_modules.
func hashProjectFile(hasher *fingerprint.Hasher, path string) (string, error) {
	if filepath.Base(filepath.Dir(path)) == string(dirNodeModules) {
		for _, marker := range installMarkerNames() {
			if filepath.Base(path) != marker {
				continue
			}
			if digest, ok := fingerprint.HashInstallMarker(path); ok {
				return digest, nil
			}
		}
	}
	return hasher.HashFile(path)
}

func joinRepoPath(repoRoot, relSlash string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(relSlash))
}

func addExistingFile(found map[string]struct{}, p string) {
	if p != "" && fileExists(p) {
		found[resolveRealPath(p)] = struct{}{}
	}
}

func collectDirConfigFiles(found map[string]struct{}, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !isProjectConfigName(name) {
			continue
		}
		p := filepath.Join(dir, name)
		addExistingFile(found, p)
		if isTSConfigName(name) {
			collectTSConfigExtends(found, p)
		}
	}
	// Package managers rewrite these whenever node_modules changes, lockfile edit or not.
	for _, marker := range installMarkerNames() {
		addExistingFile(found, filepath.Join(dir, string(dirNodeModules), marker))
	}
	collectTreeFiles(found, filepath.Join(dir, dirPatches))
}

func isProjectConfigName(name string) bool {
	switch name {
	case string(manifestPkgJSON), "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock",
		"bun.lockb", "bun.lock", ".browserslistrc", "jsconfig.json":
		return true
	}
	for _, rcPrefix := range []string{".babelrc", ".postcssrc"} {
		if strings.HasPrefix(name, rcPrefix) {
			return true
		}
	}
	for _, stem := range []string{"vitest.config.", "vite.config.", "vitest.workspace.", "postcss.config.", "tailwind.config.", "babel.config."} {
		// One extension only: Vite's transient "<config>.timestamp-*.mjs" copies are not inputs.
		if ext, ok := strings.CutPrefix(name, stem); ok && ext != "" && !strings.Contains(ext, ".") {
			return true
		}
	}
	return isTSConfigName(name)
}

func isTSConfigName(name string) bool {
	return strings.HasPrefix(name, tsconfigStem) && strings.HasSuffix(name, extJSONFile)
}

// collectTSConfigExtends follows the "extends" chain of a tsconfig through local files.
// Configs extended from a package are covered by the lockfile and the install marker.
func collectTSConfigExtends(found map[string]struct{}, tsconfigPath string) {
	visited := map[string]struct{}{tsconfigPath: {}}
	queue := []string{tsconfigPath}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		data, err := os.ReadFile(cur)
		if err != nil {
			continue
		}
		for _, spec := range parseTSConfigExtends(data) {
			target := resolveTSConfigExtends(filepath.Dir(cur), spec)
			if _, seen := visited[target]; seen || target == "" {
				continue
			}
			visited[target] = struct{}{}
			found[target] = struct{}{}
			queue = append(queue, target)
		}
	}
}

// parseTSConfigExtends reads the "extends" value (a string or an array of strings) without a
// full parser, because tsconfig files allow comments and trailing commas.
func parseTSConfigExtends(data []byte) []string {
	field := regexp.MustCompile(`"extends"\s*:\s*(\[[^\]]*\]|"[^"]*")`).FindSubmatch(data)
	if field == nil {
		return nil
	}
	var specs []string
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(field[1], -1) {
		specs = append(specs, string(m[1]))
	}
	return specs
}

func resolveTSConfigExtends(dir, spec string) string {
	if !strings.HasPrefix(spec, ".") && !filepath.IsAbs(spec) {
		return ""
	}
	base := spec
	if !filepath.IsAbs(base) {
		base = filepath.Join(dir, spec)
	}
	found := firstExistingFile(base, base+extJSONFile, filepath.Join(base, tsconfigStem+extJSONFile))
	if found == "" {
		return ""
	}
	return resolveRealPath(found)
}

// collectEnvFiles adds the files Vite loads from an envDir: .env, .env.local, .env.<mode>...
func collectEnvFiles(found map[string]struct{}, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == envFileName || strings.HasPrefix(name, envFileName+".") {
			addExistingFile(found, filepath.Join(dir, name))
		}
	}
}

func collectTreeFiles(found map[string]struct{}, root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipAll
		}
		if !d.IsDir() {
			addExistingFile(found, p)
		}
		return nil
	})
}
