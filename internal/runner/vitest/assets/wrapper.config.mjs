import fs from 'node:fs';
import nodeModule, { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const GLOB_KEY = Symbol.for('testcache.importGlobs');
const CONFIG_LOAD_KEY = Symbol.for('testcache.configLoad');
const FACTS_KEY = Symbol.for('testcache.moduleFacts');
const ENV_USE_KEY = 'testcache.envUse';
// Set by tc's own integrations to label the run log; never an input of the project config.
const TC_LABEL_ENV = 'TC_INVOKED_BY';
const FILE_URL_PREFIX = 'file:';
const DATA_URL_PREFIX = 'data:';
const PARENT_POLL_MS = 250;
const MOCKS_DIR = '__mocks__';
const MOCK_OBJECTS = new Set(['vi', 'vitest']);
const MOCK_METHODS = new Set(['mock', 'doMock']);
// Modules worth parsing: import.meta.glob, a bare `import 'x'`, or a vi.mock/vi.doMock call.
const TRACKED_SYNTAX = /import\.meta\.glob|\bimport\s*(?:['"]|\{\s*\}\s*from)|\b(?:mock|doMock)\s*\(/;
const INVALID_AST_VAL = Symbol('invalid');
const JS_TS_EXTS = new Set(['.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx', '.mts', '.cts']);

const projectRoot = process.env.TESTCACHE_PROJECT_ROOT || process.cwd();
const vitestConfigPath = process.env.TESTCACHE_VITEST_CONFIG_ENTRY;
const vitestConfigEntry = vitestConfigPath ? pathToFileURL(vitestConfigPath).href : 'vitest/config';

let viteEntry = process.env.TESTCACHE_VITE_ENTRY
  ? pathToFileURL(process.env.TESTCACHE_VITE_ENTRY).href
  : '';
if (!viteEntry) {
  try {
    const req = createRequire(vitestConfigPath || `${projectRoot}/package.json`);
    viteEntry = pathToFileURL(req.resolve('vite')).href;
  } catch {
    viteEntry = 'vite';
  }
}

const viteMod = await import(/* @vite-ignore */ viteEntry);
const { loadConfigFromFile, parseSync, parseAstAsync } = viteMod;
const { defineConfig } = await import(/* @vite-ignore */ vitestConfigEntry);

function realpathSafe(p) {
  try {
    return fs.realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

function resolveWithMissingAncestor(absPath) {
  try {
    return fs.realpathSync(absPath);
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
        return path.join(fs.realpathSync(cur), ...tail);
      } catch {
        // Keep walking up until an existing ancestor is found.
      }
    }
  }
}

function toPosix(p) {
  return p.split(path.sep).join('/');
}

function stripQueryAndHash(raw) {
  let s = raw || '';
  const qIdx = s.indexOf('?');
  if (qIdx !== -1) s = s.slice(0, qIdx);
  const hIdx = s.indexOf('#');
  if (hIdx !== -1) s = s.slice(0, hIdx);
  return s;
}

async function parseSourceProgram(cleanId, code) {
  if (typeof parseSync === 'function') {
    const ext = path.extname(cleanId).toLowerCase();
    const opts = JS_TS_EXTS.has(ext) ? undefined : { lang: 'tsx' };
    const parsed = parseSync(cleanId, code, opts);
    if (parsed?.program && (!parsed.errors || parsed.errors.length === 0 || parsed.program.body?.length > 0)) {
      return parsed.program;
    }
  }
  if (typeof parseAstAsync === 'function') {
    try {
      return await parseAstAsync(code);
    } catch {
      return null;
    }
  }
  return null;
}

function isImportMetaGlobCallee(callee) {
  if (!callee || callee.type !== 'MemberExpression' || callee.computed) {
    return false;
  }
  const obj = callee.object;
  const prop = callee.property;
  return (
    obj?.type === 'MetaProperty' &&
    obj.meta?.name === 'import' &&
    obj.property?.name === 'meta' &&
    prop?.type === 'Identifier' &&
    prop.name === 'glob'
  );
}

function walkAst(node, onNode) {
  if (!node || typeof node !== 'object') return;
  if (Array.isArray(node)) {
    for (const item of node) walkAst(item, onNode);
    return;
  }
  if (typeof node.type === 'string') {
    onNode(node);
  }
  for (const key of Object.keys(node)) {
    if (key === 'loc' || key === 'start' || key === 'end' || key === 'span') continue;
    const child = node[key];
    if (child && typeof child === 'object') {
      walkAst(child, onNode);
    }
  }
}

function extractStringLiteral(node) {
  if (!node) return null;
  if (node.type === 'Literal' && typeof node.value === 'string') {
    return node.value;
  }
  if (node.type === 'TemplateLiteral' && node.expressions?.length === 0 && node.quasis?.length === 1) {
    return node.quasis[0].value.cooked ?? node.quasis[0].value.raw ?? null;
  }
  return null;
}

function extractRawGlobs(argNode) {
  if (!argNode) return null;
  if (argNode.type === 'ArrayExpression') {
    const out = [];
    for (const el of argNode.elements || []) {
      if (!el) continue;
      const s = extractStringLiteral(el);
      if (s === null) return null;
      out.push(s);
    }
    return out;
  }
  const single = extractStringLiteral(argNode);
  return single === null ? null : [single];
}

function evalStaticAstNode(node) {
  if (!node) return INVALID_AST_VAL;
  if (node.type === 'Literal') return node.value;
  if (node.type === 'Identifier' && node.name === 'undefined') return undefined;
  if (node.type === 'TemplateLiteral' && node.expressions?.length === 0 && node.quasis?.length === 1) {
    return node.quasis[0].value.cooked ?? node.quasis[0].value.raw;
  }
  if (node.type === 'UnaryExpression' && node.argument?.type === 'Literal') {
    if (node.operator === '!') return !node.argument.value;
    if (node.operator === '-') return -node.argument.value;
    if (node.operator === '+') return +node.argument.value;
  }
  if (node.type === 'ObjectExpression') {
    const obj = {};
    for (const prop of node.properties || []) {
      if (prop.type !== 'Property' || prop.computed || prop.kind !== 'init') {
        return INVALID_AST_VAL;
      }
      const key =
        prop.key?.type === 'Identifier'
          ? prop.key.name
          : prop.key?.type === 'Literal'
            ? String(prop.key.value)
            : null;
      if (key === null) return INVALID_AST_VAL;
      const val = evalStaticAstNode(prop.value);
      if (val === INVALID_AST_VAL) return INVALID_AST_VAL;
      obj[key] = val;
    }
    return obj;
  }
  return INVALID_AST_VAL;
}

function collectGlobCalls(program) {
  const calls = [];
  walkAst(program, (node) => {
    if (node.type !== 'CallExpression' || !isImportMetaGlobCallee(node.callee)) {
      return;
    }
    if (!node.arguments || node.arguments.length < 1 || node.arguments.length > 2) {
      return;
    }
    const globs = extractRawGlobs(node.arguments[0]);
    if (!globs || globs.length === 0) {
      return;
    }
    let options = {};
    if (node.arguments[1]) {
      const evaluated = evalStaticAstNode(node.arguments[1]);
      if (evaluated && typeof evaluated === 'object' && !Array.isArray(evaluated)) {
        options = evaluated;
      }
    }
    calls.push({ globs, options });
  });
  return calls;
}

function staticBaseDir(absGlob, fallbackBase) {
  let metaIdx = -1;
  for (let i = 0; i < absGlob.length; i++) {
    const ch = absGlob[i];
    if (ch === '*' || ch === '?' || ch === '[' || ch === '{') {
      metaIdx = i;
      break;
    }
  }
  if (metaIdx === -1) {
    return absGlob.endsWith('/') ? absGlob.slice(0, -1) || '/' : path.posix.dirname(absGlob);
  }
  const prefix = absGlob.slice(0, metaIdx);
  if (!prefix) {
    return fallbackBase;
  }
  if (prefix.endsWith('/')) {
    return prefix.length > 1 ? prefix.slice(0, -1) : '/';
  }
  return path.posix.dirname(prefix);
}

async function resolveGlobPattern(rawGlob, rootPosix, importerPosix, resolveFn, base) {
  const isNegated = rawGlob.startsWith('!');
  const pat = isNegated ? rawGlob.slice(1) : rawGlob;
  const importerDir = path.posix.dirname(importerPosix);
  let dir = importerDir;
  if (typeof base === 'string' && base.length > 0) {
    dir = base.startsWith('/') ? path.posix.join(rootPosix, base) : path.posix.resolve(importerDir, base);
  }
  let resolved;
  if (pat.startsWith('/')) {
    resolved = path.posix.join(rootPosix, pat.slice(1));
  } else if (pat.startsWith('./')) {
    resolved = path.posix.join(dir, pat.slice(2));
  } else if (pat.startsWith('../')) {
    resolved = path.posix.join(dir, pat);
  } else if (pat.startsWith('**')) {
    resolved = pat;
  } else {
    const isSubImportsPattern = pat.startsWith('#') && pat.includes('*');
    const customRes = await resolveFn(pat, {
      custom: { 'vite:import-glob': { isSubImportsPattern } },
    });
    const candidate = toPosix(customRes || pat);
    if (!path.posix.isAbsolute(candidate)) {
      return null;
    }
    resolved = candidate;
  }
  const fallbackBase = typeof base === 'string' && base.length > 0 ? dir : rootPosix;
  return {
    isNegated,
    raw: rawGlob,
    resolved,
    staticBase: isNegated ? '' : resolveWithMissingAncestor(staticBaseDir(resolved, fallbackBase)),
  };
}

function normalizeQueryOption(options) {
  if (typeof options.query === 'string' && options.query.length > 0) {
    return options.query.startsWith('?') ? options.query : `?${options.query}`;
  }
  if (options.query && typeof options.query === 'object') {
    const keys = Object.keys(options.query);
    if (keys.length > 0) {
      const sp = new URLSearchParams();
      for (const k of keys.sort()) {
        sp.set(k, String(options.query[k]));
      }
      return `?${sp.toString()}`;
    }
  }
  if (typeof options.as === 'string' && options.as.length > 0) {
    return options.as.startsWith('?') ? options.as : `?${options.as}`;
  }
  return '';
}

async function buildGlobRecord(call, viteRoot, importer, resolveFn) {
  const rootPosix = toPosix(viteRoot);
  const importerPosix = toPosix(importer);
  const base = typeof call.options.base === 'string' ? call.options.base : undefined;
  const affirmed = [];
  const negated = [];
  for (const g of call.globs) {
    const resolved = await resolveGlobPattern(g, rootPosix, importerPosix, resolveFn, base);
    if (!resolved) continue;
    if (resolved.isNegated) {
      negated.push(resolved.resolved);
    } else {
      affirmed.push({
        glob: resolved.raw,
        resolved: resolved.resolved,
        staticBase: resolved.staticBase,
      });
    }
  }
  if (affirmed.length === 0) {
    return null;
  }
  const query = normalizeQueryOption(call.options);
  return {
    importer,
    affirmed,
    negated,
    exhaustive: Boolean(call.options.exhaustive),
    hasQuery: query.length > 0,
    query,
  };
}

// `import './x'` (no bindings) is imported for its side effects alone: nothing of x is read, so
// V8 coverage may show no executed function in it even though the test depends on it.
function collectBareImports(program) {
  const specs = [];
  for (const node of program.body || []) {
    if (
      node.type === 'ImportDeclaration' &&
      node.importKind !== 'type' &&
      (node.specifiers || []).length === 0 &&
      typeof node.source?.value === 'string'
    ) {
      specs.push(node.source.value);
    }
  }
  return specs;
}

// vi.mock(path) / vi.doMock(path) without a factory: Vitest uses a __mocks__ file if one
// exists and automocks otherwise, so that file's existence is an input.
function collectFactorylessMocks(program) {
  const specs = [];
  walkAst(program, (node) => {
    const callee = node.type === 'CallExpression' ? node.callee : null;
    if (
      callee?.type !== 'MemberExpression' ||
      callee.computed ||
      callee.object?.type !== 'Identifier' ||
      !MOCK_OBJECTS.has(callee.object.name) ||
      !MOCK_METHODS.has(callee.property?.name)
    ) {
      return;
    }
    const [target, factory] = node.arguments || [];
    if (factory?.type === 'ArrowFunctionExpression' || factory?.type === 'FunctionExpression') {
      return;
    }
    const spec =
      extractStringLiteral(target) ??
      (target?.type === 'ImportExpression' ? extractStringLiteral(target.source) : null);
    if (spec) specs.push(spec);
  });
  return specs;
}

function isProjectFile(id) {
  return Boolean(id) && path.isAbsolute(id) && !id.includes('/node_modules/') && !id.includes('\\node_modules\\');
}

// Mirrors Vitest's findMockRedirect: a local module is redirected to <dir>/__mocks__/<file>;
// a package or builtin to a match inside <root>/__mocks__/<dir of spec>.
function mockCandidatePath(spec, resolvedId, viteRoot) {
  if (isProjectFile(resolvedId) && fs.existsSync(resolvedId)) {
    return path.join(path.dirname(resolvedId), MOCKS_DIR, path.basename(resolvedId));
  }
  return path.join(viteRoot, MOCKS_DIR, path.dirname(spec));
}

async function buildModuleFacts(program, viteRoot, resolveId) {
  const bareImports = [];
  for (const spec of collectBareImports(program)) {
    const resolved = stripQueryAndHash(await resolveId(spec));
    if (isProjectFile(resolved)) bareImports.push(realpathSafe(resolved));
  }
  const mockCandidates = [];
  for (const spec of collectFactorylessMocks(program)) {
    const resolved = stripQueryAndHash(await resolveId(spec));
    mockCandidates.push(resolveWithMissingAncestor(mockCandidatePath(spec, resolved, viteRoot)));
  }
  return bareImports.length > 0 || mockCandidates.length > 0 ? { bareImports, mockCandidates } : null;
}

function createImportGlobPlugin(defaultRoot) {
  const globMap = globalThis[GLOB_KEY] instanceof Map ? globalThis[GLOB_KEY] : new Map();
  globalThis[GLOB_KEY] = globMap;
  const factsMap = globalThis[FACTS_KEY] instanceof Map ? globalThis[FACTS_KEY] : new Map();
  globalThis[FACTS_KEY] = factsMap;
  let viteRoot = realpathSafe(defaultRoot);

  return {
    name: 'testcache:import-glob',
    enforce: 'pre',
    configResolved(resolved) {
      if (resolved?.root) {
        viteRoot = realpathSafe(resolved.root);
      }
    },
    transform: {
      filter: { code: TRACKED_SYNTAX },
      async handler(code, id) {
        if (!code || !TRACKED_SYNTAX.test(code)) {
          return null;
        }
        const cleanId = stripQueryAndHash(id);
        if (!cleanId || cleanId.startsWith('\0') || cleanId.startsWith('virtual:') || !path.isAbsolute(cleanId)) {
          return null;
        }
        if (cleanId.includes('/node_modules/') || cleanId.includes('\\node_modules\\')) {
          return null;
        }
        const importer = realpathSafe(cleanId);
        const program = await parseSourceProgram(cleanId, code);
        if (!program) {
          return null;
        }
        const resolveFn = (im, opt) => this.resolve(im, id, opt).then((r) => r?.id || im);
        const facts = await buildModuleFacts(program, viteRoot, resolveFn);
        if (facts) {
          factsMap.set(importer, facts);
        }
        const records = [];
        for (const call of collectGlobCalls(program)) {
          const rec = await buildGlobRecord(call, viteRoot, importer, resolveFn);
          if (rec) records.push(rec);
        }
        if (records.length > 0) {
          globMap.set(importer, records);
        }
        return null;
      },
    },
  };
}

// What the project config read from the environment, shared with the loadEnv stand-in below.
// `quiet` is above zero while Vite's own loadEnv runs: it copies the whole environment, and
// what counts is what the config then takes from its result.
const envUse = { keys: new Set(), quiet: 0, trackLoadEnv: null };
globalThis[Symbol.for(ENV_USE_KEY)] = envUse;

function recordEnvKey(key) {
  if (typeof key === 'string' && !key.startsWith('TESTCACHE_') && key !== TC_LABEL_ENV) envUse.keys.add(key);
}

// Runs fn with process.env wrapped so every key it reads by name is recorded.
async function recordEnvReads(fn) {
  const rawEnv = process.env;
  const noteKey = (key) => {
    if (envUse.quiet === 0) recordEnvKey(key);
  };
  const proxy = new Proxy(rawEnv, {
    get(target, key) {
      noteKey(key);
      return Reflect.get(target, key, target);
    },
    has(target, key) {
      noteKey(key);
      return Reflect.has(target, key);
    },
  });
  const setEnv = (value) =>
    Object.defineProperty(process, 'env', { value, configurable: true, writable: true, enumerable: true });
  setEnv(proxy);
  try {
    return await fn();
  } finally {
    setEnv(rawEnv);
  }
}

// Vite expands `$VAR` in .env files against the whole environment, so a value loadEnv returns
// may depend on a variable the config never names. True when that cannot be ruled out.
function envFilesMayExpand(mode, envDir) {
  if (envDir === false) return false;
  if (typeof envDir !== 'string' || typeof mode !== 'string') return true;
  for (const name of ['.env', '.env.local', `.env.${mode}`, `.env.${mode}.local`]) {
    try {
      if (fs.readFileSync(path.join(envDir, name), 'utf8').includes('$')) return true;
    } catch (err) {
      if (err?.code !== 'ENOENT' && err?.code !== 'ENOTDIR' && err?.code !== 'EISDIR') return true;
    }
  }
  return false;
}

// loadEnv reads every environment variable, whatever the prefix, so recording its reads makes
// the whole environment (session ids, shell counters, ...) an input of every test. Instead the
// result is handed back with one accessor per key: a key is recorded when the config, or a
// plugin later in the run, actually reads it. Copying the object whole (`{ ...env }`,
// `test.env`) reads every key and records them all. The result stays a plain object, so it
// can still be cloned to the workers.
function trackLoadEnv(realLoadEnv, args) {
  if (envFilesMayExpand(args[0], args[1])) return realLoadEnv(...args);
  envUse.quiet++;
  let loaded;
  try {
    loaded = realLoadEnv(...args);
  } finally {
    envUse.quiet--;
  }
  if (!loaded || typeof loaded !== 'object') return loaded;
  const tracked = {};
  for (const key of Object.keys(loaded)) {
    let value = loaded[key];
    Object.defineProperty(tracked, key, {
      enumerable: true,
      configurable: true,
      get() {
        recordEnvKey(key);
        return value;
      },
      set(next) {
        value = next;
      },
    });
  }
  return tracked;
}
envUse.trackLoadEnv = trackLoadEnv;

function buildViteStandIn(realViteURL) {
  const real = JSON.stringify(realViteURL);
  const source = [
    `export * from ${real};`,
    'default' in viteMod ? `export { default } from ${real};` : '',
    `import { loadEnv as realLoadEnv } from ${real};`,
    `const use = globalThis[Symbol.for(${JSON.stringify(ENV_USE_KEY)})];`,
    'export function loadEnv(...args) {',
    '  return use && use.trackLoadEnv ? use.trackLoadEnv(realLoadEnv, args) : realLoadEnv(...args);',
    '}',
  ].join('\n');
  return `${DATA_URL_PREFIX}text/javascript,${encodeURIComponent(source)}`;
}

// While the project config loads, its `import ... from 'vite'` resolves to a stand-in that
// re-exports Vite and wraps loadEnv. Returns a function that removes the redirection; on a
// Node without module.registerHooks, or any surprise, nothing is redirected and every read
// of loadEnv is recorded as before.
function redirectConfigViteImport() {
  const noop = () => {};
  if (typeof nodeModule.registerHooks !== 'function' || !viteEntry.startsWith(FILE_URL_PREFIX)) return noop;
  try {
    const realVitePath = realpathSafe(fileURLToPath(viteEntry));
    const realViteURL = pathToFileURL(realVitePath).href;
    // <package>/dist/node/index.js
    const vitePackageURL = pathToFileURL(path.resolve(realVitePath, '..', '..', '..')).href + '/';
    const standInURL = buildViteStandIn(realViteURL);
    const hooks = nodeModule.registerHooks({
      resolve(specifier, context, nextResolve) {
        const resolved = nextResolve(specifier, context);
        const parent = context?.parentURL || '';
        const fromImport = Array.isArray(context?.conditions) && context.conditions.includes('import');
        if (
          resolved?.url === realViteURL &&
          fromImport &&
          parent.startsWith(FILE_URL_PREFIX) &&
          !parent.startsWith(vitePackageURL)
        ) {
          return { ...resolved, url: standInURL, format: 'module', shortCircuit: true };
        }
        return resolved;
      },
    });
    return () => hooks.deregister();
  } catch {
    return noop;
  }
}

// Vitest's main process leads its own process group; if tc dies without stopping it (SIGKILL,
// crash), take the whole group down instead of running on as an orphan.
function exitWhenParentDies() {
  const parentPid = process.ppid;
  const timer = setInterval(() => {
    if (process.ppid === parentPid) return;
    try {
      process.kill(-process.pid, 'SIGKILL');
    } catch {
      process.exit(1);
    }
  }, PARENT_POLL_MS);
  timer.unref();
}

if (process.env.TESTCACHE_REPORT_FILE) {
  exitWhenParentDies();
}

const baseConfigPath = process.env.TESTCACHE_BASE_CONFIG || undefined;
const restoreViteImport = redirectConfigViteImport();
let loaded;
try {
  loaded = await recordEnvReads(() =>
    loadConfigFromFile(
      { command: 'serve', mode: process.env.TESTCACHE_VITE_MODE || 'test' },
      baseConfigPath,
      projectRoot,
    ),
  );
} finally {
  restoreViteImport();
}
// Read by tc-reporter.mjs when the run ends: what the config load consumed in this (main)
// process, plus the loadEnv keys read after it (inside plugin hooks).
globalThis[CONFIG_LOAD_KEY] = {
  dependencies: loaded?.dependencies || [],
  get envKeys() {
    return Array.from(envUse.keys).sort();
  },
};
const base = loaded ? loaded.config : {};
const baseTest = base.test ? { ...base.test } : {};
const existingPlugins = Array.isArray(base.plugins) ? base.plugins : base.plugins ? [base.plugins] : [];

const existingSetup = Array.isArray(baseTest.setupFiles)
  ? baseTest.setupFiles
  : baseTest.setupFiles
    ? [baseTest.setupFiles]
    : [];

// Vitest can keep transformed modules on disk and reuse them in later runs (on by default
// from Vitest 5, `experimental.fsModuleCache` before). A module served from that cache never
// reaches a plugin's transform hook, which is where tc reads import.meta.glob calls, bare
// imports and factory-less vi.mock calls: the run would pass with those inputs unrecorded.
// It is switched off for the runs tc starts; tc-reporter.mjs refuses to cache if it is
// still on (a CLI flag wins over this file).
function disableModuleCache(testConfig) {
  const major = Number.parseInt(process.env.TESTCACHE_VITEST_VERSION || '', 10);
  if (Number.isNaN(major) || major >= 5) {
    testConfig.fsModuleCache = false;
  }
  if (testConfig.experimental?.fsModuleCache) {
    testConfig.experimental = { ...testConfig.experimental, fsModuleCache: false };
  }
}
disableModuleCache(baseTest);

// `vitest related <sources> --run`: tc passes only the files without a cached pass, and
// Vitest keeps those that import a source. Like the subcommand, finding none is not a failure.
if (process.env.TESTCACHE_RELATED) {
  baseTest.related = JSON.parse(process.env.TESTCACHE_RELATED);
  baseTest.passWithNoTests = true;
}

const setupFile = process.env.TESTCACHE_SETUP_FILE;
if (setupFile) {
  baseTest.setupFiles = [setupFile, ...existingSetup];
}

if (process.env.TESTCACHE_PASS_REPORTER_CLI !== '1') {
  const existingReporters = Array.isArray(baseTest.reporters)
    ? baseTest.reporters
    : baseTest.reporters
      ? [baseTest.reporters]
      : ['default'];
  const reporterFile = process.env.TESTCACHE_REPORTER_FILE;
  if (reporterFile) {
    baseTest.reporters = [...existingReporters, reporterFile];
  }
}

export default defineConfig({
  ...base,
  plugins: [createImportGlobPlugin(projectRoot), ...existingPlugins],
  test: baseTest,
});
