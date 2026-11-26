package vitest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runner"
)

type toolingIdentifier string

const (
	pkgVitest       toolingIdentifier = "vitest"
	pkgVite         toolingIdentifier = "vite"
	dirNodeModules  toolingIdentifier = "node_modules"
	dirDist         toolingIdentifier = "dist"
	manifestPkgJSON toolingIdentifier = "package.json"
)

// ProjectInfo describes the resolved Vitest project environment.
type ProjectInfo struct {
	ProjectRoot       string
	RepoRoot          string
	VitestBin         string
	VitestVersion     string
	ViteEntry         string
	VitestConfigEntry string
	BaseConfigFile    string
	ProjectFiles      []cache.DepHash
}

type toolingResolution struct {
	vitestBin         string
	vitestVersion     string
	viteEntry         string
	vitestConfigEntry string
}

// InspectProject resolves project/repo paths and the Vitest binary/version. ProjectFiles is
// filled in afterwards by collectProjectFiles, once the hasher is configured.
func InspectProject(workDir string, parsed ParsedArgs) (ProjectInfo, error) {
	projRoot := workDir
	if parsed.RootDir != "" {
		if filepath.IsAbs(parsed.RootDir) {
			projRoot = parsed.RootDir
		} else {
			projRoot = filepath.Join(workDir, parsed.RootDir)
		}
	}
	projRoot = resolveRealPath(projRoot)
	repoRoot := fingerprint.FindRepoRoot(projRoot)

	tools, err := resolveVitestTooling(projRoot, repoRoot)
	if err != nil {
		return ProjectInfo{}, err
	}

	baseCfg := resolveBaseConfig(workDir, projRoot, parsed.ConfigPath)
	return ProjectInfo{
		ProjectRoot:       projRoot,
		RepoRoot:          repoRoot,
		VitestBin:         tools.vitestBin,
		VitestVersion:     tools.vitestVersion,
		ViteEntry:         tools.viteEntry,
		VitestConfigEntry: tools.vitestConfigEntry,
		BaseConfigFile:    baseCfg,
	}, nil
}

func resolveVitestTooling(projRoot, repoRoot string) (toolingResolution, error) {
	bin := findVitestBin(projRoot, repoRoot)
	if bin == "" {
		return toolingResolution{}, runner.MarkUsageError(fmt.Errorf("vitest binary not found in %s", projRoot))
	}

	vitestPkgDir := findPackageDir(projRoot, repoRoot, bin, string(pkgVitest))
	version := "unknown"
	var vitestCfgEntry string
	if vitestPkgDir != "" {
		if v := readPackageVersion(filepath.Join(vitestPkgDir, string(manifestPkgJSON))); v != "" {
			version = v
		}
		vitestCfgEntry = firstExistingFile(
			filepath.Join(vitestPkgDir, string(dirDist), "config.js"),
			filepath.Join(vitestPkgDir, string(dirDist), "config.mjs"),
		)
	}

	viteEntry := resolveViteEntry(projRoot, repoRoot, vitestPkgDir)
	return toolingResolution{
		vitestBin:         bin,
		vitestVersion:     version,
		viteEntry:         viteEntry,
		vitestConfigEntry: vitestCfgEntry,
	}, nil
}

func findVitestBin(projRoot, repoRoot string) string {
	for _, dir := range ancestorDirs(projRoot, repoRoot) {
		cand := filepath.Join(dir, string(dirNodeModules), ".bin", string(pkgVitest))
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath(string(pkgVitest)); err == nil {
		return p
	}
	return ""
}

func resolveViteEntry(projRoot, repoRoot, vitestPkgDir string) string {
	vitePkgDir := findPackageDir(projRoot, repoRoot, "", string(pkgVite))
	if vitePkgDir == "" && vitestPkgDir != "" {
		// In pnpm's non-hoisted layout, vite lives beside or inside vitest's real package dir.
		for _, cand := range []string{
			filepath.Join(filepath.Dir(vitestPkgDir), string(pkgVite)),
			filepath.Join(vitestPkgDir, string(dirNodeModules), string(pkgVite)),
		} {
			if fileExists(filepath.Join(cand, string(manifestPkgJSON))) {
				vitePkgDir = resolveRealPath(cand)
				break
			}
		}
	}
	if vitePkgDir == "" {
		return ""
	}
	return firstExistingFile(
		filepath.Join(vitePkgDir, string(dirDist), "node", "index.js"),
		filepath.Join(vitePkgDir, string(dirDist), "node", "index.mjs"),
	)
}

func firstExistingFile(candidates ...string) string {
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func findPackageDir(projRoot, repoRoot, binPath, pkgName string) string {
	for _, dir := range ancestorDirs(projRoot, repoRoot) {
		cand := filepath.Join(dir, string(dirNodeModules), pkgName)
		if fileExists(filepath.Join(cand, string(manifestPkgJSON))) {
			return resolveRealPath(cand)
		}
	}
	if binPath != "" && pkgName == string(pkgVitest) {
		if realBin, err := filepath.EvalSymlinks(binPath); err == nil {
			dir := filepath.Dir(realBin)
			for i := 0; i < 3; i++ {
				if fileExists(filepath.Join(dir, string(manifestPkgJSON))) {
					return dir
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
	}
	return ""
}

func ancestorDirs(start, stop string) []string {
	var dirs []string
	cur := start
	for {
		dirs = append(dirs, cur)
		if cur == stop {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return dirs
}

func readPackageVersion(pkgJSON string) string {
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		return ""
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return parsed.Version
}

func resolveBaseConfig(workDir, projRoot, cliConfig string) string {
	if cliConfig != "" {
		if filepath.IsAbs(cliConfig) {
			return resolveRealPath(cliConfig)
		}
		return resolveRealPath(filepath.Join(workDir, cliConfig))
	}
	candidates := []string{
		"vitest.config.ts", "vitest.config.mts", "vitest.config.cts",
		"vitest.config.js", "vitest.config.mjs", "vitest.config.cjs",
		"vite.config.ts", "vite.config.mts", "vite.config.cts",
		"vite.config.js", "vite.config.mjs", "vite.config.cjs",
	}
	for _, name := range candidates {
		p := filepath.Join(projRoot, name)
		if fileExists(p) {
			return resolveRealPath(p)
		}
	}
	return ""
}
