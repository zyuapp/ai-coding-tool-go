#!/usr/bin/env bash
# Usage: scripts/release-upload.sh <tag> <file>...
# Uploads files to the draft release <tag> once the release workflow's prepare job has opened it.
# Builds start alongside that job, so the draft may not exist yet. Never uploads to a published release.
set -euo pipefail
[[ $# -ge 2 ]] || { echo "Usage: $0 <tag> <file>..." >&2; exit 1; }
tag=$1
shift
for _ in $(seq 60); do
  if [[ $(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null) == true ]]; then
    gh release upload "$tag" "$@" --clobber
    exit 0
  fi
  sleep 5
done
echo "No draft release $tag to upload to." >&2
exit 1
