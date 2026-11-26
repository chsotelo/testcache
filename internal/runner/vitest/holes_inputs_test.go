package vitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dependency rewritten while its test runs must not be cached under the new content.
func TestVitestEditDuringRunIsNotCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeFixtureFile(t, filepath.Join(projDir, "src", "midrun.ts"), "export const LIMIT = 5;\n")
	writeFixtureFile(t, filepath.Join(projDir, "tests", "midrun.test.ts"), `import { writeFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { LIMIT } from '../src/midrun.js';
it('sees the value it imported', () => {
  writeFileSync(new URL('../src/midrun.ts', import.meta.url), 'export const LIMIT = 6;\n');
  expect(LIMIT).toBe(5);
});
`)
	const target = "tests/midrun.test.ts"

	// The test passes with LIMIT = 5 and leaves LIMIT = 6 on disk.
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, target, "src/midrun.ts")
	// A replayed pass here would vouch for content the test never saw: it must run and fail.
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

const (
	fileVitestConfig = "vitest.config.ts"
	targetATest      = "tests/a.test.ts"
	envConfigAPI     = "TC_TEST_CONFIG_API"
)

func writeProjectFile(t *testing.T, projDir, rel, content string) {
	t.Helper()
	path := filepath.Join(projDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, path, content)
}

func assertRunsThenHits(t *testing.T, projDir, target string) {
	t.Helper()
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneHit})
}

// Vite reads .env.<mode> in its main process; the worker hooks never see the file.
func TestVitestDotEnvFileInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, ".env.test", "VITE_TC_FOO=one\n")
	writeProjectFile(t, projDir, "tests/dotenv.test.ts", `import { expect, it } from 'vitest';
it('reads .env.test', () => { expect(import.meta.env.VITE_TC_FOO).toBe('one'); });
`)
	const target = "tests/dotenv.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, ".env.test", "VITE_TC_FOO=two\n")
	assertExplainContains(t, projDir, target, ".env.test")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A new .env file must invalidate too, not only an edited one.
func TestVitestNewDotEnvFileInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "tests/dotenv.test.ts", `import { expect, it } from 'vitest';
it('has no .env value', () => { expect(import.meta.env.VITE_TC_FOO).toBeUndefined(); });
`)
	const target = "tests/dotenv.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, ".env.test.local", "VITE_TC_FOO=two\n")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func writeConfigDrivenFixture(t *testing.T, projDir string) {
	t.Helper()
	writeProjectFile(t, projDir, "cfg-helper.ts", "export const defineValue = 'A';\n")
	writeProjectFile(t, projDir, "gs-helper.ts", "export const provided = 'g1';\n")
	writeProjectFile(t, projDir, "global-setup.ts", `import { provided } from './gs-helper';
export default function setup(project: any) { project.provide('tcKey', provided); }
`)
	writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
import { defineValue } from './cfg-helper';
export default defineConfig({
  define: {
    __TC_DEF__: JSON.stringify(defineValue),
    __TC_API__: JSON.stringify(process.env.`+envConfigAPI+` ?? 'none'),
  },
  test: { environment: 'node', globalSetup: ['./global-setup.ts'] },
});
`)
	writeProjectFile(t, projDir, "tests/def.test.ts", `import { expect, it } from 'vitest';
declare const __TC_DEF__: string;
it('define from config helper', () => { expect(__TC_DEF__).toBe('A'); });
`)
	writeProjectFile(t, projDir, "tests/api.test.ts", `import { expect, it } from 'vitest';
declare const __TC_API__: string;
it('define from config-time env', () => { expect(__TC_API__).toBe('x'); });
`)
	writeProjectFile(t, projDir, "tests/gs.test.ts", `import { expect, inject, it } from 'vitest';
it('value provided by globalSetup', () => { expect((inject as any)('tcKey')).toBe('g1'); });
`)
}

// A local file imported by vitest.config.ts is bundled into the config in the main process.
func TestVitestConfigImportInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envConfigAPI, "x")
	writeConfigDrivenFixture(t, projDir)
	const target = "tests/def.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "cfg-helper.ts", "export const defineValue = 'B';\n")
	assertExplainContains(t, projDir, target, "cfg-helper.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// process.env read while the config loads never reaches the worker's env hook.
func TestVitestConfigEnvReadInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envConfigAPI, "x")
	writeConfigDrivenFixture(t, projDir)
	const target = "tests/api.test.ts"
	assertRunsThenHits(t, projDir, target)

	t.Setenv(envConfigAPI, "y")
	assertExplainContains(t, projDir, target, envConfigAPI)
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// globalSetup files, and what they import, run in the main process only.
func TestVitestGlobalSetupInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envConfigAPI, "x")
	writeConfigDrivenFixture(t, projDir)
	const target = "tests/gs.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "gs-helper.ts", "export const provided = 'g2';\n")
	assertExplainContains(t, projDir, target, "gs-helper.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})

	writeProjectFile(t, projDir, "gs-helper.ts", "export const provided = 'g1';\n")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneHit})
	writeProjectFile(t, projDir, "global-setup.ts", `export default function setup(project: any) { project.provide('tcKey', 'other'); }
`)
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// Tool configs that Vite, esbuild or PostCSS load on their own must be part of the key.
func TestVitestStaticConfigFilesInvalidate(t *testing.T) {
	cases := []struct{ name, file, before, after string }{
		{"tsconfig extends chain", "config/ts-base.json", `{ "compilerOptions": { "target": "es2020" } }`, `{ "compilerOptions": { "target": "es2022" } }`},
		{"extra tsconfig", "tsconfig.app.json", `{ "compilerOptions": {} }`, `{ "compilerOptions": { "strict": true } }`},
		{"browserslist", ".browserslistrc", "defaults\n", "last 1 chrome version\n"},
		{"postcss config", "postcss.config.cjs", "module.exports = {};\n", "module.exports = { plugins: [] };\n"},
		{"tailwind config", "tailwind.config.cjs", "module.exports = {};\n", "module.exports = { content: [] };\n"},
		{"babel config", "babel.config.json", "{}\n", `{ "presets": [] }`},
		{"patches", "patches/left-pad+1.0.0.patch", "--- a\n", "--- b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projDir := setupFullFixtureCopy(t)
			writeProjectFile(t, projDir, "tsconfig.json", `{
  // comments are legal here
  "extends": "./config/ts-base.json",
}`)
			writeProjectFile(t, projDir, "config/ts-base.json", `{ "compilerOptions": {} }`)
			writeProjectFile(t, projDir, tc.file, tc.before)
			assertRunsThenHits(t, projDir, targetATest)

			writeProjectFile(t, projDir, tc.file, tc.after)
			assertExplainContains(t, projDir, targetATest, tc.file)
			assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneRun})
		})
	}
}

// node_modules can drift from the lockfile (a partial install, a manual edit): the package
// manager's install marker is the cheap witness of what is actually installed.
func TestVitestInstallMarkerInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	linked := filepath.Join(projDir, string(dirNodeModules))
	realModules, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(realModules)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if name := entry.Name(); name == ".bin" || !strings.HasPrefix(name, ".") {
			if err := os.Symlink(filepath.Join(realModules, name), filepath.Join(linked, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	const marker = "node_modules/.package-lock.json"
	writeProjectFile(t, projDir, marker, `{ "name": "fixture", "lockfileVersion": 3 }`)
	assertRunsThenHits(t, projDir, targetATest)

	writeProjectFile(t, projDir, marker, `{ "name": "fixture", "lockfileVersion": 3, "packages": {} }`)
	assertExplainContains(t, projDir, targetATest, marker)
	assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneRun})
}

// A dependency saved moments before the run cannot be told apart from one saved during it by
// its mtime, so that pass is not stored; once the file has aged the next pass is.
func TestVitestFreshInputIsNotCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	usedByA := filepath.Join(projDir, "src", "usedByA.ts")
	data, err := os.ReadFile(usedByA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(usedByA, append(data, []byte("export const touched = 1;\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, targetATest, "src/usedByA.ts")

	ageFixturePath(t, usedByA)
	assertRunsThenHits(t, projDir, targetATest)
}
