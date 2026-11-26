package vitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	targetGlobTest      fixtureTarget = "tests/glob.test.ts"
	targetSpawnTest     fixtureTarget = "tests/spawn.test.ts"
	targetNetworkTest   fixtureTarget = "tests/network.test.ts"
	targetFetchMockTest fixtureTarget = "tests/fetch-mock.test.ts"
)

func TestVitestImportMetaGlobInvalidation(t *testing.T) {
	projDir := setupFullFixtureCopy(t)

	// Cold run passes; warm run hits cache.
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneHit})

	// Adding src/mods/b.ts invalidates cache, explain names glob and baseDir, and test re-runs and fails.
	modB := filepath.Join(projDir, "src", "mods", "b.ts")
	writeFixtureFile(t, modB, "export const name = 'b';\n")
	assertExplainContains(t, projDir, string(targetGlobTest), "./mods/*.ts", "src/mods")
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})

	// Update test to expect 2 modules, cache it, then remove b.ts -> re-runs and fails.
	globTestPath := filepath.Join(projDir, "tests", "glob.test.ts")
	origTest, err := os.ReadFile(globTestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, globTestPath, strings.ReplaceAll(string(origTest), "toBe(1)", "toBe(2)"))
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneHit})

	if err := os.Remove(modB); err != nil {
		t.Fatal(err)
	}
	assertExplainContains(t, projDir, string(targetGlobTest), "./mods/*.ts", "src/mods")
	assertRunOutcome(t, projDir, string(targetGlobTest), runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestImportMetaGlobQueryAndBase(t *testing.T) {
	projDir := setupFullFixtureCopy(t)
	rawDir := filepath.Join(projDir, "src", "rawtext")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helloTxt := filepath.Join(rawDir, "hello.txt")
	writeFixtureFile(t, helloTxt, "hello-v1")

	queryReg := filepath.Join(projDir, "src", "rawRegistry.ts")
	writeFixtureFile(t, queryReg, `const texts = import.meta.glob('./*.txt', { base: './rawtext', query: '?raw', eager: true, import: 'default' });
export const joined = Object.values(texts).join(',');
`)
	queryTest := filepath.Join(projDir, "tests", "glob-query.test.ts")
	writeFixtureFile(t, queryTest, `import { expect, it } from 'vitest';
import { joined } from '../src/rawRegistry.js';
it('reads raw glob', () => { expect(joined).toBe('hello-v1'); });
`)

	assertRunOutcome(t, projDir, "tests/glob-query.test.ts", runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, "tests/glob-query.test.ts", runExpectation{summarySubstr: summaryOneHit})

	writeFixtureFile(t, helloTxt, "hello-v2")
	assertExplainContains(t, projDir, "tests/glob-query.test.ts", "./*.txt", "src/rawtext")
	assertRunOutcome(t, projDir, "tests/glob-query.test.ts", runExpectation{summarySubstr: summaryOneRun, wantFailure: true})
}

func TestVitestSpawnedProcessUncacheable(t *testing.T) {
	projDir := setupFullFixtureCopy(t)

	// execSync test must never be cached across repeated runs, and explain must report why.
	assertRunOutcome(t, projDir, string(targetSpawnTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetSpawnTest), runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, string(targetSpawnTest), `spawned a process: node -e "process.stdout.write('x')"`)

	// worker_threads Worker must also be marked uncacheable.
	workerTest := filepath.Join(projDir, "tests", "worker.test.ts")
	writeFixtureFile(t, workerTest, `import { Worker } from 'node:worker_threads';
import { expect, it } from 'vitest';
it('spawns worker thread', async () => {
  const code = await new Promise((resolve, reject) => {
    const w = new Worker('process.exit(0)', { eval: true });
    w.on('exit', resolve);
    w.on('error', reject);
  });
  expect(code).toBe(0);
});
`)
	assertRunOutcome(t, projDir, "tests/worker.test.ts", runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, "tests/worker.test.ts", runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, "tests/worker.test.ts", "spawned a process: [eval]")
}

func TestVitestNetworkAccessUncacheable(t *testing.T) {
	projDir := setupFullFixtureCopy(t)

	// Real local fetch must never be cached across repeated runs, and explain must show host:port.
	assertRunOutcome(t, projDir, string(targetNetworkTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetNetworkTest), runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, string(targetNetworkTest), "network access: 127.0.0.1:")

	// Unix-domain socket path attempt must also mark test uncacheable.
	unixTest := filepath.Join(projDir, "tests", "unix-sock.test.ts")
	writeFixtureFile(t, unixTest, `import net from 'node:net';
import { expect, it } from 'vitest';
it('attempts unix socket connect', async () => {
  const err = await new Promise((resolve) => {
    const sock = net.connect('/tmp/nonexistent-tc-test.sock');
    sock.on('error', resolve);
  });
  expect(err).toBeTruthy();
});
`)
	assertRunOutcome(t, projDir, "tests/unix-sock.test.ts", runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, "tests/unix-sock.test.ts", runExpectation{summarySubstr: summaryOneRun})
	assertExplainContains(t, projDir, "tests/unix-sock.test.ts", "network access: /tmp/nonexistent-tc-test.sock")
}

func TestVitestMockedFetchStaysCached(t *testing.T) {
	projDir := setupFullFixtureCopy(t)

	assertRunOutcome(t, projDir, string(targetFetchMockTest), runExpectation{summarySubstr: summaryOneRun})
	assertRunOutcome(t, projDir, string(targetFetchMockTest), runExpectation{summarySubstr: summaryOneHit})

	entries := mustExplain(t, projDir, string(targetFetchMockTest))
	if len(entries) != 1 || !entries[0].Cached {
		t.Fatalf("expected mocked fetch test to stay cached, got %+v", entries)
	}
}
