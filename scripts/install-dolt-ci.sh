#!/usr/bin/env bash
set -euo pipefail

readonly DOLT_VERSION="2.2.2"
readonly BASE_URL="https://github.com/dolthub/dolt/releases/download/v${DOLT_VERSION}"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    archive="dolt-linux-amd64.tar.gz"
    expected="16517e03e1d6e380654f2804de9e05357fb8a2d660ccb535fc1dc5410552e1bb"
    ;;
  Linux-aarch64|Linux-arm64)
    archive="dolt-linux-arm64.tar.gz"
    expected="68b52bc2dfbb53d7df761d4a6d660b302e6ba2850a62baa1ff78b1b15bffddb6"
    ;;
  Darwin-x86_64)
    archive="dolt-darwin-amd64.tar.gz"
    expected="6ce092b0f2dd45a4ece9d6e087df5b9f1a858e9e437ed48550af65ccf6fd0150"
    ;;
  Darwin-arm64)
    archive="dolt-darwin-arm64.tar.gz"
    expected="13feab5b3ad36f365cf3a5eb0a45de8bfd9b54268daa8ed705c37acb511fad4a"
    ;;
  *)
    echo "Unsupported Dolt CI platform: $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
archive_path="$tmpdir/$archive"

curl --fail --show-error --silent --location \
  --proto '=https' --tlsv1.2 \
  --output "$archive_path" "$BASE_URL/$archive"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$archive_path" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$archive_path" | cut -d' ' -f1)"
fi
if [[ "$actual" != "$expected" ]]; then
  echo "Dolt archive checksum mismatch: expected $expected, got $actual" >&2
  exit 1
fi

mkdir "$tmpdir/extracted"
tar -xzf "$archive_path" -C "$tmpdir/extracted"
binary="$(find "$tmpdir/extracted" -type f -path '*/bin/dolt' -print -quit)"
if [[ -z "$binary" || ! -f "$binary" ]]; then
  echo "Dolt archive did not contain bin/dolt" >&2
  exit 1
fi
sudo install -m 0755 "$binary" /usr/local/bin/dolt
dolt version
