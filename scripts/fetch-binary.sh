#!/bin/sh
# Put a herdr-resurrect binary in bin/ for the current platform.
#
# Prefers the prebuilt asset from the GitHub release matching the version in
# herdr-plugin.toml, so installing needs only curl and tar. Falls back to
# building from source when no asset matches, which needs Go.
set -eu

REPO="PascalKraupner/herdr-resurrect"
VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' herdr-plugin.toml)"

case "$(uname -s)" in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64 | amd64)  arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p bin
asset="herdr-resurrect_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/v$VERSION/$asset"

if command -v curl >/dev/null 2>&1 && curl -fsSL "$url" -o "/tmp/$asset" 2>/dev/null; then
    tar -xzf "/tmp/$asset" -C bin herdr-resurrect
    rm -f "/tmp/$asset"
    chmod +x bin/herdr-resurrect
    echo "installed prebuilt $asset ($VERSION)"
    exit 0
fi

if command -v go >/dev/null 2>&1; then
    echo "no prebuilt binary for $os/$arch, building from source"
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/herdr-resurrect .
    exit 0
fi

echo "no prebuilt binary for $os/$arch and Go is not installed" >&2
exit 1
