#!/usr/bin/env bash
# Usage: scripts/prepare-cua-driver.sh [out-dir]
# Fetches the pinned Cua Driver for this machine into out-dir (default build/cua-driver), where a
# source build runs it from (computeruse.DriverPath) and the packaging scripts copy it from. macOS
# builds are Apple silicon only.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/build/cua-driver}
version=0.55.0
case "$(uname -s)-$(uname -m)" in
  Darwin-*) archive=cua-driver-rs-$version-darwin-arm64.tar.gz entry=cua-driver-rs-$version-darwin-arm64/cua-driver sha256=2749dfbc95d5735fd848926725bb8dae137a3526c239e1ef2cd7ae0767956878 ;;
  Linux-x86_64) archive=cua-driver-rs-$version-linux-x86_64-binary.tar.gz entry=cua-driver sha256=1cb8aa6d30e5cdf5bf4d222cb3693a50af2c178073a49dc59e88737c5f7807e6 ;;
  Linux-aarch64 | Linux-arm64) archive=cua-driver-rs-$version-linux-arm64-binary.tar.gz entry=cua-driver sha256=401f7f4a9ad276dbcdb324f1b00e152987a46985b68c9e625ac01101f5e6af77 ;;
  *) echo "No Cua Driver for $(uname -s) $(uname -m)." >&2; exit 0 ;;
esac
marker="$version-$archive"
if [[ -x $out/cua-driver && $(cat "$out/version" 2>/dev/null) == "$marker" ]]; then
  exit 0
fi
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/$archive" "https://github.com/trycua/cua/releases/download/cua-driver-rs-v$version/$archive"
actual=$( (command -v sha256sum >/dev/null && sha256sum "$work/$archive" || shasum -a 256 "$work/$archive") | cut -d' ' -f1)
[[ $actual == "$sha256" ]] || { echo "Cua Driver archive checksum mismatch." >&2; exit 1; }
tar -xzf "$work/$archive" -C "$work" "$entry"
binary=$work/$entry
if [[ $(uname -s) == Darwin && $(lipo -archs "$binary") != arm64 ]]; then
  lipo -thin arm64 "$binary" -output "$binary.arm64"
  mv "$binary.arm64" "$binary"
fi
chmod 755 "$binary"
mv "$binary" "$out/cua-driver"
echo "$marker" > "$out/version"
echo "Fetched the Cua Driver $version into $out"
