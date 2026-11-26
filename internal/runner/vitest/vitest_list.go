package vitest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/chsotelo/testcache/internal/cache"
)

func computeDiscoveryKey(env *runEnv, parsed ParsedArgs, candidates []string) string {
	h := sha256.New()
	writeHashField(h, "fmt", cache.FormatVersion)
	writeHashField(h, "projRoot", relSlashPath(env.proj.RepoRoot, env.proj.ProjectRoot))
	writeHashField(h, "vitestVer", env.proj.VitestVersion)
	writeHashField(h, "nodeVer", env.nodeVer)
	for _, pf := range env.proj.ProjectFiles {
		writeHashField(h, "projFile", pf.Path+"\x00"+pf.Hash)
	}
	for _, f := range parsed.DiscoveryFlags {
		writeHashField(h, "flag", f)
	}
	for _, ep := range env.envPairs {
		if ep.Set {
			writeHashField(h, "env1", ep.Key+"="+ep.Value)
		} else {
			writeHashField(h, "env0", ep.Key)
		}
	}
	for _, c := range candidates {
		writeHashField(h, "cand", relSlashPath(env.proj.ProjectRoot, c))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func queryVitestFiles(ctx context.Context, env *runEnv, parsed ParsedArgs) ([]string, error) {
	args := []string{"list", "--filesOnly", "--json"}
	if env.proj.BaseConfigFile != "" && parsed.ConfigPath != "" {
		args = append(args, "--config", env.proj.BaseConfigFile)
	}
	args = append(args, parsed.DiscoveryFlags...)

	cmd := exec.Command(env.proj.VitestBin, args...)
	cmd.Dir = env.proj.ProjectRoot
	// Discovery is tc's own Vitest call: no integration may route it back into tc.
	cmd.Env = buildChildEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runInProcessGroup(ctx, cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("vitest list failed: %s", firstLine(msg))
		}
		return nil, fmt.Errorf("vitest list failed: %w", err)
	}
	return parseVitestListJSON(stdout.Bytes())
}

func parseVitestListJSON(data []byte) ([]string, error) {
	type listedFile struct {
		File string `json:"file"`
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var items []listedFile
	if err := json.Unmarshal(trimmed, &items); err != nil {
		idx := bytes.IndexByte(trimmed, '[')
		if idx < 0 || json.Unmarshal(trimmed[idx:], &items) != nil {
			return nil, fmt.Errorf("parse vitest list output: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.File == "" {
			continue
		}
		rp := resolveRealPath(it.File)
		if _, ok := seen[rp]; ok {
			continue
		}
		seen[rp] = struct{}{}
		out = append(out, rp)
	}
	sort.Strings(out)
	return out, nil
}
