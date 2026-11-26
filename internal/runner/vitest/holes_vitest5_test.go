package vitest

import (
	"os"
	"strings"
	"testing"
)

// Flags added in Vitest 5. A value flag the parser does not know would hand its value to
// Vitest as a test file; a flag that lets Vitest pick the files must not be served from the
// cache.
func TestParseArgsVitest5Flags(t *testing.T) {
	valued := ParseArgs([]string{"run", "--repeats", "3", "-p", "unit", "--fsModuleCachePath", "x/cache", "tests/a.test.ts"})
	if len(valued.Positionals) != 1 || valued.Positionals[0] != targetATest || valued.BypassCache {
		t.Fatalf("flag values must not become test files: %+v", valued)
	}
	if len(valued.DiscoveryFlags) != 2 || valued.DiscoveryFlags[0] != "-p" || valued.DiscoveryFlags[1] != "unit" {
		t.Fatalf("-p selects a project, so it is part of discovery: %v", valued.DiscoveryFlags)
	}
	for _, flag := range []string{"--lastFailed", "--sinceLastRun", "--fsModuleCache", "--browser"} {
		if parsed := ParseArgs([]string{"run", flag, "chromium"}); !parsed.BypassCache {
			t.Fatalf("%s must go straight to Vitest", flag)
		}
	}
}

// tc applies --shard itself (see ownedByShard): the flag never reaches Vitest, whose own
// split of the misses would differ from shard to shard, and is not part of any cache key. A
// value Vitest would reject goes to Vitest unchanged.
func TestParseArgsShard(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--shard=1/3"},
		{"run", "--shard", "2/3", "tests/a.test.ts"},
	} {
		parsed := ParseArgs(args)
		if parsed.BypassCache || !parsed.Shard.active() {
			t.Fatalf("%v must be handled by tc, got %+v", args, parsed)
		}
		for _, list := range [][]string{parsed.ForwardFlags, parsed.CacheableFlags, parsed.Positionals} {
			for _, a := range list {
				if strings.Contains(a, "shard") || a == "2/3" {
					t.Fatalf("%v: --shard must not reach Vitest or a key: %+v", args, parsed)
				}
			}
		}
	}
	for _, bad := range []string{"--shard=0/3", "--shard=4/3", "--shard=a/b", "--shard=3"} {
		if parsed := ParseArgs([]string{"run", bad}); !parsed.BypassCache {
			t.Fatalf("%s must go to Vitest, which reports it", bad)
		}
	}
}

// Vitest 5 keeps transformed modules on disk by default. A module served from there never
// reaches tc's transform hook, so a factory-less vi.mock in it was not recorded: creating
// the __mocks__ file afterwards left a failing test cached as a pass. Here tc's cache is
// emptied while Vitest's stays warm, which is what a `tc clean` or an evicted entry looks like.
func TestVitestWarmModuleCacheStillRecordsMocks(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "src/warmdep.ts", "export function val(): string { return 'real'; }\n")
	writeProjectFile(t, projDir, "tests/warmmock.test.ts", `import { expect, it, vi } from 'vitest';
import { val } from '../src/warmdep.js';
vi.mock('../src/warmdep.js');
it('gets the automock', () => { expect(val()).toBeUndefined(); });
`)
	const target = "tests/warmmock.test.ts"
	assertRunsThenHits(t, projDir, target)

	if err := os.RemoveAll(os.Getenv("TESTCACHE_DIR")); err != nil {
		t.Fatal(err)
	}
	assertRunsThenHits(t, projDir, target)

	writeProjectFile(t, projDir, "src/__mocks__/warmdep.ts", "export function val(): string { return 'manual'; }\n")
	assertExplainContains(t, projDir, target, "src/__mocks__/warmdep.ts")
	assertRunOutcome(t, projDir, target, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

// A mocking library that intercepts at the socket (MSW 3) calls net.Socket.prototype.connect
// for every mocked request, with its own lookup and no connection behind it. Judged at that
// level every such test counted as network access and was never cached. Here the lookup
// never answers, so nothing reaches the network.
func TestVitestSocketLevelMockStaysCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "tests/socket-mock.test.ts", `import net from 'node:net';
import { expect, it } from 'vitest';
it('connects without reaching the network', async () => {
  const socket = new net.Socket();
  let asked = '';
  socket.on('error', () => undefined);
  socket.connect({ host: 'mocked.invalid', port: 80, lookup: (host: string) => { asked = host; } } as any);
  await new Promise((resolve) => setImmediate(resolve));
  socket.destroy();
  expect(asked).toBe('mocked.invalid');
});
`)
	assertRunsThenHits(t, projDir, "tests/socket-mock.test.ts")
}

// A connection that is refused still depended on the outside world: never cached.
func TestVitestRefusedConnectionIsNeverCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	writeProjectFile(t, projDir, "tests/refused.test.ts", `import net from 'node:net';
import { expect, it } from 'vitest';
it('sees a refused connection', async () => {
  const code = await new Promise((resolve) => {
    const socket = net.connect({ host: '127.0.0.1', port: 1 });
    socket.on('error', (err: any) => resolve(err.code));
    socket.on('connect', () => { socket.destroy(); resolve('connected'); });
  });
  expect(typeof code).toBe('string');
});
`)
	assertNeverCached(t, projDir, "tests/refused.test.ts", "network access: 127.0.0.1:1")
}

const (
	envListedNoise = "TC_ENVLIST_NOISE"
	envListedUsed  = "TC_ENVLIST_USED"
	targetEnvList  = "tests/envlist.test.ts"
)

func writeEnvListTest(t *testing.T, projDir, body string) {
	t.Helper()
	writeProjectFile(t, projDir, targetEnvList, "import { expect, it } from 'vitest';\nit('reads the environment', async () => {\n"+body+"\n});\n")
}

// Listing the environment's names (what the `debug` package does when it loads) is not a
// read of every value: a variable that only differs between two sessions must not matter.
func TestVitestEnvListingIsNotAReadOfEveryVariable(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv(envListedNoise, "session-a")
	writeEnvListTest(t, projDir, `  const debugKeys = Object.keys(process.env).filter((key) => /^debug_/i.test(key));
  expect(Array.isArray(debugKeys)).toBe(true);`)
	assertRunsThenHits(t, projDir, targetEnvList)

	t.Setenv(envListedNoise, "session-b")
	assertRunOutcome(t, projDir, targetEnvList, runExpectation{summarySubstr: summaryOneHit})
}

// Every way of actually reading a value still makes that variable an input: a get after a
// listing, a copy of the whole environment, and an existence check by name in a later tick.
func TestVitestEnvValueReadsStillInvalidate(t *testing.T) {
	cases := map[string]string{
		"get after listing": `  const keys = Object.keys(process.env).filter((key) => key === '` + envListedUsed + `');
  expect(keys.map((key) => process.env[key])).toEqual(['x']);`,
		"spread copy": `  const copy = { ...process.env };
  expect(copy.` + envListedUsed + `).toBe('x');`,
		"entries": `  const found = Object.entries(process.env).find(([key]) => key === '` + envListedUsed + `');
  expect(found?.[1]).toBe('x');`,
		"hasOwn after a listing": `  const first = Object.keys(process.env)[0];
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(typeof first).toBe('string');
  expect(Object.getOwnPropertyDescriptor(process.env, '` + envListedUsed + `')?.value).toBe('x');`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			projDir := setupFullFixtureCopy(t)
			t.Setenv(envListedUsed, "x")
			writeEnvListTest(t, projDir, body)
			assertRunsThenHits(t, projDir, targetEnvList)

			t.Setenv(envListedUsed, "y")
			assertExplainContains(t, projDir, targetEnvList, envListedUsed)
			assertRunOutcome(t, projDir, targetEnvList, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
		})
	}
}

// A test that lists the environment depends on which variables exist. A variable that appears
// re-runs it; the result for each set of names is kept, so going back is a hit again. A test
// that never lists the environment does not care.
func TestVitestEnvListingDependsOnTheSetOfNames(t *testing.T) {
	const envExtra = "DEBUG_TC_ENVLIST_EXTRA"
	projDir := setupFullFixtureCopy(t)
	writeEnvListTest(t, projDir, `  const debugKeys = Object.keys(process.env).filter((key) => /^debug_tc_envlist/i.test(key));
  expect(debugKeys).toEqual([]);`)
	assertRunsThenHits(t, projDir, targetEnvList)
	assertRunsThenHits(t, projDir, targetATest)

	t.Setenv(envExtra, "1")
	assertExplainContains(t, projDir, targetEnvList, "the set of environment variables changed", "+"+envExtra)
	assertRunOutcome(t, projDir, targetEnvList, runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
	assertRunOutcome(t, projDir, targetATest, runExpectation{summarySubstr: summaryOneHit})

	if err := os.Unsetenv(envExtra); err != nil {
		t.Fatal(err)
	}
	assertRunOutcome(t, projDir, targetEnvList, runExpectation{summarySubstr: summaryOneHit})
}

// How tc was reached is tc's own business: the test does not see it and the key ignores it.
func TestVitestOwnBookkeepingVariablesAreInvisible(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	t.Setenv("TC_INVOKED_BY", "script-shell")
	writeEnvListTest(t, projDir, `  const own = Object.keys(process.env).filter((key) => key === 'TC_INVOKED_BY' || key === 'TC_NODE_PRELOAD_ACTIVE');
  expect(own).toEqual([]);`)
	assertRunsThenHits(t, projDir, targetEnvList)

	t.Setenv("TC_INVOKED_BY", "node-preload")
	t.Setenv("TC_NODE_PRELOAD_ACTIVE", "1")
	assertRunOutcome(t, projDir, targetEnvList, runExpectation{summarySubstr: summaryOneHit})
}
