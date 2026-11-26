package fingerprint

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	envOracleHelper = "TC_ORACLE_HELPER"
	envOracleCorpus = "TC_ORACLE_CORPUS"
)

type oracleHashRequest struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
}

type oracleHashResponse struct {
	Hash string `json:"hash"`
	OK   bool   `json:"ok"`
}

// TestOracleHelper serves normalized hashes over stdin/stdout NDJSON when TC_ORACLE_HELPER=1.
func TestOracleHelper(t *testing.T) {
	if os.Getenv(envOracleHelper) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req oracleHashRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatalf("decode oracle request: %v", err)
		}
		raw, err := base64.StdEncoding.DecodeString(req.ContentBase64)
		if err != nil {
			t.Fatalf("decode base64: %v", err)
		}
		res := NormalizeSource(req.Path, raw)
		sum := sha256.Sum256(res.Data)
		prefix := "raw:"
		if res.OK {
			prefix = "norm:"
		}
		if err := enc.Encode(oracleHashResponse{
			Hash: prefix + hex.EncodeToString(sum[:]),
			OK:   res.OK,
		}); err != nil {
			t.Fatalf("encode oracle response: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stdin: %v", err)
	}
}

func TestNormalizationOracle(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node binary not found; skipping oracle test")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	viteEntry := filepath.Join(repoRoot, "testdata", "fixture-vitest", "node_modules", "vite", "dist", "node", "index.js")
	if _, err := os.Stat(viteEntry); err != nil {
		t.Skipf("fixture node_modules/vite missing (%v); skipping oracle test", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	args := []string{
		filepath.Join(repoRoot, "scripts", "oracle.mjs"),
		"--hasher", exe,
	}
	if corpus := strings.TrimSpace(os.Getenv(envOracleCorpus)); corpus != "" {
		for _, p := range filepath.SplitList(corpus) {
			if p != "" {
				args = append(args, "--corpus", p)
			}
		}
	} else {
		args = append(args,
			"--corpus", filepath.Join(repoRoot, "testdata", "fixture-vitest"),
			"--corpus", filepath.Join(repoRoot, "testdata", "oracle-corpus"),
		)
	}

	cmd := exec.Command(nodeBin, args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Log(strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("oracle failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "violations=0") || strings.Contains(output, "equal_hash_pairs=0") {
		t.Fatalf("unexpected oracle summary:\n%s", output)
	}
}
