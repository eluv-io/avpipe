#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -d "$REPO_ROOT/media" ]; then
    echo "Test media not found. Run: ./scripts/download-test-assets.sh" >&2
    exit 1
fi

echo "Running live UDP tests"
go test -run TestUdpToMp4 ./live/

echo "Running live probe tests"
go test -run TestProbe ./live/

echo "Running live RTMP tests"
go test -run TestRtmpToMp4 ./live/

echo "Running live SRT tests"
go test -run TestSrtToMp4 ./live/

#echo "Running live HLS tests"
#go test -run TestHLS ./live/
