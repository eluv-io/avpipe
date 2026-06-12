#!/usr/bin/env bash
#
# Download or sync test media assets from GCS.
#
# Usage: ./scripts/download-test-assets.sh [--delete] [--dir <path>]
#
#   --delete   also remove local files not present in the bucket
#   --dir      local destination directory (default: <repo-root>/media)

set -euo pipefail

BUCKET="gs://eluvio-test-assets"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MEDIA_DIR="$REPO_ROOT/media"
DELETE_FLAG=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --delete) DELETE_FLAG="-d" ;;
        --dir)    MEDIA_DIR="$2"; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
    shift
done

if ! command -v gsutil &>/dev/null; then
    echo "gsutil not found — install the Google Cloud SDK: https://cloud.google.com/sdk/docs/install" >&2
    exit 1
fi

mkdir -p "$MEDIA_DIR"
# shellcheck disable=SC2086
gsutil -m rsync -r $DELETE_FLAG "$BUCKET/" "$MEDIA_DIR"
