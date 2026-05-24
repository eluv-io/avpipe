#!/usr/bin/env bash
set -euo pipefail

# Note that this does not detect changes to eluvio-test-assets
if [ ! -d "./media" ]; then
    if ! command -v gsutil &>/dev/null; then
        echo "gsutil could not be found, install gsutil"
        exit 1
    fi
    mkdir ./media
    gsutil -m cp 'gs://eluvio-test-assets/*' ./media
fi

echo "=== Short tests ==="
if ! go test -v -short --timeout 30m ./...; then
    echo "Short tests failed; skipping long tests"
    exit 1
fi

echo "=== Long tests ==="
go test -v --timeout 4h ./...
