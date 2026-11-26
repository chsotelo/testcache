package vitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// vi.mock('./x') without a factory automocks x unless <dir>/__mocks__/x exists: creating that
// file changes the result without touching anything the test executed before.
func TestVitestMocksDirInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/dep.ts", "export function val(): string { return 'real'; }\n")
	writeProjectFile(t, projDir, "tests/automock.test.ts", `import { expect, it, vi } from 'vitest';
import { val } from '../src/dep.js';
vi.mock('../src/dep.js');
it('gets the automock', () => { expect(val()).toBeUndefined(); });
`)
	const target = "tests/automock.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/__mocks__/dep.ts", "export function val(): string { return 'manual'; }\n")
	assertExplainContains(t, projDir, target, "src/__mocks__/dep.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A package mocked without a factory is redirected to <root>/__mocks__/<package>.
func TestVitestRootMocksDirInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "tests/pkgmock.test.ts", `import { expect, it, vi } from 'vitest';
import { parse } from 'es-module-lexer';
vi.mock('es-module-lexer');
it('gets the automock', () => { expect(vi.isMockFunction(parse)).toBe(true); });
`)
	const target = "tests/pkgmock.test.ts"
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "__mocks__/es-module-lexer.ts", "export const parse = 'manual';\n")
	assertExplainContains(t, projDir, target, "__mocks__")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// With TZ unset Node takes the zone from the system and the locale from LANG / LC_ALL.
func TestVitestLocaleEnvInvalidates(t *testing.T) {
	for _, key := range []string{"LANG", "LC_ALL"} {
		t.Run(key, func(t *testing.T) {
			projDir := setupFullFixtureCopy(t)
			t.Setenv(key, "en_US.UTF-8")
			assertRunsThenHits(t, projDir, targetATest)

			t.Setenv(key, "de_DE.UTF-8")
			assertExplainContains(t, projDir, targetATest, key)
			assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneRun})
		})
	}
}

const shimBinaryName = "node"

// writeNodeShim puts a version-manager style launcher first on PATH: it answers --version
// from .node-version in the working directory and otherwise runs the real node.
func writeNodeShim(t *testing.T) {
	t.Helper()
	realNode, err := exec.LookPath(shimBinaryName)
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	script := `#!/bin/sh
pinned=$(cat .node-version 2>/dev/null)
if [ "$1" = "--version" ] && [ -n "$pinned" ] && [ "$pinned" != "system" ]; then
  echo "v$pinned"
  exit 0
fi
exec "` + realNode + `" "$@"
`
	if err := os.WriteFile(filepath.Join(shimDir, shimBinaryName), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A shim is one unchanged file standing for many Node versions: the pin decides.
func TestVitestNodeShimPinInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeNodeShim(t)
	writeProjectFile(t, projDir, ".node-version", "system\n")
	assertRunsThenHits(t, projDir, targetATest)

	writeProjectFile(t, projDir, ".node-version", "99.0.0\n")
	assertExplainContains(t, projDir, targetATest, "node version changed")
	assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneRun})
	// The shim claimed v99 but Vitest reported the Node it really ran on: nothing was cached.
	assertExplainContains(t, projDir, targetATest, "node version")
}
