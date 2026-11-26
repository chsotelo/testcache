package vitest

import "testing"

const (
	envLoadEnvTarget = "TC_LOADENV_TARGET"
	envLoadEnvNoise  = "TC_LOADENV_SESSION_NOISE"
	envLoadEnvLate   = "TC_LOADENV_LATE"
	envLoadEnvSource = "TC_LOADENV_EXPAND_SRC"
	targetLoadEnv    = "tests/loadenv.test.ts"
	loadEnvImports   = "import { defineConfig } from 'vitest/config';\nimport { loadEnv } from 'vite';\n"
)

// writeLoadEnvFixture gives the project a config that calls Vite's loadEnv with the empty
// prefix (every variable) and builds `define.__TC_VALUE__` from configBody's `value`.
func writeLoadEnvFixture(t *testing.T, projDir, configBody, want string) {
	t.Helper()
	writeProjectFile(t, projDir, fileVitestConfig, loadEnvImports+`export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
`+configBody+`
});
`)
	writeProjectFile(t, projDir, targetLoadEnv, `import { expect, it } from 'vitest';
declare const __TC_VALUE__: string;
it('sees the value the config took from loadEnv', () => { expect(__TC_VALUE__).toBe('`+want+`'); });
`)
}

const loadEnvDefineBody = `  return {
    define: { __TC_VALUE__: JSON.stringify(env.` + envLoadEnvTarget + ` ?? 'none') },
    test: { environment: 'node' },
  };`

// loadEnv copies the whole environment whatever the prefix. Only the keys the config takes
// from its result are inputs: a variable that differs between two shells or two agent
// sessions (a session id, SHLVL, ...) must not throw the cache away.
func TestVitestLoadEnvIgnoresUnusedVariables(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envLoadEnvTarget, "x")
	t.Setenv(envLoadEnvNoise, "session-a")
	writeLoadEnvFixture(t, projDir, loadEnvDefineBody, "x")
	assertRunsThenHits(t, projDir, targetLoadEnv)

	t.Setenv(envLoadEnvNoise, "session-b")
	assertRunOutcome(t, projDir, targetLoadEnv, runExpectation{summarySubstr: summaryOneHit})
}

// A key the config does take from the loadEnv result is still an input.
func TestVitestLoadEnvUsedVariableInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envLoadEnvTarget, "x")
	writeLoadEnvFixture(t, projDir, loadEnvDefineBody, "x")
	assertRunsThenHits(t, projDir, targetLoadEnv)

	t.Setenv(envLoadEnvTarget, "y")
	assertExplainContains(t, projDir, targetLoadEnv, envLoadEnvTarget)
	assertRunOutcome(t, projDir, targetLoadEnv, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A plugin hook may read the loadEnv result long after the config has loaded.
func TestVitestLoadEnvLateReadInvalidates(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envLoadEnvLate, "x")
	writeProjectFile(t, projDir, fileVitestConfig, loadEnvImports+`export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  return {
    plugins: [{
      name: 'tc-late-env',
      resolveId(id) { return id === 'virtual:tc-late' ? '\0virtual:tc-late' : null; },
      load(id) { return id === '\0virtual:tc-late' ? 'export default ' + JSON.stringify(env.`+envLoadEnvLate+` ?? 'none') : null; },
    }],
    test: { environment: 'node' },
  };
});
`)
	writeProjectFile(t, projDir, targetLoadEnv, `import { expect, it } from 'vitest';
// @ts-ignore virtual module served by the config's plugin
import late from 'virtual:tc-late';
it('sees the value the plugin read late', () => { expect(late).toBe('x'); });
`)
	assertRunsThenHits(t, projDir, targetLoadEnv)

	t.Setenv(envLoadEnvLate, "y")
	assertExplainContains(t, projDir, targetLoadEnv, envLoadEnvLate)
	assertRunOutcome(t, projDir, targetLoadEnv, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// Vite expands $VAR in .env files against the whole environment: the value of one key then
// depends on a variable the config never names, so nothing is narrowed for that loadEnv call.
func TestVitestLoadEnvExpansionStaysConservative(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envLoadEnvSource, "one")
	writeProjectFile(t, projDir, ".env.test", "VITE_TC_EXPANDED=$"+envLoadEnvSource+"\n")
	writeLoadEnvFixture(t, projDir, `  return {
    define: { __TC_VALUE__: JSON.stringify(env.VITE_TC_EXPANDED ?? 'none') },
    test: { environment: 'node' },
  };`, "one")
	assertRunsThenHits(t, projDir, targetLoadEnv)

	t.Setenv(envLoadEnvSource, "two")
	assertExplainContains(t, projDir, targetLoadEnv, envLoadEnvSource)
	assertRunOutcome(t, projDir, targetLoadEnv, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// `test.env: loadEnv(...)` hands the whole result to Vitest, which copies it to the workers:
// the run must behave as without tc, and every key is an input again.
func TestVitestLoadEnvPassedWholeStillWorks(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envLoadEnvTarget, "x")
	writeProjectFile(t, projDir, fileVitestConfig, loadEnvImports+`export default defineConfig(({ mode }) => ({
  test: { environment: 'node', env: loadEnv(mode, process.cwd(), '') },
}));
`)
	writeProjectFile(t, projDir, targetLoadEnv, `import { expect, it } from 'vitest';
it('gets the variable through test.env', () => { expect(process.env.`+envLoadEnvTarget+`).toBe('x'); });
`)
	assertRunsThenHits(t, projDir, targetLoadEnv)

	t.Setenv(envLoadEnvTarget, "y")
	assertRunOutcome(t, projDir, targetLoadEnv, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}
