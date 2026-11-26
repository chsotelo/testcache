# testcache (`tc`): a test result cache for Vitest

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**Skip the Vitest test files whose inputs did not change, the way `go test` does.** `tc` caches each passing test file's result on disk, keyed by everything that file actually executed and read, and replays it on the next run without starting Node or Vitest. Edit one module and only the test files that ran it run again; the rest come back from the cache in milliseconds.

- **Per test file, not per task.** Nx and Turborepo cache a whole `test` task: change one file and the whole suite runs again. `tc` keeps every test file whose inputs are unchanged.
- **Coverage-aware, not import-graph based.** Dependencies come from what each file executed at runtime (V8 coverage) plus the files it read, so a barrel file or an unexecuted branch does not invalidate everything that imports it.
- **Zero changes to your repositories.** After a one-time install, `pnpm test`, `npx vitest run`, `pnpm vitest run src/x` and `vitest related --run` go through `tc` in every project, for you and for coding agents.
- **Safe by default.** A file that spawns processes, opens network connections, reads files modified during the run, or runs with `isolate: false` is never cached. A small sample of hits is re-run to catch flaky or under-tracked tests, and `tc verify` re-checks the whole cache before a merge.
- **A single Go binary**, standard library only. Works with Vitest 3.x, 4.x and 5.x, npm, pnpm and yarn.

Example from a real 1,248-file suite: the full run takes about 5 minutes cold; after editing a few modules, the next full run took **19 s**, with 1,208 files replayed from the cache and 40 run.

```text
 Test Files  156 passed (156) [tc total: 144 cached + 12 run]
      Tests  2005 passed (2005) [tc total]
tc: 156 files, 144 cached, 12 run, saved 183.2s
```

## Quick start

Requirements: macOS or Linux (x64 or arm64), zsh or bash, Node, and a project using Vitest 3, 4 or 5.

```sh
curl -fsSL https://raw.githubusercontent.com/chsotelo/testcache/main/scripts/get.sh | sh
# open a new shell, then:
tc doctor   # check the install
```

Then run your tests as usual (`pnpm test`, `npx vitest run`, ...): every project on the machine goes through `tc`, with no change to any repository. `tc log` shows what happened, `tc explain <file>` why a file was not cached, and `TC_DISABLE=1` turns `tc` off for one command.

To install only the binary and call it yourself (`tc vitest run`), without changing your shell or `.npmrc`: `curl ... | sh -s -- --no-integrations`. That is also how to use it in CI (see [In CI](#in-ci)).

On Linux, `tc` is also the name of the iproute2 traffic-control tool (`/usr/sbin/tc`). The integrations call testcache's binary by its full path, so they are not affected; when you type `tc` yourself, keep `~/.local/bin` ahead of `/usr/sbin` in `PATH`.

## How it works

1. **Discover**: `tc` finds the test files Vitest would select (`vitest list` once, then from its own cache, so a full hit never starts Node).
2. **Key**: each file's key hashes the test file, the content of every file it executed or read in its last passing run, the Vitest config, lockfile, Node and Vitest versions, cache-relevant flags and the environment variables the run read.
3. **Replay or run**: files whose key matches a stored pass are replayed; the rest are handed to one Vitest process, with tc's hooks recording what each file executes and touches.
4. **Store**: a file is stored only when it passed and nothing made its result depend on something `tc` cannot track.

The details are in [How cache keys are computed](#how-cache-keys-are-computed), [When a pass is not cached](#when-a-pass-is-not-cached) and [Limits](#limits).

## How it compares

| Tool | What it skips | Decides from | Setup |
| --- | --- | --- | --- |
| `vitest related` / `--changed` | test files that do not import a changed file | static import paths | none |
| Nx, Turborepo | the whole `test` task, only when no input of the package changed | declared task inputs | per workspace |
| [vite-plugin-vitest-cache](https://github.com/raegen/vite-plugin-vitest-cache) | test files whose bundled code is unchanged; Vitest still starts | hash of each test file built by Vite | a plugin in each Vitest config |
| **testcache** | test files whose recorded inputs are unchanged; a full hit never starts Node | files each test executed (V8 coverage) and read, env vars it read, config, lockfile, versions | one install per machine |

They also combine: `tc vitest related --run src/a.ts` replays the related files that are cached and runs the rest.

---

## Install

### Machine-wide

```sh
curl -fsSL https://raw.githubusercontent.com/chsotelo/testcache/main/scripts/get.sh | sh
```

`get.sh` downloads the release archive for your system from GitHub Releases, checks it against the release's `checksums.txt`, and runs the `scripts/install.sh` inside it. Arguments after `sh -s --` go to that installer (`curl ... | sh -s -- --no-integrations`); `TESTCACHE_VERSION=v1.2.3` picks a release instead of the latest.

From a source checkout (needs Go 1.26+):

```sh
git clone https://github.com/chsotelo/testcache.git && cd testcache
./scripts/install.sh
```

The installer uses the prebuilt `bin/tc` of a release archive, or, in a source checkout, builds `./cmd/tc` with `-trimpath` and stamps `main.version` from `git describe --always --dirty` (falling back to `dev` if `git` is unavailable). It checks the binary, installs it to `${PREFIX:-$HOME/.local}/bin/tc` plus the link `tc-shell -> tc`, and keeps a copy of the uninstaller at `<prefix>/lib/testcache/uninstall.sh`.

**Installing activates tc machine-wide.** From then on every Vitest single run goes through `tc`, in every repository, with no repository change, however it is started:

- a package script that is exactly one Vitest single run (`pnpm test`, `npm test`, `pnpm run test`, ... when the script is `vitest run`) is served by the [script shell](#script-shell-for-npm-and-pnpm-tc-shell); every other script runs in `/bin/sh` unchanged;
- any other launch of the Vitest CLI (`pnpm vitest run`, `pnpm exec vitest run`, `npx vitest run`, `yarn vitest run`, `node_modules/.bin/vitest run`, inside `&&` chains, pipes or `timeout`) is handed to `tc` by the [Node preload](#node-preload).

The installer:

- writes a marked block to `~/.zshenv` (or `$ZDOTDIR/.zshenv`), and also to `~/.bashrc` when your login shell is bash, exporting `pnpm_config_script_shell` (pnpm 11 reads the script shell only from the environment) and adding `--require <prefix>/lib/testcache/node-preload.cjs` to `NODE_OPTIONS`, keeping a `.tc-backup` copy;
- sets `script-shell` in the user `.npmrc` (npm, pnpm 9/10), unless another script shell is already configured there, which it leaves alone;
- writes the preload to `<prefix>/lib/testcache/node-preload.cjs` after checking it in a real Node process.

Re-running it is idempotent. Open a new shell after installing, and run `tc doctor` to check the result. Turn tc off for one command or one shell with `TC_DISABLE=1`. `./scripts/install.sh --no-node-preload` activates the script shell only; `--no-integrations` installs the binary only.

Override the destination prefix with `PREFIX`:

```sh
PREFIX=/usr/local ./scripts/install.sh
```

Build manually without installing:

```sh
go build -trimpath -o tc ./cmd/tc
```

Uninstall:

```sh
~/.local/lib/testcache/uninstall.sh                # removes the ~/.zshenv and ~/.bashrc blocks and the .npmrc entry, then tc and tc-shell
~/.local/lib/testcache/uninstall.sh --keep-binary  # deactivates only
```

(`./scripts/uninstall.sh` in a source checkout is the same script.)

The integrations are reverted before the binaries are removed. If `tc-shell` is still referenced somewhere the uninstaller did not write (a line added by hand), it stops and keeps the binaries, because every npm/pnpm script would otherwise fail. The preload file is emptied rather than deleted: a shell opened while tc was active still names it in `NODE_OPTIONS`, and Node refuses to start when a `--require`d file is missing. Delete it once those shells are closed.

### In CI

Install the binary only, and keep the cache directory between jobs with your CI's cache. GitHub Actions:

```yaml
- uses: actions/cache@v4
  with:
    path: ~/.cache/testcache
    key: testcache-${{ runner.os }}-${{ github.sha }}
    restore-keys: testcache-${{ runner.os }}-
- run: curl -fsSL https://raw.githubusercontent.com/chsotelo/testcache/main/scripts/get.sh | sh -s -- --no-integrations
- run: npm ci
- run: ~/.local/bin/tc vitest run
```

Each job restores the newest cache and saves its own under a new key. A fresh clone, a fresh `node_modules` and a new runner do not matter: keys are built from content and repo-relative paths, and the Node binary is identified by its version. The repository is identified by where its checkout lives, so the checkout path must be the same from one job to the next, as it is on GitHub Actions, GitLab CI and CircleCI. `CI=true` is part of every key, so CI and local runs keep separate entries. Pin a release with `TESTCACHE_VERSION=v1.2.3` in the environment of the install step.

---

## Commands

Run Vitest through `tc` from a project directory:

```sh
tc vitest [vitest args] [files...]
tc vitest tests/a.test.ts
tc vitest run -t "adds" tests/
tc vitest related --run src/a.ts           # the tests that import src/a.ts, cached too
tc vitest --no-cache tests/a.test.ts       # bypass cache read/write (like Go's -count=1)
tc vitest --no-revalidate tests/a.test.ts  # disable revalidation sampling for one run
TC_VERBOSE=1 tc vitest                     # print per-phase timings and counts to stderr
```

Cache inspection, verification, maintenance, and metadata commands:

| Command | Description |
| --- | --- |
| `tc vitest [--no-cache] [--no-revalidate] [vitest args] [files...]` | Run Vitest with per-file caching; exit code matches Vitest (`0` on full hit). |
| `tc explain [vitest args] [files...]` | Inspect cache status (`cached` or `not cached (<reason>)`) in pure Go without starting Node. |
| `tc verify [vitest args] [files...]` | Pre-merge safety net: run selected files (default: all discovered) with no cache, compare against cached entries for the current keys, delete and quarantine stale entries, and exit `0` only when there are no stale entries and no failures. |
| `tc quarantine [--clear <file>]` | List quarantined test files (with date and failing test names), or release a file with `--clear <file>`. |
| `tc log [-n <runs>] [--anomalies] [--repo <path>] [--json]` | Show the last runs from the [run log](#run-log-and-anomalies) (default 20) as a table; `--anomalies` keeps the flagged ones, `--repo .` the current repository, `--json` prints the raw lines. |
| `tc doctor` | Check the install: binary, `TC_DISABLE`, script shell, Node preload (file and `NODE_OPTIONS`), failures the preload logged, and the last runs by how they reached `tc`. Exits `1` when something is broken (a configured script shell or preload that does not exist). |
| `tc stats` | Show repo ID, cache directory, cumulative hits/misses, time saved, entry count, and cache size. |
| `tc gc` | Run blocking garbage collection (`maxAgeDays`, `maxCacheBytes` LRU eviction) and clean stale temp/run files. |
| `tc clean [--all]` | Clear cache for the current repository, or all repositories under the cache root with `--all`. |
| `tc --version` (`tc version`, `-v`) | Print the `tc` binary version. |
| `tc help` (`--help`, `-h`) | Print CLI usage. |

### Reading the output

Vitest prints its own summary for the files it executed; `tc` then replays the cached files and closes with totals for the whole selection:

```text
 Test Files  12 passed (12)                      <- Vitest: only what it executed now
      Tests  111 passed (111)
 ✓ src/pages/Users.test.jsx (27 tests) (cached)
 ...
 Test Files  156 passed (156) [tc total: 144 cached + 12 run]
      Tests  2005 passed (2005) [tc total]
tc: 156 files, 144 cached, 12 run, saved 183.2s
tc: "(cached)" = that test file passed before with identical inputs (...) and its result was replayed; nothing was skipped. Real run: add --no-cache (or TC_DISABLE=1). Reason per file: tc explain <file>.
```

The `[tc total]` lines and the last line are there for whoever filters the output with `grep "Test Files"` and has never heard of `tc`, which is usually a coding agent: read alone, Vitest's count looks as if test files had gone missing. They are printed whenever at least one file came from the cache, and left out when the outcome of the executed files is unknown rather than guessed.

### Verbose Diagnostics (`TC_VERBOSE=1`)

Setting `TC_VERBOSE=1` prints one line per phase to `stderr`, prefixed with `tc:`:

```text
tc: [verbose] discover: 1ms (cache, 5 files)
tc: [verbose] hashing: 1ms (5 files)
tc: [verbose] lookup: 0ms (5 hits, 0 misses)
tc: [verbose] run: 0ms (0 files, skipped)
tc: [verbose] persist: 0ms (0 saved, skipped)
tc: [verbose] gc: 0ms (0 removed, skipped)
```

### Explaining Cache Misses (`tc explain`)

`tc explain [files...]` reports why each file is or is not cached without invoking Node, including:
- `first run` / `previous run failed`
- `dependency changed: <path> (<old>→<new>)`
- `new file: <path> (missing→<new>)` / `missing file: <path> (<old>→missing)`
- `test file changed: <path> (<old>→<new>)`
- `env var changed: <KEY> (<old>→<new>)`
- `config/lockfile changed: <path> (<old>→<new>)` / `testcache.json config changed`
- `node version changed` / `vitest version changed` / `vitest flags changed`
- `cache format version changed` / `cache entry missing or evicted` / `dependency record missing or evicted`
- `excluded by vitest config` / `excluded by testcache.json`
- `quarantined: was cached as passing but failed on re-run with identical inputs (likely flaky or depends on an untracked input)`
- why a pass was not stored (see [When a pass is not cached](#when-a-pass-is-not-cached)), e.g. `modified during the run or less than 2s before it: <path>`, `changed during the run: <path>`, `spawned a process: <cmd>`, `network access: <host:port>`, `tc hooks did not load for this file`, `vitest config disables isolation (isolate: false)`, `node version mismatch`

### Revalidation Sampling, Quarantine & `tc verify`

- **Revalidation sampling (`revalidateRate`)**: on every `tc vitest` run, each cache hit is randomly re-run with probability `revalidateRate` (default `0.02`; disable for one run with `--no-revalidate`). Passing re-runs update the cache as usual.
- **Quarantine**: if a re-run of a cached passing entry fails with identical inputs (same full cache key), `tc` deletes the stale cache entry, prints a warning, and records the file in `quarantine.json` with the date and failing test names. Quarantined files always run uncached until released via `tc quarantine --clear <file>`.
- **Pre-merge verification (`tc verify`)**: runs all discovered (or selected) test files uncached, compares each file's result against the cached entry for the same current key, and reports checked files, agreeing cached entries, **stale entries** (cached pass, now fails, same key — deleted and quarantined), and new failures without an entry. `tc verify` exits `0` only when there are no stale entries and no failures.

---

## How Cache Keys Are Computed

`tc` uses a two-level SHA-256 scheme modeled on Go's test cache:

1. **Base key (`baseKey`)** — SHA-256 of:
   - Cache format version (`tc/v11`) and normalization algorithm version (`v2` when `normalizeHash` is enabled)
   - Runner name (`vitest`) and Vitest version (from `node_modules/vitest/package.json`)
   - Node version (`node --version`, cached on disk so hits never spawn `node`). A real binary is identified by `(resolvedNodePath, size, mtime)`. A version-manager shim (volta, asdf, mise: a script, or a binary not named `node`) is one file standing for many versions, so its identity also includes the content of every `.nvmrc`, `.node-version`, `.tool-versions`, `mise.toml`, `.mise.toml` and `package.json` in the project directory and its ancestors, plus `ASDF_NODEJS_VERSION`, `MISE_NODE_VERSION` and `VOLTA_HOME`; `node --version` is run from the project directory. nvm and fnm put the real binary on `PATH`, so a version switch changes the resolved path.
   - Merged `testcache.json` configuration hash (excluding GC bounds `maxCacheBytes` and `maxAgeDays`)
   - **Project files**, looked up in the project directory and every ancestor up to the repo root:
     - `vitest.config.*`, `vite.config.*`, `vitest.workspace.*`
     - `package.json`, `package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lockb`, `bun.lock`
     - the install marker the package manager rewrites whenever `node_modules` changes: `node_modules/.modules.yaml` (pnpm), `node_modules/.package-lock.json` (npm), `node_modules/.yarn-state.yml`, `node_modules/.yarn-integrity`
     - `tsconfig*.json` and `jsconfig.json`, plus every local file reached through their `extends` chain
     - `postcss.config.*`, `.postcssrc*`, `tailwind.config.*`, `babel.config.*`, `.babelrc*`, `.browserslistrc`
     - every file under `patches/`
     - `.env`, `.env.local`, `.env.<mode>`, `.env.<mode>.local` in the project directory and in the config's `envDir`
   - **Main-process inputs**: what the last run saw Vite's main process read, where the worker hooks cannot see it. They are recorded per (project, config file, mode) and hashed into the key from then on:
     - the local files the config imports (transitively)
     - `globalSetup` files and every project file they import
     - configured `setupFiles`
     - the environment variables read by name while the config loads. Vite's `loadEnv` copies the whole environment whatever the prefix, so its result is tracked key by key instead: a variable counts when the config, or one of its plugins later in the run, reads that key from the object `loadEnv` returned. Variables the config never uses (session ids, `SHLVL`, `npm_lifecycle_*`, ...) stay out of the key, so two shells or two agent sessions share cached results
   - Cacheable Vitest CLI flags (presentation and worker flags such as `--reporter`, `--maxWorkers`, `--color`, and `--silent` are excluded)
   - Test file repo-relative path and content hash
   - Allowlisted environment variables: `NODE_ENV`, `CI`, `TZ`, `LANG`, `LC_ALL`, `LC_MESSAGES`, all `VITE_*` and `VITEST_*` variables, any keys listed in `testcache.json` `env`, and the system time zone (the target of `/etc/localtime`, or a hash of the file when it is not a symlink)

2. **Full key (`fullKey`)** — `SHA-256(baseKey, deps, runtimeEnv)` using the dependency record `deps/<baseKey>.json` saved by the test file's last passing run:
   - **Executed files (V8 coverage)**: project JS/TS files actually executed during the test, collected via `node:inspector/promises` (`Profiler.startPreciseCoverage` with `{ callCount: false, detailed: false }`). If V8 coverage is unavailable, `tc` falls back to Vite's static imported module graph and marks the cached output with `[fallback: static import graph]`.
   - **Filesystem reads**: repo-internal paths accessed via `node:fs` or `node:fs/promises` (`readFile*`, `readdir*`, `stat*`/`lstat*`, `exists*`, `access*`, `createReadStream`, `opendir*`). Directories hash by sorted entry names; absent paths are recorded as `"missing"` so creating them later invalidates the entry.
   - **Snapshots**: default `<dir>/__snapshots__/<file>.snap` (present or absent), custom `snapshotPath`, and `toMatchFileSnapshot(...)` targets.
   - **Runtime environment reads**: `process.env` keys whose value was read during test execution (captured via a `Proxy`): `process.env.X`, `'X' in process.env`, a descriptor or `hasOwn` check by name, and every key when the environment is copied (`{ ...process.env }`, `Object.entries`). Listing the environment (`Object.keys(process.env)`, `for...in`, a spread; the `debug` package does it on load) reads no value by itself, but the test now depends on **which variables exist**: the sorted list of names becomes part of its key. A variable that appears or disappears re-runs that test, a changed value of a variable it never read does not, and the result for each set of names is kept, so alternating between two ways of starting Vitest (`pnpm test` adds `npm_lifecycle_*`, a plain binary does not) costs one run each and hits afterwards. `TC_INVOKED_BY`, `TC_NODE_PRELOAD_ACTIVE` and `TC_DISABLE` are tc's own: they are removed from (or constant in) the environment of the Vitest tc starts and never count.
   - **Side-effect imports**: the target of every `import './x'` / `import {} from './x'` (no bindings) in a module the test loaded. Such a module is imported for what its top level does, so coverage may show no executed function in it.
   - **`__mocks__` candidates**: for every `vi.mock(path)` / `vi.doMock(path)` without a factory function in a loaded module, the path Vitest would redirect to if it existed: `<dir>/__mocks__/<file>` for a project module, the directory `<root>/__mocks__/<dir of specifier>` for a package or builtin. Recorded present or missing, so creating the mock invalidates.

   **Comment-insensitive dependency hashing**: when `normalizeHash` is enabled (the default), dependency files and test files with `.js`, `.jsx`, `.mjs`, `.cjs`, `.ts`, `.tsx`, `.mts`, `.cts`, and `.css` extensions are hashed after a Go lexer strips non-meaningful comments and collapses multi-line whitespace runs into single `\n` (preserving single-line spaces, ASI newlines, string/template/regex literals, `url(...)` in CSS, JSX children text, `#!` hashbangs, and meaningful comments containing `@` or `#` or starting with `!` such as `@vitest-environment`, `@vite-ignore`, `#__PURE__`, and `/*! ... */`). In TypeScript files (`.ts`, `.tsx`, `.mts`, `.cts`) the top-level type-only statements are dropped too: `import type ...`, `export type ...`, type aliases and interfaces. They leave nothing in the code that runs, so moving a type between files or editing an interface changes no key. Types inside functions, inline `import { type A }` and annotations are hashed as written, and a statement whose end is unclear is kept. Any lexing error, unterminated literal, or ambiguous JSX context falls back automatically to the raw content hash (logged under `TC_VERBOSE=1`). Other files hash as the SHA-256 of their bytes, cached by `(path, size, mtime)` in `filehashes.json`. For a tracked, unmodified file the digest is found by its git blob id (`git ls-files -s`) in a table kept in the same file, so it is read once per content and not even stat-ed while it stays clean. The same content always gives the same hash: committing a file, or having it clean in one checkout and edited to the same bytes in another, changes no key.

3. **Discovery cache**: on a cold miss, `tc` runs `vitest list --filesOnly --json` and caches the resolved test list under `discovery/<key>.json` keyed by format version, config/project hashes, discovery flags, and candidate paths on disk. Cache hits do not start Node.

---

## When a pass is not cached

`tc` prefers running a test again over replaying a pass it cannot vouch for. A passing file is run but **not stored** when:

- **An input changed during the run, or too close to it.** The test file, every known dependency and every project file are hashed before Vitest starts and again after it exits (git state is re-read in between). The pass is dropped if any of them differs, or if the test file, any dependency or any project file has an mtime later than *run start − 2s*: such a file may have been written after the test read it, and coarse filesystem timestamps cannot tell. Editing a file and running `tc` within two seconds therefore costs one extra run before the result is cached.
- **The test spawned a process or opened a network connection** (`child_process`, `worker_threads`, `net`, `dgram`, `dns`). A TCP or Unix-socket connection counts when it reaches Node's native handle, refused or not. A request answered by a mock does not, including libraries that intercept at the socket (MSW 3 calls `net.Socket.prototype.connect` for every mocked request without opening anything).
- **The config uses `test.projects` or browser mode.** One cache entry stands for one test file, and with projects a file has one result per project, each with its own config. (Vitest 5 passes the root `setupFiles` to inline projects, so tc's hooks do load there; the run is refused all the same. Before that fix a file failing in one project and passing in another was replayed as a pass.)
- **`tc`'s worker hooks did not load for the file**: there is no record of what it read.
- **Vitest's on-disk transform cache is on** (`fsModuleCache`, the default from Vitest 5; `experimental.fsModuleCache` in 4.x). A module served from it never reaches a plugin's transform hook, where tc reads `import.meta.glob` calls, bare imports and factory-less `vi.mock` calls. tc switches it off for the runs it starts, so this only happens when the flag is forced on.
- **The resolved Vitest config gives up per-file attribution**: `isolate: false` (also `--no-isolate`, `--isolate=false`, `poolOptions.<pool>.isolate: false`), a vm pool (`vmThreads`, `vmForks`), or `sequence.shuffle`. The whole project then always runs.
- **The Node version Vitest reported differs from the one in the cache keys.** The stored version is forgotten and resolved again on the next run.
- **Vitest did not report its main-process inputs**, or the run was interrupted.
- The file is quarantined or excluded by `testcache.json`.

`tc explain` prints the reason. When V8 coverage is unavailable but the hooks did load, `tc` still caches, using Vite's static import graph as the dependency list, and marks the entry `[fallback: static import graph]`.

### When `tc` itself fails

A test run must never break because of `tc`. If `tc` panics, the panic is recovered, logged as `internal_error` and the run continues as plain Vitest with the same arguments (`vitest run ...`), so the exit code is Vitest's. If Vitest had already finished when the panic happened, its exit code is kept and nothing runs twice. A run log that cannot be written is a warning, never a failure.

### Stopping `tc`

Vitest runs in its own process group. On `SIGINT` or `SIGTERM`, `tc` sends `SIGTERM` to the whole group, `SIGKILL` one second later, stores nothing and exits `130`. If `tc` itself dies without a chance to do that (`SIGKILL`, a crash), the Vitest main process notices within 250 ms that its parent is gone and kills its own group. Passthrough commands (`--watch`, `--ui`, ...) stay in `tc`'s foreground process group so they keep the terminal.

---

## Run log and anomalies

Every `tc vitest` and `tc verify` invocation appends exactly one JSON line to `~/.cache/testcache/runs.jsonl` (under `TESTCACHE_DIR` when set). A line is written with a single `O_APPEND` write under a kernel `flock`, so concurrent `tc` processes never interleave. When the file is over 10 MiB it is rotated to `runs.jsonl.1` and then `runs.jsonl.2`: three files at most.

```json
{"time":"2026-11-20T15:04:05Z","tc_version":"v0.3.0","command":"vitest","mode":"cache","invoked_by":"script-shell",
 "repo_root":"/work/app","repo_id":"6c0fb44cf78b0a64eefe00726e860218","cwd":"/work/app","argv":["tc","vitest","run"],
 "files":{"discovered":42,"cached":40,"run":2,"uncacheable":0,"quarantined":0},
 "phases_ms":{"discover":2,"hashing":9,"lookup":3,"lock_wait":0,"run":1840,"persist":31,"gc":0},
 "duration_ms":1893,"vitest_exit":0,"tc_exit":0,"saved_s":63.2,"anomalies":[]}
```

| Field | Meaning |
| --- | --- |
| `time` | Start of the run, UTC, RFC 3339. |
| `tc_version`, `command`, `argv`, `cwd` | The binary, `vitest` or `verify`, and how it was called. `argv` is trimmed when a line would exceed 32 KiB. |
| `mode` | `cache` (normal run), `verify`, `passthrough` (watch, UI, `--coverage`, no test files: handed to Vitest), `disabled` (`TC_DISABLE`), `fallback` (plain Vitest after a `tc` panic). |
| `invoked_by` | `direct`, `script-shell`, `node-preload` or `claude-hook`, read from `TC_INVOKED_BY`, which the [integrations](#integrations) set. |
| `repo_root`, `repo_id` | The repository and its cache directory name. |
| `files` | `discovered`: test files selected. `cached`: replayed. `run`: executed. `uncacheable`: passed but not stored (see [When a pass is not cached](#when-a-pass-is-not-cached)). `quarantined`: executed because they are quarantined. |
| `phases_ms` | Milliseconds per phase. `lock_wait` is the time queued behind another `tc`. |
| `duration_ms`, `saved_s` | Total wall time, and the wall time the cached files saved: each file's share of the run that stored it, so files that ran in parallel are not counted twice. |
| `vitest_exit`, `tc_exit` | Exit codes; `vitest_exit` is `null` when Vitest did not run. |
| `anomalies` | List of `{"code","detail"}`, empty for a normal run. |
| `error` | Present only when `tc` itself failed. |

### Anomaly codes

| Code | Raised when |
| --- | --- |
| `slow_run` | The run took more than `max(3 × median, 120 s)`, the median being that of the last 20 successful runs of the same repository and mode that executed a similar number of files (within a factor of two, ± 2 files). Needs at least 5 such runs. |
| `slow_hit_path` | A run executed 0 files and still took more than 5 s, lock waits excluded. |
| `long_lock_wait` | The run queued more than 60 s behind another `tc`. |
| `stale_entry` | Revalidation or `tc verify` found a cached pass that fails with identical inputs (the file is then quarantined). |
| `quarantined` | The run executed files that were already quarantined; they keep running uncached until `tc quarantine --clear <file>`. |
| `interrupted` | The run was stopped by `SIGINT` or `SIGTERM`. |
| `internal_error` | `tc` itself failed: a recovered panic, an unexpected I/O error, a corrupt cache file. Errors caused by how `tc` was called (no Vitest in the project, a missing target file) set `error` without this code. |
| `node_version_mismatch` | Vitest ran on another Node than the one in the cache keys; nothing was cached. |

### `tc log`

```text
$ tc log --repo . -n 3
TIME (UTC)            REPO  VIA           MODE   CACHED/RUN/ALL  DURATION  EXIT  SAVED  ANOMALIES
2026-11-20T15:04:05Z  app   script-shell  cache  40/2/42         1.9s      0     63.2s  -
2026-11-20T15:09:41Z  app   claude-hook   cache  42/0/42         14ms      0     65.0s  -
2026-11-20T15:20:02Z  app   direct        cache  41/1/42         6.3s      1     64.1s  stale_entry
```

`tc log --anomalies --json | jq .` gives the full records, details included.

### Alerts (`alertCommand`)

When a run ends with any anomaly other than `interrupted` and `alertCommand` is configured, `tc` runs it through `/bin/sh -c` with the JSON line on stdin. `tc` has no network code: the command decides where the alert goes.

```json
{ "alertCommand": "curl -fsS -m 8 -d @- https://ntfy.example/tc-alerts" }
```

The command runs in a detached helper process, so it can neither delay nor fail the test run. It is killed after 10 s. If it fails or times out, the next `tc` run prints the reason once on stderr (`tc: alertCommand failed: ...`).

---

## Integrations

None of these change a repository. The script shell and the Node preload are enabled by the installer (see [Install](#install)) and reverted by the uninstaller. The Claude Code hook is not installed by default and is rarely needed now that the preload covers the same commands whatever they look like; add it to `~/.claude/settings.json` yourself if you want it (see below).

### Script shell for npm and pnpm (`tc-shell`)

npm and pnpm run every package script as `<script-shell> -c "<command>"`. With `tc-shell` as the script shell, a script that is exactly one Vitest single run is served by `tc vitest` (logged with `invoked_by: script-shell`); for anything else `tc-shell` replaces itself with `/bin/sh` and the untouched arguments, so stdio, environment, working directory, signals and exit code are the real shell's. A pass-through costs a few milliseconds.

| Rewritten | Passed to `/bin/sh` unchanged |
| --- | --- |
| `vitest run [args]`, `vitest --run [args]` | `vitest`, `vitest watch`, `--watch`, `-w`, `--ui`, `vitest bench`/`list`/`related`/`typecheck` |
| the same after `NAME=value` assignments or `cross-env NAME=value` | compound commands: `&&`, `\|\|`, `;`, pipes, `&`, redirections |
| the same through `npx`, `pnpm exec`, `pnpm`, `yarn` or `./node_modules/.bin/vitest` | anything the shell must expand: `$VAR`, globs, `~`, backticks, `$(...)` |
| | every script when `TC_DISABLE` is set, or when the `vitest` on `PATH` is not the one `tc` resolves for the project |

`pretest`/`posttest` and all other scripts keep working: they go through the same shim and fall in the right-hand column.

| Package manager | How it is configured |
| --- | --- |
| npm, pnpm 9 and 10 | `script-shell` in `~/.npmrc` (`npm config set script-shell ~/.local/bin/tc-shell`) or `npm_config_script_shell` |
| pnpm 11+ | `pnpm_config_script_shell` in the environment, or `scriptShell` in `pnpm-workspace.yaml`. pnpm 11 ignores `~/.npmrc`, `npm_config_script_shell` and the key in its global `config.yaml` |

`pnpm test -- <file>`: pnpm 10 and later keep the `--` in the command (`vitest run -- <file>`) and Vitest ignores everything after it, so all files run, with or without `tc`. Use `pnpm test <file>`.

### Node preload

A package script is only one way to start Vitest. Agents and people also type `pnpm -s vitest run`, `pnpm exec vitest run src/x`, `timeout 300 npx vitest run ...` or `pnpm lint && pnpm vitest run`, none of which goes through the script shell. The preload does not look at commands at all: `NODE_OPTIONS=--require <prefix>/lib/testcache/node-preload.cjs` loads one small file into every Node process of the shell, and when that process is the Vitest CLI it replaces itself with `tc vitest <same arguments>` (`execve`, so the pid, the terminal, signals and the exit code are tc's; on a Node without `process.execve` it starts tc and exits with its status). The run is logged with `invoked_by: node-preload`.

It steps aside, leaving plain Vitest, when:

- `TC_DISABLE` is set (which `tc` does for the Vitest it starts, so nothing loops);
- the process is not the Vitest CLI entry (`.../vitest/vitest.mjs`, also reached through `node_modules/.bin/vitest`), or it is a worker thread;
- `node` was given flags of its own (`node --inspect node_modules/.bin/vitest`): they would be lost;
- a script sets its own `NODE_OPTIONS` without keeping the inherited value.

Watch mode, `--ui`, `--coverage` and the other [passthrough commands](#limits) reach `tc`, which hands them straight back to Vitest.

**When it cannot reach `tc`** (binary missing or not executable, `execve` refused) the tests still run on plain Vitest and the failure is never silent: one line on stderr (`tc: node preload: ...; running plain Vitest`) and one line in `<cache root>/preload-errors.log` with the time, pid, directory and reason (rotated at 256 KiB). `tc doctor` shows the count and the latest entries.

Every other Node process pays for loading the file and nothing else (measured: 42.0 ms vs 42.2 ms for `node -e 0`). It only applies to shells that load the block (zsh through `~/.zshenv`, interactive bash through `~/.bashrc`); a tool that starts Node with a clean environment is not covered.

### Claude Code hook (`tc claude-hook`)

A `PreToolUse` hook for the Bash tool. It reads the hook JSON on stdin and, for commands that run Vitest once directly (`npx vitest run ...`, `pnpm exec vitest run ...`, `pnpm vitest run ...`, `yarn vitest run ...`, `./node_modules/.bin/vitest run ...`, `vitest run ...`, also after `cd <dir> &&`), answers with the [documented response](https://code.claude.com/docs/en/hooks#pretooluse-decision-control) that replaces the command:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"cd app && TC_INVOKED_BY=claude-hook tc vitest run tests/a.test.ts","description":"...","timeout":120000}}}
```

For anything else it prints nothing, which leaves the tool call untouched. Details:

- Only the launcher is replaced. Arguments, pipes, redirections and neighbouring commands stay byte for byte: `npx vitest run 2>&1 | tail -20` becomes `TC_INVOKED_BY=claude-hook tc vitest run 2>&1 | tail -20`.
- `updatedInput` carries every other field of the tool input unchanged, as the hook contract requires. The hook sets no `permissionDecision`: the rewritten command goes through the normal permission flow.
- It only rewrites when the directory the command runs in (the session `cwd`, followed through literal `cd <dir>` steps) resolves a Vitest binary. Commands using subshells, `$(...)`, backticks, heredocs or comments are left alone.
- The rewrite is idempotent and tolerates rtk prefixes: `rtk proxy npx vitest run a` becomes `TC_INVOKED_BY=claude-hook rtk proxy tc vitest run a`, `rtk vitest a` becomes `TC_INVOKED_BY=claude-hook tc vitest a`, and a command that already calls `tc vitest` is not touched.
- Claude Code runs matching hooks in parallel on the original command. With rtk's hook installed, both rewrite a direct Vitest command, so exclude Vitest in rtk (`[hooks] exclude_commands = ["vitest"]` in its `config.toml`).

Settings snippet (`~/.claude/settings.json`):

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "~/.local/bin/tc claude-hook" }] }
    ]
  }
}
```

### Turning everything off (`TC_DISABLE`)

`TC_DISABLE=1` (any value except empty and `0`) disables every path: the script shell passes everything to `/bin/sh`, the Node preload does nothing, the hook prints nothing, and `tc vitest` runs plain Vitest once (logged with `mode: disabled`). `tc` sets it for the Vitest process it starts, so a test that itself runs a package script gets plain Vitest instead of queueing behind the run that started it.

### Worktrees share one cache

Keys are built from repo-relative paths and content hashes, never from where a checkout lives, so every checkout of a repository reads and writes the same cache. A new `git worktree` starts with everything its repository already has: the first run there replays whatever is unchanged, and so does `tc explain`, without starting Node. What makes that work:

- the repo id comes from the shared git directory (read from `.git` and `commondir`, no `git` process);
- the node_modules install marker is hashed by what it describes: pnpm's `.modules.yaml` (JSON since pnpm 11) with its keys sorted and `prunedAt` removed, because each worktree has its own install of the same lockfile;
- the records of the main-process inputs and of test discovery use repo-relative paths;
- a test keeps up to 8 dependency lists (`deps/<baseKey>.json`, newest first). Two checkouts, or two branches, whose sources make one test execute different files each find their own entries; with a single list they would evict each other on every run. An entry found through an older list is as valid as any: it says that with those inputs at those contents the test passed.

What stays per checkout: the limit on concurrent Vitest runs (`maxConcurrentRuns`), so two worktrees do not queue behind each other. `tc clean`, `tc stats`, `tc gc` and the quarantine list act on the whole repository. A test that reads a variable holding an absolute path (`PWD`, `INIT_CWD`) depends on its value and is not shared.

---

## Guarantees

- **Only passes are cached**: failing test runs never write a cache entry and always re-run (`--no-cache` neither reads nor writes cache entries).
- **A pass is stored only under content the test saw**: see [When a pass is not cached](#when-a-pass-is-not-cached).
- **The worker hooks are transparent**: the functions `tc` replaces to record what a test reads, spawns or connects to (`node:fs`, `node:fs/promises`, `node:child_process`, `node:net`, `node:dgram`, `node:dns`) keep the name, length, prototype and own properties of the originals, including `util.promisify.custom`, so `promisify(execFile)`, `promisify(exec)`, `promisify(fs.exists)` and `promisify(dns.lookup)` resolve to the same shapes as without `tc`.
- **Unreadable or corrupt cache data is always a miss**: truncated, unparsable, or format-version-mismatched files (`entries`, `deps`, `discovery`, `meta`, `filehashes.json`, `node-versions.json`, `stats.json`) are treated as cache misses and removed automatically—never crashing and never returning a false pass.
- **Atomic writes & crash safety**: cache files are written via same-directory temp files + `fsync` + atomic `rename`. Cross-process deduplication (`locks/key-<baseKey>.lock`) and the repo runner semaphore (`locks/vitest-*.lock`) use kernel `flock` bound to open file descriptors, so `SIGKILL` never leaves blocking stale locks.

---

## Limits

- **Non-deterministic or stateful tests must be excluded**: tests relying on `Date.now()`, `Math.random()`, wall-clock timing or state outside the repository will replay cached passes until a tracked input changes. Exclude them in `testcache.json` `exclude` or run with `--no-cache`.
- **Imported but unused modules are not dependencies.** A module counts when one of its functions ran or one of its exports was read (Vite turns exports into getters, so constants count), or when it was imported with a bare `import './x'`. A module imported with bindings that nothing uses is ignored on purpose (this is what keeps barrel files from invalidating everything); if such a module is later edited to do something at its top level, tests that load it keep their cached pass until another input changes. Side effects loaded through `require('./x')` or a dynamic `import('./x')` whose result is discarded are not detected either.
- **Global `setupFiles` and the config affect every test**: editing a setup file, a `globalSetup` file, a file the config imports, a `.env` file, a lockfile or any other project file re-runs all tests.
- **First runs are slower**: cold runs collect V8 precise coverage (`Profiler.startPreciseCoverage`) and run Vitest file discovery. The first run of a project also learns the main-process inputs.
- **Environment variables that appear later are not seen through `loadEnv` or enumeration**: a key is tracked once the config has read it, so a variable that did not exist when the config read `env.SOME_KEY` (and got `undefined`), or when it ran `Object.keys(process.env)`, does not invalidate when it is set later. `VITE_*` is always in the key; list any other such variable in `testcache.json` `env`.
- **Whole-environment reads other than `loadEnv` put every variable in the key**: `{ ...process.env }` or `dotenv` in the config, `test.env: loadEnv(...)`, a `.env` file that uses `$VAR` expansion, a CommonJS config, or a Node without `module.registerHooks`. Results are then correct but only reused while the environment is identical, which in practice means the same shell session.
- **Locale without `LANG`/`LC_ALL`**: when neither is set, Node takes the locale from the operating system settings (on macOS, the user's language preferences), which `tc` does not read.
- **Shim defaults outside the project tree**: a global default changed with `volta install node@x`, `asdf global` or `mise use -g` is only seen when it lives in a pin file in an ancestor of the project (for example `~/.tool-versions` for a project under `$HOME`). Otherwise the next real run notices the mismatch and refuses to cache until the version is resolved again.
- **Untracked scopes**: reads outside the repository root (e.g. `/tmp`) and inside `node_modules` (beyond lockfiles, the install marker and the Vitest version) are not tracked. A config dependency outside the repository is tracked; a test's own reads outside it are not.
- **Glob contents during a run**: `import.meta.glob` listings are compared before and after the run, but files matched by a glob with a `query` are not checked against the two-second rule.
- **`tc` killed by `SIGKILL` while queued or running**: nothing is cached and locks are released by the kernel; Vitest stops through its parent watchdog (see above). A test that itself detaches a child into another process group can leave that child behind.
- **Arguments after a bare `--`** are dropped, as Vitest itself ignores them.
- **Passthrough flags & subcommands**: `--watch`/`-w`, `--ui`, `--update`/`-u`, `--coverage`, `--typecheck`, `--changed`, `--lastFailed`, `--sinceLastRun`, `--fsModuleCache`, `--browser`, `--shard`, `--mergeReports`, `--no-isolate`, `--isolate=false`, `--help`, `--version`, and subcommands `watch`, `bench`, `list`, `typecheck`, `init` pass directly through to Vitest uncached, and so does `related` without `--run` (it watches on a terminal).
- **`vitest related <sources> --run`** (what lint-staged and commit hooks run) is cached. A cached file is replayed when a source is the test, a file its passing run executed, or a file under one of its `import.meta.glob` directories; `package.json` and `vite.config.*`/`vitest.config.*` select every test, as Vitest's default `forceRerunTriggers` do. The files without a cached pass go to Vitest with the sources, and Vitest picks the ones that import a source. Two differences from plain Vitest, neither of which changes a result: a cached test that only imports a source without executing it is not listed, and custom `forceRerunTriggers` are not read, so a trigger of your own reruns only the uncached tests. When none of those is related, Vitest prints `No test files found` with the files it was given before `tc` lists the cached ones.
- **Workspaces / browser mode**: multi-project `test.projects` configs and browser-mode tests run through `tc` but are never cached.
- **Vitest's transform cache is off under `tc`**: on Vitest 5 a file that does have to run pays the full transform again instead of reading `node_modules/.vitest-cache`.
- **Supported Vitest versions**: the whole end-to-end suite runs against 3.2.7 (`testdata/fixture-vitest3`), 4.1.11 (`testdata/fixture-vitest`) and 5.1.1 (`testdata/fixture-vitest5`); select one with `TC_TEST_FIXTURE=<dir> go test ./...`. Other majors are not tested.
- **POSIX only**: process groups, signals and `flock` are used directly.

---

## Configuration

`tc` loads and merges JSON config files in order (later files override scalar settings and append to `exclude`/`env`):

1. `~/.config/testcache/config.json` and `~/.config/testcache/testcache.json` (or `$TESTCACHE_CONFIG_DIR`)
2. `<repo-root>/testcache.json`
3. `<project-root>/testcache.json` (when running in a monorepo subdirectory)

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `exclude` | `string[]` | `[]` | Glob patterns (`*`, `?`, `**`) relative to project root for test files that always run uncached. |
| `env` | `string[]` | `[]` | Additional environment variable names included in `baseKey`. |
| `maxConcurrentRuns` | `int` | `1` | Maximum concurrent Vitest invocations per repository. |
| `maxWorkers` | `int` | `clamp(NumCPU/2, 1, 4)` | Default `--maxWorkers` cap passed to Vitest unless specified on the CLI. |
| `maxCacheBytes` | `int64` | `1073741824` (1 GiB) | Per-repository cache size limit before LRU eviction. |
| `maxAgeDays` | `int` | `30` | Maximum age in days before unused cache files expire. |
| `normalizeHash` | `bool` | `true` | Strip non-meaningful comments, normalize multi-line whitespace and drop top-level type-only TypeScript statements when hashing JS/TS/JSX/CSS dependencies and test files; set `false` for raw byte hashing. |
| `revalidateRate` | `float64` | `0.02` | Probability (`0.0`–`1.0`) that each cache hit is re-run on `tc vitest` to detect flaky tests or untracked inputs. |
| `alertCommand` | `string` | `""` | Shell command run with the run-log line on stdin when a run has anomalies (see [Alerts](#alerts-alertcommand)). Any config file may set it; the user config is the usual place. |

Changing `maxCacheBytes`, `maxAgeDays`, `revalidateRate`, or `alertCommand` does not invalidate existing test cache keys.

Example `testcache.json`:

```json
{
  "exclude": ["**/*.integration.test.ts", "tests/e2e/**"],
  "env": ["API_BASE_URL", "FEATURE_FLAGS"],
  "maxConcurrentRuns": 1,
  "maxWorkers": 4,
  "maxCacheBytes": 1073741824,
  "maxAgeDays": 30,
  "normalizeHash": true,
  "revalidateRate": 0.02
}
```

---

## Cache Location & Bounds

- **Cache root**: `~/.cache/testcache/` (overridable via `TESTCACHE_DIR`).
- **Per-repo directory**: `~/.cache/testcache/<repo-id>/`, where `<repo-id>` is the first 16 bytes (32 hex chars) of SHA-256 of the repository's git directory: the one the main checkout and every `git worktree` of it share. Outside git it is the checkout path. See [Worktrees](#worktrees-share-one-cache).
- **Layout**:
  - `entries/`, `deps/`, `discovery/`, `meta/`: cached outputs, dependency lists, discovery snapshots, `explain` metadata, and the main-process input records (`meta/config-*.json`).
  - `filehashes.json`, `stats.json`, `quarantine.json`, `gc.stamp`: per-repo file hash cache, cumulative stats, quarantined test list, and last-GC timestamp.
  - `locks/`, `runtime/`: `flock` lock files, materialized runner scripts, and ephemeral `run-*` directories.
  - `<cache-root>/node-versions.json`: shared `node --version` lookup cache (binary identity, plus version pins for shims).
  - `<cache-root>/preload-errors.log` (+ `.1`): runs the [Node preload](#node-preload) could not hand to `tc`.
  - `<cache-root>/runs.jsonl` (+ `.1`, `.2`), `runs.lock`, `alert-error.log`: the [run log](#run-log-and-anomalies), its append lock and the last `alertCommand` failure. `tc clean --all` removes them with everything else.
- **Bounds & eviction**: bounded per repository by `maxCacheBytes` (default `1 GiB`) and `maxAgeDays` (default `30` days). Cache hits update entry/dep mtimes in place (throttled to 1 minute) for accurate LRU eviction. `tc` runs non-blocking opportunistic GC at most once every 24 hours after an actual test run; `tc gc` runs blocking GC immediately and removes orphaned temp/run files older than 1 hour.

## License

Licensed under the [Apache License, Version 2.0](LICENSE). See [NOTICE](NOTICE).
