#!/usr/bin/env bash
# Opens the built app at a size, captures its window, and quits it (macOS).
#
# usage: scripts/shot.sh WxH OUT.png [STATE.json]
#   STATE.json defaults to tmp/fixture/state.json (scripts/fixture-state.mjs writes it).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
size="$1"
out="$2"
state="${3:-$root/tmp/fixture/state.json}"
binary="$root/build/bin/AI Coding Tool.app/Contents/MacOS/aicodingtool"
profile="$(mktemp -d)"
tools="$(mktemp -d)"

cat > "$tools/window-id.swift" <<'SWIFT'
// Prints the id of the largest on-screen window a process owns.
import CoreGraphics
import Foundation

guard CommandLine.arguments.count > 1, let pid = Int32(CommandLine.arguments[1]) else { exit(2) }
let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let owned = windows.filter { ($0[kCGWindowOwnerPID as String] as? Int32) == pid && ($0[kCGWindowLayer as String] as? Int) == 0 }
func area(_ window: [String: Any]) -> Double {
  let bounds = window[kCGWindowBounds as String] as? [String: Double] ?? [:]
  return (bounds["Width"] ?? 0) * (bounds["Height"] ?? 0)
}
guard let largest = owned.max(by: { area($0) < area($1) }), let id = largest[kCGWindowNumber as String] as? Int else { exit(1) }
print(id)
SWIFT
swiftc -O -o "$tools/window-id" "$tools/window-id.swift"

ACT_WINDOW_SIZE="$size" ACT_PROFILE_DIR="$profile" ACT_FIXTURE_STATE="$state" \
  ACT_FIXTURE_ATTACHMENTS="$HOME/Library/Application Support/AI Coding Tool Dev/attachments" \
  "$binary" >/dev/null 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; rm -rf "$profile" "$tools"' EXIT

window=""
for _ in $(seq 1 50); do
  window="$("$tools/window-id" "$pid" 2>/dev/null || true)"
  [ -n "$window" ] && break
  sleep 0.2
done
if [ -z "$window" ]; then
  echo "no window for process $pid" >&2
  exit 1
fi
sleep "${SHOT_SETTLE:-2.5}"
mkdir -p "$(dirname "$out")"
screencapture -x -o -l "$window" "$out"
echo "$out"
