package vitest

import (
	"testing"
)

const sideEffectTestSource = `import { expect, it } from 'vitest';
import '../src/sideEffect.js';
it('sees the side effects', () => {
  expect((globalThis as any).__tcFlag).toBe('v1');
  expect((globalThis as any).__tcNested).toBe('n1');
});
`

// A module imported only for its side effects, holding a function that never runs, shows no
// executed function in V8 coverage; so does the module it imports the same way.
func TestVitestSideEffectImportInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/nested.ts", `(globalThis as any).__tcNested = 'n1';
(globalThis as any).__tcNestedHandlers = [() => 'never-called'];
`)
	writeProjectFile(t, projDir, "src/sideEffect.ts", `import './nested.js';
(globalThis as any).__tcFlag = 'v1';
(globalThis as any).__tcHandlers = [() => 'never-called'];
`)
	writeProjectFile(t, projDir, "tests/side.test.ts", sideEffectTestSource)
	const target = "tests/side.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/sideEffect.ts", `import './nested.js';
(globalThis as any).__tcFlag = 'v2';
(globalThis as any).__tcHandlers = [() => 'never-called'];
`)
	assertExplainContains(t, projDir, target, "src/sideEffect.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestNestedSideEffectImportInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/nested.ts", `(globalThis as any).__tcNested = 'n1';
(globalThis as any).__tcNestedHandlers = [() => 'never-called'];
`)
	writeProjectFile(t, projDir, "src/sideEffect.ts", `import './nested.js';
(globalThis as any).__tcFlag = 'v1';
(globalThis as any).__tcHandlers = [() => 'never-called'];
`)
	writeProjectFile(t, projDir, "tests/side.test.ts", sideEffectTestSource)
	const target = "tests/side.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/nested.ts", `(globalThis as any).__tcNested = 'n2';
(globalThis as any).__tcNestedHandlers = [() => 'never-called'];
`)
	assertExplainContains(t, projDir, target, "src/nested.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A setup file that only assigns globals and defines things never called is just as invisible.
func TestVitestSetupFileInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "my-setup.ts", `(globalThis as any).__tcSetupFlag = 's1';
(globalThis as any).ResizeObserverStub = class { observe() {} };
`)
	writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
export default defineConfig({ test: { environment: 'node', setupFiles: ['./my-setup.ts'] } });
`)
	writeProjectFile(t, projDir, "tests/setupflag.test.ts", `import { expect, it } from 'vitest';
it('sees the setup flag', () => { expect((globalThis as any).__tcSetupFlag).toBe('s1'); });
`)
	const target = "tests/setupflag.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "my-setup.ts", `(globalThis as any).__tcSetupFlag = 's2';
(globalThis as any).ResizeObserverStub = class { observe() {} };
`)
	assertExplainContains(t, projDir, target, "my-setup.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func assertNeverCached(t *testing.T, projDir, target, explainReason string) {
	t.Helper()
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, target, explainReason)
}

// One cache entry stands for one test file; with test.projects a file has one result per
// project and each project its own config, so nothing is cached. Vitest 4 does not inject
// tc's hooks into projects and Vitest 5 does: the outcome must be the same on both.
func TestVitestProjectsConfigIsNeverCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
export default defineConfig({
  test: { projects: [{ test: { name: 'unit', environment: 'node', include: ['tests/a.test.ts'] } }] },
});
`)
	assertNeverCached(t, projDir, targetATest, "test.projects")
}

// The same file in two projects, failing in one: the pass of the other project must never be
// replayed (on Vitest 5 it was, and the second run exited 0).
func TestVitestFileFailingInOneProjectIsNeverCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
export default defineConfig({
  test: { projects: [
    { define: { __TC_FLAG__: '"one"' }, test: { name: 'one', environment: 'node', include: ['tests/proj.test.ts'] } },
    { define: { __TC_FLAG__: '"two"' }, test: { name: 'two', environment: 'node', include: ['tests/proj.test.ts'] } },
  ] },
});
`)
	writeProjectFile(t, projDir, "tests/proj.test.ts", `import { expect, it } from 'vitest';
declare const __TC_FLAG__: string;
it('passes only in project one', () => { expect(__TC_FLAG__).toBe('one'); });
`)
	for run := 1; run <= 2; run++ {
		assertRunOutcome(t, projDir, "tests/proj.test.ts", runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
	}
}

func TestHasRepeatedModule(t *testing.T) {
	once := []reportModule{{TestPath: "/p/a.test.ts"}, {TestPath: "/p/b.test.ts"}}
	twice := append(append([]reportModule(nil), once...), reportModule{TestPath: "/p/a.test.ts"})
	if hasRepeatedModule(once) || !hasRepeatedModule(twice) {
		t.Fatalf("want false for distinct files and true for a repeated one")
	}
}

// Shared module state between files, vm pools and shuffled order break per-file attribution.
func TestVitestUnsafeConfigIsNeverCached(t *testing.T) {
	cases := []struct{ name, option, reason string }{
		{"isolate false", "isolate: false", "isolate: false"},
		{"vm pool", "pool: 'vmThreads'", "vm pool"},
		{"shuffle", "sequence: { shuffle: true }", "sequence.shuffle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projDir := setupFullFixtureCopy(t)
			writeProjectFile(t, projDir, fileVitestConfig, `import { defineConfig } from 'vitest/config';
export default defineConfig({ test: { environment: 'node', `+tc.option+` } });
`)
			assertNeverCached(t, projDir, targetATest, tc.reason)
		})
	}
}
