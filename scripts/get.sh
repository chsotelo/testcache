#!/bin/sh
# Downloads a prebuilt testcache release, checks its SHA-256 and runs its installer.
#   curl -fsSL https://raw.githubusercontent.com/chsotelo/testcache/main/scripts/get.sh | sh
#   curl -fsSL .../get.sh | sh -s -- --no-integrations     # install tc, change nothing else
# Environment: TESTCACHE_VERSION=v1.2.3 picks a release (default: the latest),
# PREFIX changes the install prefix (default: ~/.local).
set -eu

REPO="chsotelo/testcache"
VERSION="${TESTCACHE_VERSION:-latest}"
BASE_URL="${TESTCACHE_DOWNLOAD_URL:-}"

fail() { echo "testcache: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported system $(uname -s): testcache runs on macOS and Linux" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m): testcache runs on x86_64 and arm64" ;;
esac
command -v bash >/dev/null 2>&1 || fail "the installer needs bash"

if [ -z "$BASE_URL" ]; then
  if [ "$VERSION" = "latest" ]; then
    BASE_URL="https://github.com/$REPO/releases/latest/download"
  else
    BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
  fi
fi

name="testcache_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    fail "curl or wget is required"
  fi
}

echo "Downloading $name ($VERSION)"
fetch "$BASE_URL/$name.tar.gz" "$tmp/$name.tar.gz" || fail "download failed: $BASE_URL/$name.tar.gz"
fetch "$BASE_URL/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $BASE_URL/checksums.txt"

want="$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || fail "no checksum for $name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "$tmp/$name.tar.gz" | awk '{ print $1 }')"
else
  got="$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{ print $1 }')"
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
bash "$tmp/$name/scripts/install.sh" "$@"
