package vitest

import "testing"

// Vitest 3 resolves every imported module with existsSync from vite-node, which tc leaves
// out of the test's reads. A test that imports a module without running it and also reads
// that same file itself still depends on the file's content.
func TestVitestReadOfImportedModuleInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/readme-mod.ts", "export function never(): string { return 'v1'; }\n")
	const target = "tests/selfread.test.ts"
	// The absolute path, as vite-node checks it: a read of the same path by the test counts.
	writeProjectFile(t, projDir, target, `import fs from 'node:fs';
import path from 'node:path';
import { expect, it } from 'vitest';
import { never } from '../src/readme-mod.js';
// Kept by the transform, never called: the module is loaded but none of it runs.
export function unused() { return never; }
it('reads the source of a module it imports', () => {
  expect(fs.readFileSync(path.resolve('src/readme-mod.ts'), 'utf8')).toContain('v1');
});
`)
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/readme-mod.ts", "export function never(): string { return 'v2'; }\n")
	assertExplainContains(t, projDir, target, "src/readme-mod.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A Vite virtual module served by a config plugin is no file: Vitest 3 reports it under the
// project root as "<root>/\0virtual:x", which must not stop the result from being stored.
func TestVitestVirtualModuleIsCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
export default defineConfig({
  plugins: [{
    name: 'tc-virtual',
    resolveId(id) { return id === 'virtual:tc-v' ? '\0virtual:tc-v' : null; },
    load(id) { return id === '\0virtual:tc-v' ? 'export default 42' : null; },
  }],
  test: { environment: 'node' },
});
`)
	const target = "tests/virtual.test.ts"
	writeProjectFile(t, projDir, target, `import { expect, it } from 'vitest';
// @ts-ignore virtual module served by the config's plugin
import v from 'virtual:tc-v';
it('sees the virtual module', () => { expect(v).toBe(42); });
`)
	assertRunsThenHits(t, projDir, target)
}

// Vitest 3 comes with Vite 7, whose parser reads JavaScript only. A test file written in
// TypeScript must still have its factory-less vi.mock found, or creating the __mocks__ file
// afterwards would leave the automocked pass cached.
func TestVitestMocksDirInvalidatesInTypeScript(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/typed-dep.ts", "export function val(): string { return 'real'; }\n")
	const target = "tests/typed-automock.test.ts"
	writeProjectFile(t, projDir, target, `import { expect, it, vi } from 'vitest';
import { val } from '../src/typed-dep.js';
type Result = string | undefined;
vi.mock('../src/typed-dep.js');
it('gets the automock', () => {
  const got: Result = val() as Result;
  expect(got).toBeUndefined();
});
`)
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/__mocks__/typed-dep.ts", "export function val(): string { return 'manual'; }\n")
	assertExplainContains(t, projDir, target, "src/__mocks__/typed-dep.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}
