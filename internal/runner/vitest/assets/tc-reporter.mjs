import fs from 'node:fs';
import path from 'node:path';

function realpathSafe(p) {
  try {
    return fs.realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

function cleanModuleFile(raw) {
  if (!raw) return null;
  let p = raw;
  const qIdx = p.indexOf('?');
  if (qIdx !== -1) p = p.slice(0, qIdx);
  const hIdx = p.indexOf('#');
  if (hIdx !== -1) p = p.slice(0, hIdx);
  if (!path.isAbsolute(p)) return null;
  return realpathSafe(p);
}

function collectTestCounts(node, counts) {
  if (!node) return;
  if (node.type === 'test') {
    counts.total += 1;
    const state = typeof node.result === 'function' ? node.result()?.state : undefined;
    if (state === 'passed') counts.passed += 1;
    else if (state === 'failed') {
      counts.failed += 1;
      const name = node.fullName || node.name;
      if (name) counts.failedTests.push(name);
    } else if (state === 'skipped') counts.skipped += 1;
    return;
  }
  if (node.children) {
    for (const child of node.children) {
      collectTestCounts(child, counts);
    }
  }
}

function collectTaskIds(node, out) {
  if (!node) return;
  if (node.id) out.add(node.id);
  if (node.children) {
    for (const child of node.children) {
      collectTaskIds(child, out);
    }
  }
}

function collectStaticImports(testModule, repoRoot, runtimeDir) {
  const graph = testModule?.viteEnvironment?.moduleGraph;
  if (!graph || typeof graph.getModuleById !== 'function') {
    return [];
  }
  const rootMod = graph.getModuleById(testModule.moduleId);
  if (!rootMod) {
    return [];
  }
  const testPath = realpathSafe(testModule.moduleId);
  const visited = new Set();
  const queue = [rootMod];
  const deps = new Set();

  while (queue.length > 0) {
    const mod = queue.shift();
    if (!mod || visited.has(mod)) continue;
    visited.add(mod);
    const f = cleanModuleFile(mod.file || mod.id);
    if (
      f &&
      f !== testPath &&
      !f.includes(`${path.sep}node_modules${path.sep}`) &&
      !(runtimeDir && f.startsWith(runtimeDir + path.sep)) &&
      (f === repoRoot || f.startsWith(repoRoot + path.sep))
    ) {
      deps.add(f);
    }
    if (mod.importedModules) {
      for (const imp of mod.importedModules) {
        if (!visited.has(imp)) queue.push(imp);
      }
    }
  }
  return Array.from(deps).sort();
}

function toList(value) {
  if (Array.isArray(value)) return value;
  return value ? [value] : [];
}

// Files and env vars read by the Vite main process, where the worker hooks cannot see them:
// the config's own imports, globalSetup files and what they import, and configured setupFiles.
function collectMainInputs(vitest, runtimeDir) {
  if (!vitest) return undefined;
  const configLoad = globalThis[Symbol.for('testcache.configLoad')];
  if (!configLoad) return undefined;
  const ownSetup = process.env.TESTCACHE_SETUP_FILE ? realpathSafe(process.env.TESTCACHE_SETUP_FILE) : '';
  const raw = [...configLoad.dependencies];
  const configs = [vitest.config, ...toList(vitest.projects).map((project) => project.config)];
  for (const config of configs) {
    raw.push(...toList(config?.globalSetup), ...toList(config?.setupFiles));
  }
  const mainGraph = vitest.vite?.environments?.__vitest__?.moduleGraph;
  for (const mod of mainGraph?.idToModuleMap?.values() || []) {
    raw.push(mod.file || mod.id);
  }
  const files = new Set();
  for (const item of raw) {
    const file = typeof item === 'string' ? cleanModuleFile(item) : null;
    if (
      file &&
      file !== ownSetup &&
      !file.includes(`${path.sep}node_modules${path.sep}`) &&
      !(runtimeDir && file.startsWith(runtimeDir + path.sep))
    ) {
      files.add(file);
    }
  }
  const envDir = vitest.vite?.config?.envDir;
  return {
    files: Array.from(files).sort(),
    envDir: typeof envDir === 'string' ? envDir : '',
    envKeys: configLoad.envKeys,
  };
}

// tc attributes results per test file, which needs one fresh module registry per file and a
// fixed order: a config that gives that up cannot be cached.
function findUncacheableConfig(vitest) {
  const projects = toList(vitest?.projects);
  // One cache entry stands for one test file. With test.projects the same file can run in
  // several projects with different results, and each project has its own config, so a pass
  // in one of them says nothing about the others. Vitest 5 hands the root setupFiles (and
  // with them tc's hooks) to inline projects, so "the hooks did not load" no longer covers it.
  const declared = vitest?.config?.projects ?? vitest?.config?.workspace;
  const hasDeclaredProjects = Array.isArray(declared) ? declared.length > 0 : Boolean(declared);
  if (hasDeclaredProjects || projects.length > 1 || projects.some((project) => project?.config?.name)) {
    return 'vitest config uses test.projects (not supported: results are per project, not per file)';
  }
  const configs = [vitest?.config, ...projects.map((project) => project.config)];
  for (const config of configs) {
    if (config?.browser?.enabled) {
      return 'vitest browser mode is not supported';
    }
    if (config?.fsModuleCache === true || config?.experimental?.fsModuleCache === true) {
      return 'vitest fsModuleCache is on (modules served from it are never seen by tc)';
    }
  }
  for (const config of configs) {
    if (!config) continue;
    const pool = typeof config.pool === 'string' ? config.pool : '';
    if (pool.startsWith('vm')) {
      return `vitest config uses a vm pool (${pool})`;
    }
    if (config.isolate === false || config.poolOptions?.[pool]?.isolate === false) {
      return 'vitest config disables isolation (isolate: false)';
    }
    const shuffle = config.sequence?.shuffle;
    if (shuffle === true || shuffle?.files || shuffle?.tests) {
      return 'vitest config shuffles the test order (sequence.shuffle)';
    }
  }
  return '';
}

function readGlobalMap(name) {
  const map = globalThis[Symbol.for(name)];
  return map instanceof Map ? Object.fromEntries(map.entries()) : undefined;
}

export default class TestCacheReporter {
  constructor() {
    this.logsByTask = new Map();
    this.modules = [];
    this.repoRoot = realpathSafe(process.env.TESTCACHE_REPO_ROOT || process.cwd());
    this.runtimeDir = process.env.TESTCACHE_RUNTIME_DIR ? realpathSafe(process.env.TESTCACHE_RUNTIME_DIR) : '';
    this.reportFile = process.env.TESTCACHE_REPORT_FILE;
  }

  onInit(vitest) {
    this.vitest = vitest;
  }

  onUserConsoleLog(log) {
    if (!log || !log.taskId || !log.content) return;
    const list = this.logsByTask.get(log.taskId) || [];
    list.push({ type: log.type || 'stdout', content: log.content });
    this.logsByTask.set(log.taskId, list);
  }

  onTestModuleEnd(testModule) {
    const filePath = realpathSafe(testModule.moduleId);
    const state = typeof testModule.state === 'function' ? testModule.state() : 'unknown';
    const diag = typeof testModule.diagnostic === 'function' ? testModule.diagnostic() : { duration: 0 };
    const counts = { total: 0, passed: 0, failed: 0, skipped: 0, failedTests: [] };
    collectTestCounts(testModule, counts);

    const taskIds = new Set();
    collectTaskIds(testModule, taskIds);
    const logs = [];
    for (const id of taskIds) {
      const entries = this.logsByTask.get(id);
      if (entries) {
        for (const item of entries) logs.push(item.content);
      }
    }

    const staticImports = collectStaticImports(testModule, this.repoRoot, this.runtimeDir);
    this.modules.push({
      testPath: filePath,
      relativeId: testModule.relativeModuleId || path.basename(filePath),
      passed: state === 'passed',
      state,
      durationMs: Math.round(diag?.duration || 0),
      testsTotal: counts.total,
      testsPassed: counts.passed,
      testsFailed: counts.failed,
      testsSkipped: counts.skipped,
      failedTests: counts.failedTests,
      logs,
      staticImports,
    });
  }

  onTestRunEnd() {
    if (!this.reportFile) return;
    fs.mkdirSync(path.dirname(this.reportFile), { recursive: true });
    const tmp = `${this.reportFile}.${process.pid}.tmp`;
    fs.writeFileSync(
      tmp,
      JSON.stringify({
        modules: this.modules,
        importGlobs: readGlobalMap('testcache.importGlobs'),
        moduleFacts: readGlobalMap('testcache.moduleFacts'),
        mainInputs: collectMainInputs(this.vitest, this.runtimeDir),
        uncacheableReason: findUncacheableConfig(this.vitest),
        nodeVersion: process.version,
      }),
    );
    fs.renameSync(tmp, this.reportFile);
  }
}
