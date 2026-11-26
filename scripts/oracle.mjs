import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline';
import { fileURLToPath, pathToFileURL } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..');
const fixtureNodeModules = path.join(repoRoot, 'testdata', 'fixture-vitest', 'node_modules');
const JS_EXTS = new Set(['.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx', '.mts', '.cts']);
const CSS_EXTS = new Set(['.css']);
const SKIP_DIRS = new Set(['node_modules', '.git', 'dist', 'build', '.next', 'coverage', '.turbo', '.cache']);

function parseCliArgs(argv) {
  const corpora = [];
  let hasherBin = '';
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '--corpus' && i + 1 < argv.length) {
      corpora.push(argv[++i]);
    } else if (arg === '--hasher' && i + 1 < argv.length) {
      hasherBin = argv[++i];
    } else if (!arg.startsWith('-')) {
      corpora.push(arg);
    }
  }
  if (corpora.length === 0) {
    corpora.push(path.join(repoRoot, 'testdata', 'fixture-vitest'));
    corpora.push(path.join(repoRoot, 'testdata', 'oracle-corpus'));
  }
  return { corpora, hasherBin };
}

function walkCorpusFiles(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith('.tmp-')) continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!SKIP_DIRS.has(entry.name)) walkCorpusFiles(full, out);
      continue;
    }
    const ext = path.extname(entry.name).toLowerCase();
    if (JS_EXTS.has(ext) || CSS_EXTS.has(ext)) out.push(full);
  }
  return out;
}

class GoHashClient {
  constructor(binPath) {
    this.proc = spawn(binPath, ['-test.run=^TestOracleHelper$'], {
      env: { ...process.env, TC_ORACLE_HELPER: '1' },
      stdio: ['pipe', 'pipe', 'inherit'],
    });
    this.rl = readline.createInterface({ input: this.proc.stdout });
    this.pending = [];
    this.rl.on('line', (line) => {
      const resolve = this.pending.shift();
      if (resolve) resolve(JSON.parse(line));
    });
  }
  hash(filePath, content) {
    return new Promise((resolve, reject) => {
      this.pending.push(resolve);
      const payload = JSON.stringify({ path: filePath, contentBase64: Buffer.from(content, 'utf8').toString('base64') });
      this.proc.stdin.write(payload + '\n', (err) => {
        if (err) reject(err);
      });
    });
  }
  close() {
    this.rl.close();
    this.proc.stdin.end();
    this.proc.kill();
  }
}

function collectAstRanges(Visitor, program) {
  const noWsRanges = [];
  let firstStr = null;
  let firstIdent = null;
  const visitor = new Visitor({
    TemplateLiteral(node) {
      noWsRanges.push([node.start, node.end]);
    },
    Literal(node) {
      if (typeof node.value === 'string') {
        noWsRanges.push([node.start, node.end]);
        if (!firstStr && node.end - node.start >= 2) firstStr = node;
      }
    },
    Identifier(node) {
      if (!firstIdent && node.end > node.start && /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(node.name)) {
        firstIdent = node;
      }
    },
  });
  visitor.visit(program);
  return { noWsRanges, firstStr, firstIdent };
}

function isInsideRanges(pos, ranges) {
  return ranges.some(([s, e]) => pos >= s && pos < e);
}

// CSS keeps the conservative rule: any '@' or '#'.
function isMeaningfulCommentText(val) {
  return val.startsWith('!') || val.includes('@') || val.includes('#');
}

// Mirrors docTags and hashDirectives in internal/fingerprint/comment_pragma.go.
const DOC_TAGS = new Set(('param arg argument returns return type typedef template typeparam property prop callback ' +
  'example deprecated see link linkcode linkplain throws exception description desc summary remarks default ' +
  'defaultvalue since version author todo private public protected internal readonly override abstract async ' +
  'constant const enum function func method class constructor extends augments implements interface member ' +
  'memberof module namespace name inheritdoc ignore kind static this yields yield fires emits event listens ' +
  'requires file fileoverview overview global inner instance access alias satisfies import category beta alpha ' +
  'experimental sealed virtual copyright ts-check ts-nocheck ts-ignore ts-expect-error').split(' '));
const HASH_DIRECTIVES = ['__', ' sourceMappingURL', 'sourceMappingURL', ' sourceURL', 'sourceURL'];

function isMeaningfulJsCommentText(val) {
  if (val.startsWith('!')) return true;
  for (let i = 0; i < val.length; i++) {
    if (val[i] === '@') {
      if (i > 0 && /[A-Za-z0-9_-]/.test(val[i - 1])) continue;
      let j = i + 1;
      while (j < val.length && /[A-Za-z0-9_-]/.test(val[j])) j++;
      if (j === i + 1 || !DOC_TAGS.has(val.slice(i + 1, j).toLowerCase())) return true;
      i = j - 1;
    } else if (val[i] === '#' && HASH_DIRECTIVES.some((d) => val.startsWith(d, i + 1))) {
      return true;
    }
  }
  return false;
}

// A {/* ... */} child on its own line after every JSX text that holds a line break.
function insertJsxLineComments(src, program, Visitor) {
  const at = [];
  new Visitor({
    JSXText(node) {
      const nl = src.slice(node.start, node.end).indexOf('\n');
      if (nl >= 0) at.push(node.start + nl + 1);
    },
  }).visit(program);
  if (at.length === 0) return null;
  let out = src;
  for (const pos of at.sort((a, b) => b - a)) {
    out = `${out.slice(0, pos)}{/* oracle-jsx */}\n${out.slice(pos)}`;
  }
  return out;
}

function removeNormalComments(src, comments) {
  const normal = comments.filter((c) => !isMeaningfulJsCommentText(c.value)).sort((a, b) => a.start - b.start);
  if (normal.length === 0) return null;
  let out = '';
  let prev = 0;
  for (const c of normal) {
    if (c.start < prev) continue;
    out += src.slice(prev, c.start);
    out += c.value.includes('\n') || c.value.includes('\r') ? '\n' : ' ';
    prev = c.end;
  }
  return out + src.slice(prev);
}

function insertSafeComments(src, program) {
  const body = Array.isArray(program?.body) ? program.body : [];
  const headPrefix = src.startsWith('#!') ? src.slice(0, src.indexOf('\n') + 1) : '';
  const rest = src.slice(headPrefix.length);
  if (body.length === 0) return `${headPrefix}/* oracle-head */\n${rest}\n// oracle-tail\n`;
  const firstStart = Math.max(headPrefix.length, body[0].start);
  return `${src.slice(0, firstStart)}/* oracle-head */ ${src.slice(firstStart)}\n// oracle-tail\n`;
}

function mutateMultilineWhitespace(src, noWsRanges) {
  let out = '';
  let changed = false;
  for (let i = 0; i < src.length; i++) {
    if (src[i] === '\n' && !isInsideRanges(i, noWsRanges)) {
      out += '\n  \n';
      changed = true;
    } else {
      out += src[i];
    }
  }
  return changed ? out : null;
}

function mutateOneString(src, node) {
  if (!node || node.end - node.start < 2) return null;
  const inner = src.slice(node.start + 1, node.end - 1);
  const replacement = inner.length === 0 ? 'x' : (inner[0] === 'x' ? 'y' : 'x') + inner.slice(1);
  return src.slice(0, node.start + 1) + replacement + src.slice(node.end - 1);
}

function mutateOneIdent(src, node) {
  if (!node) return null;
  return src.slice(0, node.end) + '_ov' + src.slice(node.end);
}

function generateJsVariants(src, parsed, Visitor) {
  const variants = [{ name: 'orig', code: src }];
  const { noWsRanges, firstStr, firstIdent } = collectAstRanges(Visitor, parsed.program);
  const noComments = removeNormalComments(src, parsed.comments || []);
  if (noComments !== null) variants.push({ name: 'rm_comments', code: noComments });
  variants.push({ name: 'ins_comments', code: insertSafeComments(src, parsed.program) });
  const jsxComments = insertJsxLineComments(src, parsed.program, Visitor);
  if (jsxComments !== null) variants.push({ name: 'ins_jsx_comments', code: jsxComments });
  const wsMut = mutateMultilineWhitespace(src, noWsRanges);
  if (wsMut !== null) variants.push({ name: 'ws_multiline', code: wsMut });
  const strMut = mutateOneString(src, firstStr);
  if (strMut !== null) variants.push({ name: 'mut_string', code: strMut });
  const idMut = mutateOneIdent(src, firstIdent);
  if (idMut !== null) variants.push({ name: 'mut_ident', code: idMut });
  return variants;
}

function generateCssVariants(src) {
  const variants = [
    { name: 'orig', code: src },
    { name: 'ins_comments', code: `/* oracle-css-head */\n${src}\n/* oracle-css-tail */` },
  ];
  const noComments = src.replace(/\/\*[\s\S]*?\*\//g, (m) => {
    const body = m.slice(2, -2);
    if (isMeaningfulCommentText(body)) return m;
    return body.includes('\n') ? '\n' : ' ';
  });
  if (noComments !== src) variants.push({ name: 'rm_comments', code: noComments });
  if (src.includes('\n')) variants.push({ name: 'ws_multiline', code: src.replace(/\n/g, '\n  \n') });
  return variants;
}

async function transformNormalizedJs(vite, rolldownUtils, filename, code) {
  try {
    // JSX compiled the way it runs: whitespace with a line break between children is not text.
    const res = await vite.transformWithOxc(code, filename, {
      sourcemap: false,
      jsx: { runtime: 'automatic', development: false },
    });
    const jsName = filename.replace(/\.(tsx?|mts|cts)$/i, '.js');
    const min = rolldownUtils.minifySync(jsName, res.code, {
      compress: false,
      mangle: false,
      codegen: { removeWhitespace: false },
    });
    if (min.errors && min.errors.length > 0) return { ok: false, err: String(min.errors[0]) };
    return { ok: true, code: min.code };
  } catch (err) {
    return { ok: false, err: err?.message || String(err) };
  }
}

function transformNormalizedCss(lightningcss, filename, code) {
  try {
    const res = lightningcss.transform({ filename, code: Buffer.from(code, 'utf8'), minify: true });
    return { ok: true, code: res.code.toString('utf8') };
  } catch (err) {
    return { ok: false, err: err?.message || String(err) };
  }
}

async function main() {
  const { corpora, hasherBin } = parseCliArgs(process.argv.slice(2));
  const vite = await import(pathToFileURL(path.join(fixtureNodeModules, 'vite', 'dist', 'node', 'index.js')).href);
  const rolldownUtils = await import(pathToFileURL(path.join(fixtureNodeModules, 'rolldown', 'dist', 'utils-index.mjs')).href);
  const lightningcss = await import(pathToFileURL(path.join(fixtureNodeModules, 'lightningcss', 'node', 'index.mjs')).href);

  let builtTmpDir = '';
  let bin = hasherBin;
  if (!bin) {
    builtTmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'tc-oracle-'));
    bin = path.join(builtTmpDir, 'fingerprint.test');
    execFileSync('go', ['test', '-c', '-o', bin, './internal/fingerprint'], { cwd: repoRoot, stdio: 'inherit' });
  }
  const hasher = new GoHashClient(bin);
  const stats = { files: 0, skippedParse: 0, variants: 0, equalHashPairs: 0, violations: 0 };

  try {
    const files = corpora.flatMap((dir) => walkCorpusFiles(path.resolve(dir)));
    for (const file of files) {
      const src = fs.readFileSync(file, 'utf8');
      const ext = path.extname(file).toLowerCase();
      const isCss = CSS_EXTS.has(ext);
      const baseTx = isCss ? transformNormalizedCss(lightningcss, file, src) : await transformNormalizedJs(vite, rolldownUtils, file, src);
      if (!baseTx.ok) {
        stats.skippedParse++;
        continue;
      }
      stats.files++;
      const variants = isCss ? generateCssVariants(src) : generateJsVariants(src, rolldownUtils.parseSync(file, src), rolldownUtils.Visitor);
      const evaluated = [];
      for (const v of variants) {
        stats.variants++;
        const h = await hasher.hash(file, v.code);
        const tx = v.name === 'orig' ? baseTx : (isCss ? transformNormalizedCss(lightningcss, file, v.code) : await transformNormalizedJs(vite, rolldownUtils, file, v.code));
        evaluated.push({ ...v, hash: h.hash, normOK: h.ok, tx });
      }
      for (let i = 0; i < evaluated.length; i++) {
        for (let j = i + 1; j < evaluated.length; j++) {
          const a = evaluated[i];
          const b = evaluated[j];
          if (a.hash !== b.hash) continue;
          stats.equalHashPairs++;
          if (!a.tx.ok || !b.tx.ok || a.tx.code !== b.tx.code) {
            stats.violations++;
            console.error(`VIOLATION ${file}: ${a.name} vs ${b.name} had equal hash ${a.hash} but different transform outputs`);
          }
        }
      }
    }
  } finally {
    hasher.close();
    if (builtTmpDir) fs.rmSync(builtTmpDir, { recursive: true, force: true });
  }

  console.log(`oracle: files=${stats.files} skipped_parse=${stats.skippedParse} variants=${stats.variants} equal_hash_pairs=${stats.equalHashPairs} violations=${stats.violations}`);
  if (stats.violations > 0) process.exit(1);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
