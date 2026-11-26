package vitest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/cache"
)

func TestParseTSConfigExtends(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"string", `{ "extends": "./base.json" }`, []string{"./base.json"}},
		{"array with comments", "{\n  // base first\n  \"extends\": [\"./a.json\", \"@tsconfig/node20/tsconfig.json\",],\n}", []string{"./a.json", "@tsconfig/node20/tsconfig.json"}},
		{"absent", `{ "compilerOptions": {} }`, nil},
	}
	for _, tc := range cases {
		if got := parseTSConfigExtends([]byte(tc.src)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCollectTSConfigExtendsFollowsLocalChain(t *testing.T) {
	root := resolveRealPath(t.TempDir())
	files := map[string]string{
		"tsconfig.json":       `{ "extends": "./cfg/one" }`,
		"cfg/one.json":        `{ "extends": ["../two.json", "some-package/tsconfig.json"] }`,
		"two.json":            `{ "extends": "./tsconfig.json" }`,
		"unrelated/base.json": `{}`,
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found := make(map[string]struct{})
	collectTSConfigExtends(found, filepath.Join(root, "tsconfig.json"))

	want := map[string]struct{}{
		filepath.Join(root, "cfg", "one.json"): {},
		filepath.Join(root, "two.json"):        {},
	}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("extends chain = %v, want %v", found, want)
	}
}

func TestIsProjectConfigName(t *testing.T) {
	tracked := []string{
		"package.json", "pnpm-lock.yaml", "vitest.config.ts", "vite.config.mjs", "vitest.workspace.json",
		"tsconfig.json", "tsconfig.app.json", "jsconfig.json", "postcss.config.cjs", ".postcssrc.json",
		"tailwind.config.ts", "babel.config.js", ".babelrc", ".browserslistrc",
	}
	for _, name := range tracked {
		if !isProjectConfigName(name) {
			t.Errorf("%s should be a tracked project config file", name)
		}
	}
	ignored := []string{"vite.config.ts.timestamp-1700000000-abc.mjs", "vitest.config.", "README.md", "tsconfig.tsbuildinfo", "eslint.config.js"}
	for _, name := range ignored {
		if isProjectConfigName(name) {
			t.Errorf("%s should not be a tracked project config file", name)
		}
	}
}

func TestIsNodeShim(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if !isNodeShim(write("volta-shim", "\x7fELF")) {
		t.Errorf("a launcher binary with another name is a shim")
	}
	scriptDir := filepath.Join(dir, "shims")
	if err := os.Mkdir(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scriptDir, "node")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nexec asdf exec node \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isNodeShim(script) {
		t.Errorf("a script named node is a shim")
	}
	if isNodeShim(write("node", "\xcf\xfa\xed\xfe binary")) {
		t.Errorf("a binary named node is not a shim")
	}
}

func TestHashNodeVersionPinsFollowsPinFiles(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "repo", "app")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	before := hashNodeVersionPins(projDir)
	if err := os.WriteFile(filepath.Join(filepath.Dir(projDir), ".tool-versions"), []byte("nodejs 22.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withPin := hashNodeVersionPins(projDir)
	if withPin == before {
		t.Fatalf("a .tool-versions in a parent directory must change the pin digest")
	}
	t.Setenv("ASDF_NODEJS_VERSION", "20.0.0")
	if hashNodeVersionPins(projDir) == withPin {
		t.Fatalf("an env override must change the pin digest")
	}
}

func TestCollectFactDeps(t *testing.T) {
	facts := indexModuleFacts([]byte(`{
		"/r/tests/a.test.ts": {"bareImports": ["/r/src/polyfill.ts"], "mockCandidates": ["/r/src/__mocks__/dep.ts"]},
		"/r/src/polyfill.ts": {"bareImports": ["/r/src/nested.ts"]},
		"/r/src/unloaded.ts": {"bareImports": ["/r/src/never.ts"]}
	}`))
	got := facts.collectDeps([]string{"/r/tests/a.test.ts", "/r/src/polyfill.ts", "/r/src/nested.ts"})
	want := []string{"/r/src/polyfill.ts", "/r/src/__mocks__/dep.ts", "/r/src/nested.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fact deps = %v, want %v", got, want)
	}
}

func TestRunWindowRejectsChangedAndFreshFiles(t *testing.T) {
	root := resolveRealPath(t.TempDir())
	env := &runEnv{proj: ProjectInfo{RepoRoot: root, ProjectRoot: root}}
	for _, name := range []string{"aged.ts", "fresh.ts"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("export {};\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ageFixturePath(t, filepath.Join(root, "aged.ts"))
	win := openRunWindow(env, nil)
	win.rememberHash(inputDep, "aged.ts", "sha256:before")

	if reason := win.rejectFile(env, inputDep, cache.DepHash{Path: "aged.ts", Hash: "sha256:before"}); reason != "" {
		t.Fatalf("an old, unchanged file must be accepted, got %q", reason)
	}
	// Same mtime, different content: only the before/after hashes can tell.
	if reason := win.rejectFile(env, inputDep, cache.DepHash{Path: "aged.ts", Hash: "sha256:after"}); !strings.Contains(reason, "changed during the run") {
		t.Fatalf("a file whose hash changed must be rejected, got %q", reason)
	}
	if reason := win.rejectFile(env, inputDep, cache.DepHash{Path: "fresh.ts", Hash: "sha256:any"}); !strings.Contains(reason, "modified during the run") {
		t.Fatalf("a file written just before the run must be rejected, got %q", reason)
	}
	if reason := win.rejectFile(env, inputDep, cache.DepHash{Path: "absent.ts", Hash: hashMissing}); reason != "" {
		t.Fatalf("a missing path has no mtime to distrust, got %q", reason)
	}
}
