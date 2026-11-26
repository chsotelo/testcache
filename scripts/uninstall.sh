#!/usr/bin/env bash
# Deactivates tc machine-wide and removes it. The integrations are reverted first: with a
# script shell pointing at a missing tc-shell, every npm and pnpm script would fail.
#   ./scripts/uninstall.sh                remove the integrations, then the binaries
#   ./scripts/uninstall.sh --keep-binary  remove the integrations only
set -euo pipefail

INSTALL_DIR="${PREFIX:-$HOME/.local}/bin"
TC_BIN="$INSTALL_DIR/tc"
TC_SHELL="$INSTALL_DIR/tc-shell"
PRELOAD="${PREFIX:-$HOME/.local}/lib/testcache/node-preload.cjs"
ZSHENV="${ZDOTDIR:-$HOME}/.zshenv"
BLOCK_BEGIN="# >>> testcache (tc) >>>"
BLOCK_END="# <<< testcache (tc) <<<"

keep_binary=0
case "${1:-}" in
  "") ;;
  --keep-binary) keep_binary=1 ;;
  *) echo "usage: $0 [--keep-binary]" >&2; exit 2 ;;
esac

for rc in "$ZSHENV" "$HOME/.bashrc"; do
  if [ -f "$rc" ] && grep -qF "$BLOCK_BEGIN" "$rc"; then
    tmp="$(mktemp)"
    awk -v b="$BLOCK_BEGIN" -v e="$BLOCK_END" '$0==b{skip=1} !skip{print} $0==e{skip=0}' "$rc" > "$tmp"
    mv -f "$tmp" "$rc"
    echo "Removed the tc block from $rc (open a new shell, or: unset pnpm_config_script_shell)"
  fi
done

if command -v npm >/dev/null 2>&1; then
  # npm 11 refuses `npm config get script-shell` ("protected"): read the file directly.
  npmrc="$(npm config get userconfig 2>/dev/null || echo "$HOME/.npmrc")"
  current=""
  if [ -f "$npmrc" ]; then
    current="$(sed -n 's/^[[:space:]]*script-shell[[:space:]]*=[[:space:]]*//p' "$npmrc" | tail -1)"
  fi
  if [ "$current" = "$TC_SHELL" ]; then
    npm config delete script-shell --location=user
    echo "Removed script-shell from the user .npmrc"
  fi
fi

# The Node preload is emptied, not deleted: a shell opened while it was active still names it
# in NODE_OPTIONS, and Node refuses to start when a --require'd file is missing.
if [ -f "$PRELOAD" ]; then
  tmp="$(mktemp "$(dirname "$PRELOAD")/.node-preload.XXXXXX")"
  printf '%s\n' "// tc was deactivated. This empty file keeps Node working in shells opened before that:" \
    "// they still load it through NODE_OPTIONS. Delete it once those shells are closed." > "$tmp"
  chmod 644 "$tmp"
  mv -f "$tmp" "$PRELOAD"
  echo "Emptied $PRELOAD (delete it once every shell opened before now is closed)"
fi

# Anything still pointing at tc-shell that this script did not write (e.g. added by hand).
leftover=""
for rc in "$HOME/.npmrc" "$ZSHENV" "$HOME/.zprofile" "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
  if [ -f "$rc" ] && grep -q 'tc-shell' "$rc"; then
    leftover="$leftover $rc"
  fi
done
if [ -n "$leftover" ]; then
  echo "tc-shell is still referenced in:$leftover" >&2
  echo "Remove those lines, then run this again. The binaries were kept so scripts keep working." >&2
  exit 1
fi

if [ "$keep_binary" -eq 1 ]; then
  exit 0
fi
for target in "$TC_SHELL" "$TC_BIN" "$(dirname "$PRELOAD")/uninstall.sh"; do
  if [ -e "$target" ] || [ -L "$target" ]; then
    rm -f "$target"
    echo "Removed $target"
  fi
done
