#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<USAGE
Usage: $(basename "${BASH_SOURCE[0]}") [--short] [-h|--help]

Runs the Go test suite. By default the short tests (~2 minutes) run first, then
the full suite (at least 45 minutes).

  --short      run only the short tests
  -h, --help   show this help and exit
USAGE
}

SHORT_ONLY=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --short) SHORT_ONLY=true ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage >&2; exit 1 ;;
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
    $SHORT_ONLY || echo "Short tests failed; skipping long tests"
    exit 1
fi

if $SHORT_ONLY; then
    exit 0
fi

# This takes at least 45 minutes
echo "=== All tests ==="
go test -v --timeout 4h ./...
