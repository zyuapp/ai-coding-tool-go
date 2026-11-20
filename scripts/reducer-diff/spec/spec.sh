#!/bin/zsh
# Runs the TypeScript app's own test suite with every reduce it makes also run through application.Reduce,
# and lists where they differ (the tests still see TypeScript's answers).
# Usage: scripts/reducer-diff/spec/spec.sh [vitest paths]   (default: tests/application tests/domain tests/contracts tests/host)
# TS_SRC names the TypeScript checkout (default ../ai-coding-tool beside this one); SPEC_DIR its scratch copy.
set -e
here=${0:A:h}
root=${here:h:h:h}
src=${TS_SRC:-${root:h}/ai-coding-tool}
spec=${SPEC_DIR:-${TMPDIR:-/tmp}/act-go-spec}
mkdir -p $spec
rsync -a --delete --exclude node_modules --exclude dist --exclude release --exclude ios --exclude native --exclude vendor --exclude .git $src/ $spec/ts/
[[ -d $spec/ts/node_modules ]] || cp -cR $src/node_modules $spec/ts/node_modules
node $here/patch.cjs $spec/ts
(cd $root && go build -o $spec/reduce-lines ./cmd/reduce-lines)
log=$spec/divergences.jsonl
: > $log
[[ $# -eq 0 ]] && set -- tests/application tests/domain tests/contracts tests/host
(cd $spec/ts && ACT_GO=diff ACT_GO_DUMP=${ACT_GO_DUMP:-} ACT_GO_BIN=$spec/reduce-lines ACT_GO_LOG=$log npx vitest run "$@" --reporter=dot > $spec/run.txt 2>&1) || true
grep 'Tests ' $spec/run.txt || tail -5 $spec/run.txt
node $here/summary.cjs $log
