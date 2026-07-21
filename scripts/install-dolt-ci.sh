#!/usr/bin/env bash
set -euo pipefail

readonly DOLT_VERSION="2.1.10"
readonly BASE_URL="https://github.com/dolthub/dolt/releases/download/v${DOLT_VERSION}"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    archive="dolt-linux-amd64.tar.gz"
    expected="bf6528c10db5b304eef3ee0107762ab4a93baa4c42be36b9f37eb67b35bb422c"
    ;;
  Linux-aarch64|Linux-arm64)
    archive="dolt-linux-arm64.tar.gz"
    expected="e816feb7294192e6292a0756e836248f63fb19bf0f18416bb66909c9e77d1b73"
    ;;
  Darwin-x86_64)
    archive="dolt-darwin-amd64.tar.gz"
    expected="f2e548e89839a014f2f2b8caed0552110695699077f7e837596ff2ce6038852d"
    ;;
  Darwin-arm64)
    archive="dolt-darwin-arm64.tar.gz"
    expected="f2f784ee5905cdc573330d4c2813825633b72281408cb6cca3af948689d5284b"
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
