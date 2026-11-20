#!/bin/zsh
# Checks every command's and window event's validation against the TypeScript app's: cases.cjs judges each
# with every field swapped for values from a pool, and cmd/command-check must agree, and decode each one taken.
# Usage: scripts/command-diff/run.sh   (TS_DIST names a CommonJS build of the TypeScript backend; see scripts/reducer-diff/run.sh)
set -e
here=${0:A:h}
root=${here:h:h}
dist=${TS_DIST:-/tmp/goport/tsdist}
[[ -f $dist/main/contracts/workspace-view-input.js ]] || { echo "No built TypeScript contracts at $dist (set TS_DIST)."; exit 1; }
cases=$(mktemp -t command-cases)
trap 'rm -f $cases' EXIT
node $here/cases.cjs $dist > $cases
(cd $root && go run ./cmd/command-check) < $cases
