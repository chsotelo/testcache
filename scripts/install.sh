#!/usr/bin/env bash
# Builds and installs tc, and activates it machine-wide in two ways:
#   - script shell: every package script that is exactly one Vitest single run (`pnpm test`,
#     `npm test`, `pnpm run test`, ...) is served by tc; every other script runs in /bin/sh
#     unchanged.
#   - Node preload: any other way of starting the Vitest CLI (`pnpm vitest run`,
#     `pnpm exec vitest`, `npx vitest`, node_modules/.bin/vitest, ...) is handed to tc by a
#     small file every Node process of the shell loads through NODE_OPTIONS.
#   ./scripts/install.sh                     build, install and activate both (default)
#   ./scripts/install.sh --no-node-preload   activate the script shell only
#   ./scripts/install.sh --no-integrations   build and install only
# Check the result at any time with `tc doctor`.
# Activation is idempotent and reverted by ./scripts/uninstall.sh.
# Turn tc off for one command or one shell with TC_DISABLE=1.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_DIR="${PREFIX:-$HOME/.local}/bin"
TC_BIN="$INSTALL_DIR/tc"
TC_SHELL="$INSTALL_DIR/tc-shell"
PRELOAD_DIR="${PREFIX:-$HOME/.local}/lib/testcache"
PRELOAD="$PRELOAD_DIR/node-preload.cjs"
ZSHENV="${ZDOTDIR:-$HOME}/.zshenv"
BASHRC="$HOME/.bashrc"
BLOCK_BEGIN="# >>> testcache (tc) >>>"
BLOCK_END="# <<< testcache (tc) <<<"

usage() { echo "usage: $0 [--no-integrations | --no-node-preload]" >&2; exit 2; }

activate=1
preload=1
case "${1:-}" in
  "") ;;
  --no-integrations) activate=0; preload=0 ;;
  --no-node-preload) preload=0 ;;
  *) usage ;;
esac
[ "$#" -le 1 ] || usage

# A release archive ships the binary in bin/tc; a source checkout builds it.
PREBUILT="$REPO_ROOT/bin/tc"

# Build or copy to a temporary path and check it before it replaces anything: a broken
# binary must never become the script shell of every npm/pnpm script.
mkdir -p "$INSTALL_DIR"
tmp_bin="$(mktemp "$INSTALL_DIR/.tc-new.XXXXXX")"
trap 'rm -f "$tmp_bin"' EXIT
if [ -f "$PREBUILT" ]; then
  cp "$PREBUILT" "$tmp_bin"
  chmod 755 "$tmp_bin"
else
  if [ -n "${TC_VERSION:-}" ]; then
    VERSION="$TC_VERSION"
  elif command -v git >/dev/null 2>&1 && VERSION="$(git -C "$REPO_ROOT" describe --always --dirty 2>/dev/null)" && [ -n "$VERSION" ]; then
    :
  else
    VERSION="dev"
  fi
  (cd "$REPO_ROOT" && go build -trimpath -ldflags "-X main.version=${VERSION}" -o "$tmp_bin" ./cmd/tc)
fi
VERSION="$("$tmp_bin" --version)"
VERSION="${VERSION#tc }"
[ "$(TC_DISABLE=1 "$tmp_bin" script-shell -c 'printf ok')" = "ok" ] || { echo "tc script shell self-check failed; nothing installed" >&2; exit 1; }
mv -f "$tmp_bin" "$TC_BIN"
trap - EXIT
ln -sf tc "$TC_SHELL"
echo "Installed tc ${VERSION} to $TC_BIN"

# The Node preload is always written next to the binary, activated or not: a shell opened
# while it was active keeps naming it in NODE_OPTIONS, and Node refuses to start when a
# --require'd file is missing. It is checked in a real Node process before it replaces the
# installed one, for the same reason.
mkdir -p "$PRELOAD_DIR"
# Kept next to the preload: an install from a downloaded archive has no checkout to run it from.
UNINSTALLER="$PRELOAD_DIR/uninstall.sh"
cp "$SCRIPT_DIR/uninstall.sh" "$UNINSTALLER.tmp" && chmod 755 "$UNINSTALLER.tmp" && mv -f "$UNINSTALLER.tmp" "$UNINSTALLER"
tmp_preload="$(mktemp "$PRELOAD_DIR/.node-preload.XXXXXX")"
trap 'rm -f "$tmp_preload"' EXIT
"$TC_BIN" node-preload > "$tmp_preload"
if command -v node >/dev/null 2>&1; then
  if [ "$(NODE_OPTIONS="--require $tmp_preload" node -e 'process.stdout.write("ok")' 2>/dev/null)" != "ok" ]; then
    echo "The Node preload failed its self-check; it was not installed and is not activated." >&2
    preload=0
    rm -f "$tmp_preload"
  fi
else
  echo "Note: node is not in PATH, the Node preload could not be checked and is not activated."
  preload=0
fi
if [ -f "$tmp_preload" ]; then
  chmod 644 "$tmp_preload"
  mv -f "$tmp_preload" "$PRELOAD"
fi
trap - EXIT
case "$PRELOAD" in
  *[[:space:]]*)
    [ "$preload" -eq 1 ] && echo "Note: $PRELOAD contains whitespace, which NODE_OPTIONS cannot carry; the Node preload is not activated."
    preload=0
    ;;
esac

case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Note: $INSTALL_DIR is not in PATH" ;;
esac

if [ "$activate" -eq 0 ]; then
  echo "Not activated (--no-integrations)."
  exit 0
fi

# pnpm 11+ reads the script shell only from the environment: a managed block in ~/.zshenv,
# which every zsh (interactive, scripts, editors' terminals) loads. When the login shell is
# bash, the same POSIX block also goes to ~/.bashrc, which interactive bash shells load.
write_env_block() {
  local rc="$1" tmp
  tmp="$(mktemp)"
  if [ -f "$rc" ]; then
    awk -v b="$BLOCK_BEGIN" -v e="$BLOCK_END" '$0==b{skip=1} !skip{print} $0==e{skip=0}' "$rc" > "$tmp"
  fi
  {
    printf '%s\n' "$BLOCK_BEGIN"
    printf '%s\n' "# Serves Vitest runs through tc. Off: TC_DISABLE=1. Check: tc doctor. Remove: $UNINSTALLER"
    printf 'export pnpm_config_script_shell="%s"\n' "$TC_SHELL"
    if [ "$preload" -eq 1 ]; then
      # Added once per shell tree, and only while the file exists.
      printf 'export TC_NODE_PRELOAD="%s"\n' "$PRELOAD"
      printf '%s\n' 'if [ -r "$TC_NODE_PRELOAD" ]; then'
      printf '%s\n' '  case " ${NODE_OPTIONS:-} " in'
      printf '%s\n' '    *" --require $TC_NODE_PRELOAD "*) ;;'
      printf '%s\n' '    *) export NODE_OPTIONS="${NODE_OPTIONS:+$NODE_OPTIONS }--require $TC_NODE_PRELOAD" ;;'
      printf '%s\n' '  esac'
      printf '%s\n' 'fi'
    fi
    printf '%s\n' "$BLOCK_END"
  } >> "$tmp"
  [ -f "$rc" ] && cp -p "$rc" "$rc.tc-backup"
  mv -f "$tmp" "$rc"
}
rc_files=("$ZSHENV")
if [ "$(basename "${SHELL:-}")" = "bash" ]; then rc_files+=("$BASHRC"); fi
for rc in "${rc_files[@]}"; do
  write_env_block "$rc"
done
rc_list="${rc_files[*]}"
echo "Activated for pnpm 11+: $rc_list (open a new shell, or: export pnpm_config_script_shell=\"$TC_SHELL\")"
if [ "$preload" -eq 1 ]; then
  echo "Activated the Node preload: $rc_list add --require $PRELOAD to NODE_OPTIONS (open a new shell)"
else
  echo "Node preload not activated."
fi

# npm and pnpm 9/10 read script-shell from the user ~/.npmrc. Never overwrite a script
# shell someone else configured. npm 11 refuses `npm config get script-shell`
# ("protected"), so read the user config file directly.
if command -v npm >/dev/null 2>&1; then
  npmrc="$(npm config get userconfig 2>/dev/null || echo "$HOME/.npmrc")"
  current=""
  if [ -f "$npmrc" ]; then
    current="$(sed -n 's/^[[:space:]]*script-shell[[:space:]]*=[[:space:]]*//p' "$npmrc" | tail -1)"
  fi
  case "$current" in
    "" | "$TC_SHELL")
      npm config set script-shell "$TC_SHELL" --location=user
      echo "Activated for npm and pnpm 9/10: user .npmrc"
      ;;
    *)
      echo "Skipped npm: script-shell is already set to $current (not tc). Set it yourself if you want tc there."
      ;;
  esac
fi
