package vitest

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	targetPromisifyTest fixtureTarget = "tests/promisify.test.ts"
	targetHookShapeTest fixtureTarget = "tests/hook-shape.test.ts"
)

// promisifyTestSource uses the Node APIs whose promisified form has a special shape. tc's
// worker hooks replace these functions, and the replacement must behave exactly the same.
const promisifyTestSource = `import { exec, execFile } from 'node:child_process';
import dns from 'node:dns';
import fs from 'node:fs';
import { promisify } from 'node:util';
import { expect, it } from 'vitest';

const script = 'process.stdout.write("ok")';

it('promisify(execFile) resolves to { stdout, stderr }', async () => {
  const result = await promisify(execFile)(process.execPath, ['-e', script]);
  expect(result).toEqual({ stdout: 'ok', stderr: '' });
});

it('promisify(exec) resolves to { stdout, stderr }', async () => {
  const result = await promisify(exec)(JSON.stringify(process.execPath) + " -e '" + script + "'");
  expect(result).toEqual({ stdout: 'ok', stderr: '' });
});

it('promisify(fs.exists) resolves to a boolean instead of rejecting', async () => {
  expect(await promisify(fs.exists)(import.meta.filename)).toBe(true);
  expect(await promisify(fs.exists)(import.meta.filename + '.missing')).toBe(false);
});

it('promisify(dns.lookup) resolves to { address, family }', async () => {
  const result = await promisify(dns.lookup)('localhost');
  expect(Object.keys(result).sort()).toEqual(['address', 'family']);
  expect(typeof result.address).toBe('string');
});
`

// hookShapeTestSource checks that hooked functions keep the look of the originals.
const hookShapeTestSource = `import * as cp from 'node:child_process';
import dns from 'node:dns';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import net from 'node:net';
import { promisify } from 'node:util';
import { Worker } from 'node:worker_threads';
import { expect, it } from 'vitest';

it('hooked functions keep name, length and custom promisify', () => {
  const expected: Array<[Function, string, number]> = [
    [cp.execFile, 'execFile', 4], [cp.exec, 'exec', 3], [cp.spawn, 'spawn', 3], [cp.fork, 'fork', 1],
    [fs.readFile, 'readFile', 3], [fs.readFileSync, 'readFileSync', 2], [fs.exists, 'exists', 2],
    [fs.stat, 'stat', 1], [fsp.readFile, 'readFile', 2], [dns.lookup, 'lookup', 3],
    [net.Socket.prototype.connect, '', 0],
  ];
  for (const [fn, name, length] of expected) {
    expect([fn.name, fn.length]).toEqual([name, length]);
  }
  for (const fn of [cp.execFile, cp.exec, fs.exists]) {
    expect(typeof (fn as any)[promisify.custom]).toBe('function');
  }
  expect(Object.getOwnPropertySymbols(dns.lookup).length).toBe(1);
  expect(fs.promises).toBe(fsp);
});

it('worker_threads.Worker is still a class', async () => {
  const worker = new Worker('process.exit(0)', { eval: true });
  expect(worker).toBeInstanceOf(Worker);
  expect(Worker.name).toBe('Worker');
  await worker.terminate();
});

it('process.env behaves like a plain object', () => {
  process.env.TC_SHAPE_PROBE = 'x';
  expect(Object.keys(process.env)).toContain('TC_SHAPE_PROBE');
  expect({ ...process.env }.TC_SHAPE_PROBE).toBe('x');
  expect('TC_SHAPE_PROBE' in process.env).toBe(true);
  delete process.env.TC_SHAPE_PROBE;
  expect(process.env.TC_SHAPE_PROBE).toBeUndefined();
});
`

// The results of a test must be the same with and without tc: a hook that changes what a
// Node API returns makes passing tests fail.
func TestVitestHooksKeepPromisifyShapes(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	target := string(targetPromisifyTest)
	writeFixtureFile(t, filepath.Join(projDir, target), promisifyTestSource)

	code, out, errOut := runVitestFixture(t, projDir, false, target)
	if code != 0 || !strings.Contains(out, "tc: 1 file, 0 cached, 1 run") {
		t.Fatalf("the test passes under plain vitest and must pass under tc, code=%d\n%s\n%s", code, out, errOut)
	}
	// The promisified forms bypass the public functions, and must still be seen.
	assertNeverCached(t, projDir, target, "spawned a process")
}

func TestVitestHooksKeepFunctionShapes(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	target := string(targetHookShapeTest)
	writeFixtureFile(t, filepath.Join(projDir, target), hookShapeTestSource)

	code, out, errOut := runVitestFixture(t, projDir, false, target)
	if code != 0 {
		t.Fatalf("hooked functions must look like the originals, code=%d\n%s\n%s", code, out, errOut)
	}
}
