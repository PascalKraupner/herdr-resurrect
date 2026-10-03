#!/bin/sh
# Put a herdr-resurrect binary in bin/ for the current platform.
#
# Prefers the prebuilt asset from the GitHub release matching the version in
# herdr-plugin.toml, so installing needs only curl and tar. Falls back to
# building from source when no asset matches, which needs Go.
set -eu
umask 022

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

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

if command -v curl >/dev/null 2>&1 && curl --retry 3 -fsSL "$url" -o "$tmp/$asset"; then
    curl --retry 3 -fsSL "https://github.com/$REPO/releases/download/v$VERSION/SHA256SUMS" -o "$tmp/SHA256SUMS"
    expected="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$tmp/SHA256SUMS")"
    if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$tmp/$asset" | cut -d ' ' -f 1)"
    else
        actual="$(shasum -a 256 "$tmp/$asset" | cut -d ' ' -f 1)"
    fi
    [ -n "$expected" ] && [ "$expected" = "$actual" ] || { echo "checksum mismatch for $asset" >&2; exit 1; }
    tar -xzf "$tmp/$asset" -C "$tmp" herdr-resurrect
    mv "$tmp/herdr-resurrect" bin/herdr-resurrect
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
