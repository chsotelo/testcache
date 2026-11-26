// tc node preload. Installed by scripts/install.sh and loaded into every Node process of
// the shell through NODE_OPTIONS=--require. It does nothing unless the process is the Vitest
// CLI: then the run is handed to `tc vitest` with the same arguments, whatever started it
// (pnpm vitest, pnpm exec, npx, yarn, node_modules/.bin/vitest, an editor, an agent).
//
// Rules: never break a Node process, never loop, never hide a failure. Any error on the way
// to tc is written to stderr and to <cache dir>/preload-errors.log, and plain Vitest runs.
'use strict';
(() => {
  try {
    const env = process.env;
    const disabled = env.TC_DISABLE;
    // tc sets TC_DISABLE for the Vitest it starts; the second variable guards the same loop
    // on any path where it does not.
    if ((disabled && disabled !== '0') || env.TC_NODE_PRELOAD_ACTIVE) return;
    const entry = process.argv[1];
    if (typeof entry !== 'string' || !/(^|[\\/])vitest(\.mjs)?$/.test(entry)) return;
    // Flags given to node itself (--inspect, --cpu-prof, ...) would be lost: leave that run alone.
    if (process.execArgv.length > 0) return;
    if (!require('node:worker_threads').isMainThread) return;

    const fs = require('node:fs');
    const path = require('node:path');

    const logFailure = (what, err) => {
      const detail = err && err.message ? `: ${err.message}` : '';
      try {
        process.stderr.write(`tc: node preload: ${what}${detail}; running plain Vitest\n`);
      } catch {}
      try {
        const dir = env.TESTCACHE_DIR || path.join(require('node:os').homedir(), '.cache', 'testcache');
        const file = path.join(dir, 'preload-errors.log');
        fs.mkdirSync(dir, { recursive: true });
        try {
          if (fs.statSync(file).size > 256 * 1024) fs.renameSync(file, `${file}.1`);
        } catch {}
        const line = `${new Date().toISOString()} pid=${process.pid} cwd=${JSON.stringify(process.cwd())} ${what}${detail}\n`;
        fs.appendFileSync(file, line);
      } catch {}
    };

    // node_modules/.bin/vitest is a link to the real entry in an npm layout.
    let realEntry = entry;
    try {
      realEntry = fs.realpathSync(entry);
    } catch {}
    if (!/[\\/]vitest[\\/]vitest\.mjs$/.test(realEntry)) return;

    // <prefix>/lib/testcache/node-preload.cjs -> <prefix>/bin/tc
    const tcBin = env.TC_NODE_PRELOAD_TC || path.join(__dirname, '..', '..', 'bin', 'tc');
    try {
      fs.accessSync(tcBin, fs.constants.X_OK);
    } catch (err) {
      logFailure(`tc is not runnable at ${tcBin}`, err);
      return;
    }

    const args = ['vitest', ...process.argv.slice(2)];
    const childEnv = { ...env, TC_NODE_PRELOAD_ACTIVE: '1', TC_INVOKED_BY: 'node-preload' };

    if (typeof process.execve === 'function') {
      try {
        // Replaces this process: tc keeps the pid, the terminal, the signals and the exit code.
        process.execve(tcBin, ['tc', ...args], childEnv);
      } catch (err) {
        logFailure('could not replace the process with tc', err);
        return;
      }
    }

    const res = require('node:child_process').spawnSync(tcBin, args, { stdio: 'inherit', env: childEnv });
    if (res.error) {
      logFailure('could not start tc', res.error);
      return;
    }
    const signalExit = { SIGINT: 130, SIGTERM: 143, SIGKILL: 137 };
    process.exit(res.status ?? signalExit[res.signal] ?? 1);
  } catch (err) {
    try {
      process.stderr.write(`tc: node preload: unexpected error: ${err && err.message}; running plain Vitest\n`);
    } catch {}
  }
})();
