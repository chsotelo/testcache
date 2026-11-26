#!/usr/bin/env bash
# Builds the release artifacts for one version into dist/:
#   dist/testcache_<os>_<arch>.tar.gz   prebuilt tc plus the install/uninstall scripts
#   dist/checksums.txt                  SHA-256 of every archive
#   ./scripts/release.sh v1.2.3
# The release workflow runs it on a tag; it runs the same way locally.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 <version, e.g. v1.2.3>" >&2; exit 2; }
TAG="$1"
VERSION="${TAG#v}"
case "$VERSION" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "version must look like v1.2.3, got $TAG" >&2; exit 2 ;;
esac

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$REPO_ROOT/dist"
rm -rf "$DIST"
mkdir -p "$DIST"

TARGETS="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64"

for target in $TARGETS; do
  IFS=/ read -r goos goarch <<< "$target"
  name="testcache_${goos}_${goarch}"
  stage="$DIST/$name"
  mkdir -p "$stage/bin" "$stage/scripts"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.version=$TAG" -o "$stage/bin/tc" ./cmd/tc)
  cp "$REPO_ROOT/scripts/install.sh" "$REPO_ROOT/scripts/uninstall.sh" "$stage/scripts/"
  cp "$REPO_ROOT/LICENSE" "$REPO_ROOT/NOTICE" "$REPO_ROOT/README.md" "$stage/"
  tar -C "$DIST" -czf "$DIST/$name.tar.gz" "$name"
  rm -rf "$stage"
done

(cd "$DIST" && if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz; else shasum -a 256 ./*.tar.gz; fi | sed 's# \./# #' > checksums.txt)

echo "Built $TAG into $DIST"
