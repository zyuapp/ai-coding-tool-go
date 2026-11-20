#!/bin/zsh
# Differential fuzz of application.Reduce against the TypeScript reducer.
# Usage: scripts/reducer-diff/run.sh [diff.cjs options, e.g. --seeds 1-50 --steps 400 --cover]
# TS_DIST names a CommonJS build of the TypeScript backend (default /tmp/goport/tsdist); build one with
#   (cd ../ai-coding-tool && npx tsc -p tsconfig.main.json --outDir $TS_DIST/main) && echo '{"type":"commonjs"}' > $TS_DIST/package.json
set -e
here=${0:A:h}
root=${here:h:h}
dist=${TS_DIST:-/tmp/goport/tsdist}
[[ -f $dist/main/application/workspace-reducer.js ]] || { echo "No built TypeScript reducer at $dist (set TS_DIST)."; exit 1; }
bin=${TMPDIR:-/tmp}/reduce-lines
(cd $root && go build -o $bin ./cmd/reduce-lines)
export TRAILS=${TRAILS:-${TMPDIR:-/tmp}}
exec node "$here/diff.cjs" "$dist" "$bin" "$@"
