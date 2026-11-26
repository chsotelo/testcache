package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.integration.test.ts", "tests/api.integration.test.ts", true},
		{"**/*.integration.test.ts", "api.integration.test.ts", true},
		{"**/*.integration.test.ts", "tests/unit/math.test.ts", false},
		{"tests/e2e/**", "tests/e2e/login/flow.test.ts", true},
		{"tests/e2e/**", "tests/unit/login.test.ts", false},
		{"*.test.ts", "a.test.ts", true},
		{"*.test.ts", "dir/a.test.ts", false},
		{"src/?/a.ts", "src/x/a.ts", true},
		{"src/?/a.ts", "src/xy/a.ts", false},
	}
	for _, tc := range cases {
		if got := MatchGlob(tc.pattern, tc.path); got != tc.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestLoadAndMerge(t *testing.T) {
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, "usercfg")
	repoDir := filepath.Join(tmp, "repo")
	projDir := filepath.Join(repoDir, "apps", "web")
	for _, d := range []string{userDir, repoDir, projDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	t.Setenv("TESTCACHE_CONFIG_DIR", userDir)

	if err := os.WriteFile(filepath.Join(userDir, "config.json"), []byte(`{
		"maxCacheBytes": 536870912,
		"maxAgeDays": 14
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "testcache.json"), []byte(`{
		"exclude": ["**/*.e2e.ts"],
		"env": ["GLOBAL_FLAG"],
		"maxConcurrentRuns": 2,
		"maxWorkers": 6
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "testcache.json"), []byte(`{
		"exclude": ["**/*.integration.test.ts"],
		"env": ["API_URL"],
		"maxConcurrentRuns": 1
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "testcache.json"), []byte(`{
		"env": ["WEB_MODE"],
		"maxWorkers": 3
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, hash, err := Load(repoDir, projDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if hash == "" {
		t.Fatalf("expected non-empty hash")
	}
	if cfg.MaxConcurrentRuns != 1 || cfg.MaxWorkers != 3 {
		t.Fatalf("unexpected limits: %+v", cfg)
	}
	if len(cfg.Exclude) != 2 || len(cfg.Env) != 3 {
		t.Fatalf("unexpected merged slices: exclude=%v env=%v", cfg.Exclude, cfg.Env)
	}
	if cfg.MaxCacheBytes != 536870912 || cfg.MaxAgeDays != 14 {
		t.Fatalf("unexpected GC bounds: %+v", cfg)
	}

	// Changing GC bounds must not invalidate cache keys.
	if err := os.WriteFile(filepath.Join(repoDir, "testcache.json"), []byte(`{
		"exclude": ["**/*.integration.test.ts"],
		"env": ["API_URL"],
		"maxConcurrentRuns": 1,
		"maxCacheBytes": 1024,
		"maxAgeDays": 7
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, hash2, err := Load(repoDir, projDir)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if cfg2.MaxCacheBytes != 1024 || cfg2.MaxAgeDays != 7 {
		t.Fatalf("expected GC override in repo testcache.json, got %+v", cfg2)
	}
	if hash2 != hash {
		t.Fatalf("expected GC bounds not to change config hash: %s != %s", hash2, hash)
	}
	if !cfg2.NormalizeHash {
		t.Fatalf("expected NormalizeHash to default to true")
	}
}

func TestNormalizeHashConfigMergeAndHash(t *testing.T) {
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, "usercfg")
	repoDir := filepath.Join(tmp, "repo")
	projDir := filepath.Join(repoDir, "apps", "web")
	for _, d := range []string{userDir, repoDir, projDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TESTCACHE_CONFIG_DIR", userDir)

	cfgDefault, hashDefault, err := Load(repoDir, projDir)
	if err != nil || !cfgDefault.NormalizeHash {
		t.Fatalf("expected default NormalizeHash=true, got %+v err=%v", cfgDefault, err)
	}

	if err := os.WriteFile(filepath.Join(userDir, "config.json"), []byte(`{"normalizeHash":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "testcache.json"), []byte(`{"env":["FOO"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgOff, hashOff, err := Load(repoDir, projDir)
	if err != nil || cfgOff.NormalizeHash {
		t.Fatalf("expected omitted normalizeHash in repo config to keep user false, got %+v err=%v", cfgOff, err)
	}
	if hashOff == hashDefault {
		t.Fatalf("expected normalizeHash=false to change config hash")
	}

	if err := os.WriteFile(filepath.Join(projDir, "testcache.json"), []byte(`{"normalizeHash":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgOn, hashOn, err := Load(repoDir, projDir)
	if err != nil || !cfgOn.NormalizeHash {
		t.Fatalf("expected project override normalizeHash=true, got %+v err=%v", cfgOn, err)
	}
	if hashOn == hashOff {
		t.Fatalf("expected normalizeHash=true override to change config hash")
	}
}

func TestRevalidateRateConfigMergeAndClamp(t *testing.T) {
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, "usercfg")
	repoDir := filepath.Join(tmp, "repo")
	for _, d := range []string{userDir, repoDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TESTCACHE_CONFIG_DIR", userDir)

	cfgDefault, hashDefault, err := Load(repoDir, repoDir)
	if err != nil || cfgDefault.RevalidateRate != DefaultRevalidateRate {
		t.Fatalf("expected default RevalidateRate=%v, got %+v err=%v", DefaultRevalidateRate, cfgDefault, err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "testcache.json"), []byte(`{"revalidateRate":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgZero, hashZero, err := Load(repoDir, repoDir)
	if err != nil || cfgZero.RevalidateRate != 0 {
		t.Fatalf("expected explicit 0 RevalidateRate, got %+v err=%v", cfgZero, err)
	}
	if hashZero != hashDefault {
		t.Fatalf("expected revalidateRate not to invalidate config hash")
	}

	if err := os.WriteFile(filepath.Join(repoDir, "testcache.json"), []byte(`{"revalidateRate":1.5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgClamped, _, err := Load(repoDir, repoDir)
	if err != nil || cfgClamped.RevalidateRate != 1 {
		t.Fatalf("expected clamped RevalidateRate=1, got %+v err=%v", cfgClamped, err)
	}
}
