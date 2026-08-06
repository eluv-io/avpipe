#!/usr/bin/env bash
set -euo pipefail

SHORT_ONLY=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --short) SHORT_ONLY=true ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
    shift
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -d "$REPO_ROOT/media" ]; then
    echo "Test media not found. Run: ./scripts/download-test-assets.sh" >&2
    exit 1
fi

# First run all the tests that complete in under 5 seconds (total ~2 minutes)
echo "=== Short tests ==="
if ! go test -v -short --timeout 30m ./...; then
    echo "Short tests failed; skipping long tests"
    exit 1
fi

if $SHORT_ONLY; then
    exit 0
fi

# This takes at least 45 minutes
echo "=== All tests ==="
go test -v --timeout 4h ./...
