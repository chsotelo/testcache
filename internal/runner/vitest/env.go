package vitest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/fingerprint"
	"github.com/chsotelo/testcache/internal/runlog"
)

type runEnv struct {
	store        *cache.Store
	fileCache    *cache.FileHashCache
	hasher       *fingerprint.Hasher
	cfg          config.Config
	cfgHash      string
	proj         ProjectInfo
	baseCacheDir string
	node         nodeVersionInfo
	nodeVer      string
	nodeUncached bool
	cfgInputsKey string
	cfgInputs    *cache.ConfigInputs
	discovery    *discoveryMemo
	envPairs     []fingerprint.EnvPair
	quarantine   *quarantineIndex
	verbose      bool
	// trace is nil unless the caller keeps a run log; its methods accept a nil receiver.
	trace *runlog.Trace
}

// discoveryMemo remembers which discovery record this process used.
type discoveryMemo struct {
	key        string
	candidates []string
}

func (r *Runner) prepareEnv(workDir string, parsed ParsedArgs, allowNode bool, stderr io.Writer) (*runEnv, error) {
	projRootHint := resolveProjRootHint(workDir, parsed.RootDir)
	repoRoot := fingerprint.FindRepoRoot(projRootHint)

	baseCacheDir, err := config.CacheBaseDir()
	if err != nil {
		return nil, err
	}
	store := cache.NewStore(baseCacheDir, repoRoot)
	fileCache, err := cache.LoadFileHashCache(filepath.Join(store.RepoDir, "filehashes.json"))
	if err != nil {
		return nil, err
	}
	gitState := fingerprint.LoadGitState(repoRoot)
	hasher := fingerprint.NewHasher(repoRoot, gitState, fileCache)

	proj, err := InspectProject(workDir, parsed)
	if err != nil {
		return nil, err
	}
	cfg, cfgHash, err := config.Load(proj.RepoRoot, proj.ProjectRoot)
	if err != nil {
		return nil, err
	}
	verbose := isVerboseEnv()
	configureHasher(hasher, cfg.NormalizeHash, verbose, stderr)
	node, err := resolveNodeVersion(nodeVersionRequest{baseCacheDir: baseCacheDir, projectRoot: proj.ProjectRoot, allowNode: allowNode})
	if err != nil {
		return nil, err
	}
	env := &runEnv{
		store:        store,
		fileCache:    fileCache,
		hasher:       hasher,
		cfg:          cfg,
		cfgHash:      cfgHash,
		proj:         proj,
		baseCacheDir: baseCacheDir,
		node:         node,
		nodeVer:      node.version,
		nodeUncached: node.uncached,
		cfgInputsKey: cache.ConfigInputsKey(relSlashPath(proj.RepoRoot, proj.ProjectRoot), relSlashPath(proj.RepoRoot, proj.BaseConfigFile), parsed.Mode),
		quarantine:   loadQuarantineIndex(store),
		verbose:      verbose,
	}
	env.cfgInputs = store.LoadConfigInputs(env.cfgInputsKey)
	if err := env.loadBaseInputs(); err != nil {
		return nil, err
	}
	return env, nil
}

// loadBaseInputs hashes the project files and reads the env vars shared by every base key,
// including the ones the last run saw the Vite main process read (cfgInputs).
func (e *runEnv) loadBaseInputs() error {
	files, err := collectProjectFiles(projectFilesRequest{proj: e.proj, inputs: e.cfgInputs, hasher: e.hasher})
	if err != nil {
		return err
	}
	e.proj.ProjectFiles = files
	envKeys := e.cfg.Env
	if e.cfgInputs != nil {
		envKeys = append(append([]string(nil), envKeys...), e.cfgInputs.EnvKeys...)
	}
	e.envPairs = fingerprint.CollectAllowlistedEnv(envKeys)
	return nil
}

func configureHasher(hasher *fingerprint.Hasher, normalizeHash, verbose bool, stderr io.Writer) {
	var logFn func(string, ...any)
	if verbose && stderr != nil {
		logFn = func(format string, args ...any) {
			logVerbose(true, stderr, format, args...)
		}
	}
	hasher.Configure(fingerprint.HasherOptions{
		NormalizeHash: normalizeHash,
		VerboseLog:    logFn,
	})
}

func resolveProjRootHint(workDir, rootDir string) string {
	hint := workDir
	if rootDir != "" {
		if filepath.IsAbs(rootDir) {
			hint = rootDir
		} else {
			hint = filepath.Join(workDir, rootDir)
		}
	}
	return resolveRealPath(hint)
}

func isVerboseEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TC_VERBOSE")))
	return v == "1" || v == "true" || v == "yes"
}

func logVerbose(enabled bool, w io.Writer, format string, args ...any) {
	if !enabled || w == nil {
		return
	}
	fmt.Fprintf(w, "tc: [verbose] "+format+"\n", args...)
}
