import crypto from 'node:crypto';
import { createRequire, syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { afterAll, beforeEach, expect } from 'vitest';

const HOOK_KEY = Symbol.for('testcache.workerHooks');
// Set by tc's own integrations on the way to this run; never an input of a test.
const TC_OWN_ENV_KEYS = new Set(['TC_INVOKED_BY', 'TC_NODE_PRELOAD_ACTIVE']);
// Recorded when a test lists the environment; tc turns it into the list of variable names.
const ENV_NAMES_KEY = 'tc:env-names';
const IGNORED_ENV_KEYS = new Set([
  'then',
  'toJSON',
  'valueOf',
  'toString',
  'constructor',
  '__proto__',
  'hasOwnProperty',
  'isPrototypeOf',
  'propertyIsEnumerable',
  'toLocaleString',
  'VITEST_WORKER_ID',
  'VITEST_POOL_ID',
]);

function getHookState() {
  let state = globalThis[HOOK_KEY];
  if (state) {
    return state;
  }
  const req = createRequire(import.meta.url);
  const cjsFs = req('node:fs');
  const cjsFsp = req('node:fs/promises');
  state = {
    rawEnv: process.env,
    cjsFs,
    cjsFsp,
    origFs: {
      realpathSync: cjsFs.realpathSync.bind(cjsFs),
      mkdirSync: cjsFs.mkdirSync.bind(cjsFs),
      writeFileSync: cjsFs.writeFileSync.bind(cjsFs),
      renameSync: cjsFs.renameSync.bind(cjsFs),
    },
    realpathCache: new Map(),
    fsHooked: false,
    envHooked: false,
    cpHooked: false,
    wtHooked: false,
    netHooked: false,
    cpDepth: 0,
    netDepth: 0,
    inspectorMod: null,
    session: null,
    profilerEnabled: false,
    coverageStarted: false,
    active: null,
  };
  globalThis[HOOK_KEY] = state;
  return state;
}

const state = getHookState();

function realpathSafe(p) {
  const cached = state.realpathCache.get(p);
  if (cached !== undefined) {
    return cached;
  }
  try {
    const resolved = state.origFs.realpathSync(p);
    state.realpathCache.set(p, resolved);
    return resolved;
  } catch {
    return path.resolve(p);
  }
}

function resolveWithMissingAncestor(absPath) {
  const cached = state.realpathCache.get(absPath);
  if (cached !== undefined) {
    return cached;
  }
  try {
    const resolved = state.origFs.realpathSync(absPath);
    state.realpathCache.set(absPath, resolved);
    return resolved;
  } catch {
    const tail = [];
    let cur = absPath;
    for (;;) {
      const parent = path.dirname(cur);
      if (parent === cur) {
        return absPath;
      }
      tail.unshift(path.basename(cur));
      cur = parent;
      try {
        const realBase = state.origFs.realpathSync(cur);
        state.realpathCache.set(cur, realBase);
        return path.join(realBase, ...tail);
      } catch {
        // Keep walking up until an existing ancestor is found.
      }
    }
  }
}

function toPathString(rawArg) {
  if (typeof rawArg === 'string') {
    if (rawArg.startsWith('file://')) {
      try {
        return fileURLToPath(rawArg);
      } catch {
        return null;
      }
    }
    return rawArg;
  }
  if (rawArg instanceof URL) {
    if (rawArg.protocol !== 'file:') {
      return null;
    }
    try {
      return fileURLToPath(rawArg);
    } catch {
      return null;
    }
  }
  if (Buffer.isBuffer(rawArg)) {
    return rawArg.toString('utf8');
  }
  return null;
}

function isExcludedPath(rp, repoRoot, runtimeDir) {
  if (rp !== repoRoot && !rp.startsWith(repoRoot + path.sep)) {
    return true;
  }
  if (rp.endsWith(`${path.sep}node_modules`) || rp.includes(`${path.sep}node_modules${path.sep}`)) {
    return true;
  }
  if (rp.endsWith(`${path.sep}.git`) || rp.includes(`${path.sep}.git${path.sep}`)) {
    return true;
  }
  if (runtimeDir && (rp === runtimeDir || rp.startsWith(runtimeDir + path.sep))) {
    return true;
  }
  return false;
}

function recordFsPath(rawArg) {
  const active = state.active;
  if (!active) {
    return;
  }
  const p = toPathString(rawArg);
  if (!p) {
    return;
  }
  const cacheKey = path.isAbsolute(p) ? p : `${process.cwd()}\0${p}`;
  if (active.seenRawPaths.has(cacheKey)) {
    return;
  }
  active.seenRawPaths.add(cacheKey);
  if (p.includes('/node_modules/') || p.includes('\\node_modules\\')) {
    return;
  }
  const abs = path.isAbsolute(p) ? path.normalize(p) : path.resolve(p);
  const rp = resolveWithMissingAncestor(abs);
  if (isExcludedPath(rp, active.repoRoot, active.runtimeDir)) {
    return;
  }
  active.fsPaths.add(rp);
}

// mirrorFunction makes a hook indistinguishable from the function it replaces: same name,
// length, prototype chain and own properties. The properties matter: util.promisify reads
// util.promisify.custom (exec, execFile, fs.exists) and an internal argument-names symbol
// (dns.lookup, fs.read) from the function, and without them it resolves to another shape.
// A custom promisified form calls Node internals instead of the hooked function, so it is
// wrapped to make the same record first: recordCall gets the arguments of the call.
function mirrorFunction(hook, orig, recordCall) {
  for (const key of Reflect.ownKeys(orig)) {
    if (key === 'prototype' || key === 'arguments' || key === 'caller') {
      continue;
    }
    const desc = Object.getOwnPropertyDescriptor(orig, key);
    if (key === promisify.custom && desc.value === orig) {
      // Node marks a promisified function as its own promisified form.
      desc.value = hook;
    } else if (key === promisify.custom && typeof desc.value === 'function') {
      const origCustom = desc.value;
      desc.value = mirrorFunction(
        function tcPromisifiedHook(...args) {
          recordCall(args);
          return origCustom.apply(this, args);
        },
        origCustom,
        recordCall,
      );
    }
    try {
      Object.defineProperty(hook, key, desc);
    } catch {
      // A property that cannot be mirrored is left as it is.
    }
  }
  Object.setPrototypeOf(hook, Object.getPrototypeOf(orig));
  return hook;
}

function installFsHooks() {
  if (state.fsHooked) {
    return;
  }
  const fsMethods = [
    'readFile',
    'readFileSync',
    'readdir',
    'readdirSync',
    'stat',
    'statSync',
    'lstat',
    'lstatSync',
    'exists',
    'existsSync',
    'access',
    'accessSync',
    'createReadStream',
    'opendir',
    'opendirSync',
  ];
  for (const name of fsMethods) {
    const orig = state.cjsFs[name];
    if (typeof orig !== 'function') {
      continue;
    }
    state.cjsFs[name] = mirrorFunction(
      function tcFsHook(pathArg, ...rest) {
        recordFsPath(pathArg);
        return orig.call(this, pathArg, ...rest);
      },
      orig,
      (args) => recordFsPath(args[0]),
    );
  }

  const fspMethods = ['readFile', 'readdir', 'stat', 'lstat', 'access', 'opendir'];
  for (const name of fspMethods) {
    const orig = state.cjsFsp[name];
    if (typeof orig !== 'function') {
      continue;
    }
    state.cjsFsp[name] = mirrorFunction(
      function tcFspHook(pathArg, ...rest) {
        recordFsPath(pathArg);
        return orig.call(this, pathArg, ...rest);
      },
      orig,
      (args) => recordFsPath(args[0]),
    );
  }

  syncBuiltinESMExports();
  state.fsHooked = true;
}

function installEnvHook() {
  if (state.envHooked) {
    return;
  }
  const rawEnv = state.rawEnv;
  const recordEnvKey = (prop) => {
    const active = state.active;
    if (!active || typeof prop !== 'string') {
      return;
    }
    if (prop.startsWith('TESTCACHE_') || TC_OWN_ENV_KEYS.has(prop) || IGNORED_ENV_KEYS.has(prop)) {
      return;
    }
    active.envKeys.add(prop);
  };
  // Listing the environment (Object.keys, for...in, the first half of a spread) makes the
  // engine ask for the descriptor of every key in turn. That reveals names, not values: the
  // `debug` package does it on load to find DEBUG_* variables, and counting each of those
  // descriptor checks as a read made the whole environment an input of every test that
  // loads it. A descriptor request that continues a listing is therefore not a read; the
  // value still is when the code goes on to get it (a spread or Object.entries gets every
  // key, so those record everything). A listing only stays open for the current tick.
  const deferTick = typeof queueMicrotask === 'function' ? queueMicrotask : (fn) => Promise.resolve().then(fn);
  let listing = null;
  const continuesListing = (prop) => {
    if (!listing || listing.keys[listing.next] !== prop) {
      listing = null;
      return false;
    }
    listing.next++;
    if (listing.next >= listing.keys.length) listing = null;
    return true;
  };
  const envProxy = new Proxy(rawEnv, {
    get(target, prop) {
      recordEnvKey(prop);
      return Reflect.get(target, prop, target);
    },
    has(target, prop) {
      recordEnvKey(prop);
      return Reflect.has(target, prop);
    },
    getOwnPropertyDescriptor(target, prop) {
      if (!continuesListing(prop)) recordEnvKey(prop);
      return Reflect.getOwnPropertyDescriptor(target, prop);
    },
    set(target, prop, value) {
      return Reflect.set(target, prop, value);
    },
    deleteProperty(target, prop) {
      return Reflect.deleteProperty(target, prop);
    },
    defineProperty(target, prop, desc) {
      return Reflect.defineProperty(target, prop, desc);
    },
    ownKeys(target) {
      const keys = Reflect.ownKeys(target);
      // Whatever the code does with the names, it now depends on which variables exist.
      if (state.active) state.active.envKeys.add(ENV_NAMES_KEY);
      const opened = { keys, next: 0 };
      listing = keys.length > 0 ? opened : null;
      deferTick(() => {
        if (listing === opened) listing = null;
      });
      return keys;
    },
  });
  Object.defineProperty(process, 'env', {
    value: envProxy,
    configurable: true,
    writable: true,
    enumerable: true,
  });
  syncBuiltinESMExports();
  state.envHooked = true;
}

function recordUncacheable(reason) {
  const active = state.active;
  if (!active || !reason) {
    return;
  }
  if (!active.uncacheableReasons.includes(reason)) {
    active.uncacheableReasons.push(reason);
  }
}

function recordNetworkAccess(target, host, hasPort) {
  const active = state.active;
  if (!active || !target) {
    return;
  }
  const prefix = 'network access: ';
  const reason = `${prefix}${target}`;
  if (hasPort && host) {
    const bare = `${prefix}${host}`;
    const idx = active.uncacheableReasons.indexOf(bare);
    if (idx !== -1) {
      if (active.uncacheableReasons.includes(reason)) {
        active.uncacheableReasons.splice(idx, 1);
      } else {
        active.uncacheableReasons[idx] = reason;
      }
      return;
    }
  } else if (!hasPort && host) {
    const hostPrefix = `${prefix}${host}:`;
    if (active.uncacheableReasons.some((r) => r.startsWith(hostPrefix))) {
      return;
    }
  }
  recordUncacheable(reason);
}

function formatProcessTarget(rawArg) {
  const p = toPathString(rawArg);
  if (p) {
    const firstLine = p.split(/\r?\n/, 1)[0].trim();
    return firstLine || 'unknown';
  }
  if (rawArg instanceof URL) {
    return rawArg.href;
  }
  return String(rawArg ?? 'unknown');
}

function installChildProcessHooks() {
  if (state.cpHooked) {
    return;
  }
  const req = createRequire(import.meta.url);
  const cjsCp = req('node:child_process');
  const methods = ['spawn', 'spawnSync', 'exec', 'execSync', 'execFile', 'execFileSync', 'fork'];
  for (const name of methods) {
    const orig = cjsCp[name];
    if (typeof orig !== 'function') {
      continue;
    }
    const recordSpawn = (args) => {
      if (state.cpDepth === 0) {
        recordUncacheable(`spawned a process: ${formatProcessTarget(args[0])}`);
      }
    };
    cjsCp[name] = mirrorFunction(
      function tcChildProcessHook(...args) {
        recordSpawn(args);
        state.cpDepth++;
        try {
          return orig.apply(this, args);
        } finally {
          state.cpDepth--;
        }
      },
      orig,
      recordSpawn,
    );
  }
  syncBuiltinESMExports();
  state.cpHooked = true;
}

function installWorkerThreadsHooks() {
  if (state.wtHooked) {
    return;
  }
  const req = createRequire(import.meta.url);
  const cjsWt = req('node:worker_threads');
  const OrigWorker = cjsWt.Worker;
  if (typeof OrigWorker === 'function') {
    cjsWt.Worker = new Proxy(OrigWorker, {
      construct(target, args, newTarget) {
        const desc = args[1]?.eval ? '[eval]' : formatProcessTarget(args[0]);
        recordUncacheable(`spawned a process: ${desc}`);
        return Reflect.construct(target, args, newTarget);
      },
    });
  }
  syncBuiltinESMExports();
  state.wtHooked = true;
}

function extractNetConnectTarget(rawArgs) {
  const args = rawArgs.length > 0 && Array.isArray(rawArgs[0]) ? rawArgs[0] : rawArgs;
  const first = args[0];
  if (first && typeof first === 'object' && !(first instanceof String)) {
    if (first.path) {
      return { target: String(first.path), host: '', hasPort: false };
    }
    const host = String(first.host || first.hostname || 'localhost');
    if (first.port !== undefined && first.port !== null && first.port !== '') {
      return { target: `${host}:${first.port}`, host, hasPort: true };
    }
    return { target: host, host, hasPort: false };
  }
  if (typeof first === 'number' || (typeof first === 'string' && /^\d+$/.test(first))) {
    const host = typeof args[1] === 'string' ? args[1] : 'localhost';
    return { target: `${host}:${first}`, host, hasPort: true };
  }
  if (typeof first === 'string') {
    return { target: first, host: '', hasPort: false };
  }
  return { target: 'unknown', host: '', hasPort: false };
}

function extractDgramSendTarget(args) {
  let port;
  let host;
  if (typeof args[1] === 'number' && typeof args[2] === 'number') {
    port = args[3];
    host = typeof args[4] === 'string' ? args[4] : 'localhost';
  } else if (typeof args[1] === 'number') {
    port = args[1];
    host = typeof args[2] === 'string' ? args[2] : 'localhost';
  }
  if (typeof port === 'number') {
    return { target: `${host}:${port}`, host, hasPort: true };
  }
  return { target: 'dgram', host: '', hasPort: false };
}

function extractDgramConnectTarget(args) {
  const port = args[0];
  const host = typeof args[1] === 'string' ? args[1] : 'localhost';
  if (port !== undefined && port !== null) {
    return { target: `${host}:${port}`, host, hasPort: true };
  }
  return { target: host, host, hasPort: false };
}

// Watches the calls that really open a connection: the connect methods of Node's TCP and pipe
// handles. net.Socket.prototype.connect is one level too high for that: a mocking library
// that intercepts at the socket (MSW 3 does) calls it for every mocked request, with its own
// lookup and a handle that never reaches the network, and every such test looked like
// network access. Returns false when the handles cannot be reached (process.binding gone),
// and the caller then keeps judging by Socket.prototype.connect: too strict, never too lax.
function installNativeConnectHooks() {
  const quiet = process.noDeprecation;
  try {
    // process.binding is deprecated (DEP0111) and warns under --pending-deprecation.
    process.noDeprecation = true;
    const tcpProto = process.binding('tcp_wrap')?.TCP?.prototype;
    const pipeProto = process.binding('pipe_wrap')?.Pipe?.prototype;
    if (typeof tcpProto?.connect !== 'function' || typeof tcpProto?.connect6 !== 'function') return false;
    if (typeof pipeProto?.connect !== 'function') return false;
    for (const method of ['connect', 'connect6']) {
      const orig = tcpProto[method];
      tcpProto[method] = function tcNativeTcpConnect(connectReq, address, port) {
        recordNetworkAccess(`${address}:${port}`, String(address), true);
        return orig.apply(this, arguments);
      };
    }
    const origPipeConnect = pipeProto.connect;
    pipeProto.connect = function tcNativePipeConnect(connectReq, address) {
      recordNetworkAccess(String(address), '', false);
      return origPipeConnect.apply(this, arguments);
    };
    return true;
  } catch {
    return false;
  } finally {
    process.noDeprecation = quiet;
  }
}

function installNetworkHooks() {
  if (state.netHooked) {
    return;
  }
  const req = createRequire(import.meta.url);
  const cjsNet = req('node:net');
  const nativeConnects = installNativeConnectHooks();
  const origConnect = cjsNet.Socket?.prototype?.connect;
  if (typeof origConnect === 'function') {
    const recordConnect = (args) => {
      if (nativeConnects) return;
      const info = extractNetConnectTarget(args);
      recordNetworkAccess(info.target, info.host, info.hasPort);
    };
    cjsNet.Socket.prototype.connect = mirrorFunction(
      function tcNetConnectHook(...args) {
        recordConnect(args);
        state.netDepth++;
        try {
          return origConnect.apply(this, args);
        } finally {
          state.netDepth--;
        }
      },
      origConnect,
      recordConnect,
    );
  }

  const cjsDgram = req('node:dgram');
  const origDgramSend = cjsDgram.Socket?.prototype?.send;
  if (typeof origDgramSend === 'function') {
    const recordSend = (args) => {
      const info = extractDgramSendTarget(args);
      recordNetworkAccess(info.target, info.host, info.hasPort);
    };
    cjsDgram.Socket.prototype.send = mirrorFunction(
      function tcDgramSendHook(...args) {
        recordSend(args);
        return origDgramSend.apply(this, args);
      },
      origDgramSend,
      recordSend,
    );
  }
  const origDgramConnect = cjsDgram.Socket?.prototype?.connect;
  if (typeof origDgramConnect === 'function') {
    const recordDgramConnect = (args) => {
      const info = extractDgramConnectTarget(args);
      recordNetworkAccess(info.target, info.host, info.hasPort);
    };
    cjsDgram.Socket.prototype.connect = mirrorFunction(
      function tcDgramConnectHook(...args) {
        recordDgramConnect(args);
        return origDgramConnect.apply(this, args);
      },
      origDgramConnect,
      recordDgramConnect,
    );
  }

  const cjsDns = req('node:dns');
  const cjsDnsPromises = req('node:dns/promises');
  const wrapLookup = (obj) => {
    if (!obj || typeof obj.lookup !== 'function') {
      return;
    }
    const orig = obj.lookup;
    const recordLookup = (args) => {
      if (state.netDepth === 0 && args[0]) {
        const host = String(args[0]);
        recordNetworkAccess(host, host, false);
      }
    };
    obj.lookup = mirrorFunction(
      function tcDnsLookupHook(hostname, ...rest) {
        recordLookup([hostname]);
        return orig.call(this, hostname, ...rest);
      },
      orig,
      recordLookup,
    );
  };
  wrapLookup(cjsDns);
  wrapLookup(cjsDnsPromises);
  if (cjsDns.promises && cjsDns.promises !== cjsDnsPromises) {
    wrapLookup(cjsDns.promises);
  }

  syncBuiltinESMExports();
  state.netHooked = true;
}

function hookSnapshotEnvironment(snapEnv) {
  if (!snapEnv || snapEnv.__tcHooked) {
    return;
  }
  if (typeof snapEnv.readSnapshotFile === 'function') {
    const origRead = snapEnv.readSnapshotFile;
    snapEnv.readSnapshotFile = function tcReadSnapshotFile(filepath, ...rest) {
      recordFsPath(filepath);
      return origRead.call(this, filepath, ...rest);
    };
  }
  if (typeof snapEnv.resolveRawPath === 'function') {
    const origResolve = snapEnv.resolveRawPath;
    snapEnv.resolveRawPath = async function tcResolveRawPath(testPath, rawPath, ...rest) {
      const resolved = await origResolve.call(this, testPath, rawPath, ...rest);
      recordFsPath(resolved);
      return resolved;
    };
  }
  snapEnv.__tcHooked = true;
}

const rawEnv = state.rawEnv;
const coverageDir = rawEnv.TESTCACHE_COVERAGE_DIR;
const repoRoot = realpathSafe(rawEnv.TESTCACHE_REPO_ROOT || process.cwd());
const runtimeDir = rawEnv.TESTCACHE_RUNTIME_DIR ? realpathSafe(rawEnv.TESTCACHE_RUNTIME_DIR) : '';

state.active = {
  fsPaths: new Set(),
  seenRawPaths: new Set(),
  envKeys: new Set(),
  uncacheableReasons: [],
  repoRoot,
  runtimeDir,
};

installFsHooks();
installEnvHook();
installChildProcessHooks();
installWorkerThreadsHooks();
installNetworkHooks();
hookSnapshotEnvironment(globalThis.__vitest_worker__?.config?.snapshotOptions?.snapshotEnvironment);
beforeEach(() => {
  const snapEnv =
    expect.getState()?.snapshotState?.environment ||
    globalThis.__vitest_worker__?.config?.snapshotOptions?.snapshotEnvironment;
  hookSnapshotEnvironment(snapEnv);
});

let covStarted = false;
try {
  if (coverageDir && rawEnv.TESTCACHE_DISABLE_V8_COVERAGE !== '1') {
    if (!state.inspectorMod) {
      state.inspectorMod = await import('node:inspector/promises');
    }
    if (!state.session) {
      state.session = new state.inspectorMod.Session();
      state.session.connect();
    }
    if (!state.profilerEnabled) {
      await state.session.post('Profiler.enable');
      state.profilerEnabled = true;
    }
    if (!state.coverageStarted) {
      await state.session.post('Profiler.startPreciseCoverage', {
        callCount: false,
        detailed: false,
      });
      state.coverageStarted = true;
    } else {
      await state.session.post('Profiler.takePreciseCoverage');
    }
    covStarted = true;
  }
} catch {
  covStarted = false;
  if (state.session) {
    try {
      state.session.disconnect();
    } catch {}
    state.session = null;
    state.profilerEnabled = false;
    state.coverageStarted = false;
  }
}

function normalizeScriptPath(rawUrl) {
  if (
    !rawUrl ||
    rawUrl.startsWith('node:') ||
    rawUrl.startsWith('data:') ||
    rawUrl.startsWith('vitest:') ||
    rawUrl.startsWith('wasm:')
  ) {
    return null;
  }
  if (rawUrl.includes('/node_modules/') || rawUrl.includes('\\node_modules\\')) {
    return null;
  }
  let p = rawUrl;
  if (p.startsWith('file://')) {
    try {
      p = fileURLToPath(p);
    } catch {
      return null;
    }
  }
  const qIdx = p.indexOf('?');
  if (qIdx !== -1) p = p.slice(0, qIdx);
  const hIdx = p.indexOf('#');
  if (hIdx !== -1) p = p.slice(0, hIdx);
  if (!path.isAbsolute(p)) {
    return null;
  }
  if (p.includes(`${path.sep}node_modules${path.sep}`)) {
    return null;
  }
  p = realpathSafe(p);
  if (isExcludedPath(p, repoRoot, runtimeDir)) {
    return null;
  }
  return p;
}

function isLoadedScript(script) {
  return (script.functions || []).some((fn) => (fn.ranges || []).some((r) => r.count > 0));
}

function isExecutedScript(script) {
  const fns = script.functions || [];
  if (fns.length === 0) {
    return false;
  }
  let maxEnd = 0;
  for (const fn of fns) {
    for (const r of fn.ranges || []) {
      if (r.endOffset > maxEnd) maxEnd = r.endOffset;
    }
  }
  const isOuterWrapper = (fn) => {
    if (fn.functionName !== '') return false;
    const ranges = fn.ranges || [];
    return ranges.length > 0 && ranges.every((r) => r.startOffset <= 20 && r.endOffset >= maxEnd - 2);
  };

  const innerFns = fns.filter((fn) => !isOuterWrapper(fn));
  if (innerFns.length > 0) {
    return innerFns.some((fn) => (fn.ranges || []).some((r) => r.count > 0));
  }
  return fns.some((fn) => (fn.ranges || []).some((r) => r.count > 0));
}

afterAll(async () => {
  const snapState = expect.getState()?.snapshotState;
  if (snapState?.snapshotPath) {
    recordFsPath(snapState.snapshotPath);
  }
  const active = state.active;
  state.active = null;
  if (!coverageDir) {
    return;
  }

  let result = [];
  let hasCoverage = false;
  if (covStarted && state.session) {
    try {
      const taken = await state.session.post('Profiler.takePreciseCoverage');
      result = taken?.result || [];
      hasCoverage = true;
    } catch {
      hasCoverage = false;
    }
  }

  const rawTestPath = expect.getState()?.testPath || globalThis.__vitest_worker__?.filepath;
  if (!rawTestPath) {
    return;
  }
  const testPath = realpathSafe(rawTestPath);

  // executed: a function of the module ran, so the test depends on its code.
  // loaded: the module was evaluated at all; tc looks up its bare imports and vi.mock calls.
  const executed = new Set();
  const loaded = new Set();
  if (hasCoverage) {
    for (const script of result) {
      const filePath = normalizeScriptPath(script.url);
      if (!filePath || filePath === testPath) {
        continue;
      }
      if (isLoadedScript(script)) {
        loaded.add(filePath);
      }
      if (isExecutedScript(script)) {
        executed.add(filePath);
      }
    }
  }

  const fsReads = [];
  if (active) {
    for (const p of active.fsPaths) {
      if (p !== testPath) {
        fsReads.push(p);
      }
    }
  }
  fsReads.sort();

  const envReads = active ? Array.from(active.envKeys).sort() : [];
  const uncacheableReason =
    active && active.uncacheableReasons.length > 0 ? active.uncacheableReasons.join('; ') : '';

  const hash = crypto.createHash('sha256').update(testPath).digest('hex').slice(0, 24);
  const outPath = path.join(coverageDir, `${hash}.json`);
  const tmpPath = `${outPath}.${process.pid}.tmp`;
  state.origFs.mkdirSync(coverageDir, { recursive: true });
  state.origFs.writeFileSync(
    tmpPath,
    JSON.stringify({
      testPath,
      hasCoverage,
      executedFiles: Array.from(executed).sort(),
      loadedFiles: Array.from(loaded).sort(),
      fsReads,
      envReads,
      ...(uncacheableReason ? { uncacheableReason } : {}),
    }),
  );
  state.origFs.renameSync(tmpPath, outPath);
});
