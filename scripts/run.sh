#!/usr/bin/env bash
# Builds the app and runs it from this checkout.
#
# usage: scripts/run.sh [--no-build] [--fixture STATE.json]
#   ACT_PROFILE_DIR   profile folder (default: the source build's own profile)
#   ACT_WINDOW_SIZE   WxH to open the window at, e.g. 1280x800
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
build=1
while [ $# -gt 0 ]; do
  case "$1" in
    --no-build) build=0 ;;
    --fixture) export ACT_FIXTURE_STATE="$2"; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

wails="$(command -v wails || echo "$(go env GOPATH)/bin/wails")"
if [ "$build" = 1 ]; then
  (cd "$root" && "$wails" build -skipbindings -clean=false >/dev/null)
fi

case "$(uname -s)" in
  Darwin) binary="$root/build/bin/AI Coding Tool.app/Contents/MacOS/aicodingtool" ;;
  *) binary="$root/build/bin/aicodingtool" ;;
esac
if [ -n "${ACT_FIXTURE_STATE:-}" ] && [ -z "${ACT_FIXTURE_ATTACHMENTS:-}" ] && [ "$(uname -s)" = Darwin ]; then
  export ACT_FIXTURE_ATTACHMENTS="$HOME/Library/Application Support/AI Coding Tool Dev/attachments"
fi
exec "$binary"
