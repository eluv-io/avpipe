#!/usr/bin/env bash
set -euo pipefail
AV_PIPE_PATH=""
AV_PIPE_MEDIA_DIR=""
SHORT_ONLY=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --av-pipe-path)
      AV_PIPE_PATH="${2:-}"
      AV_PIPE_MEDIA_DIR="$AV_PIPE_PATH/media"
      shift 2
      ;;
    --short)
      SHORT_ONLY=true
      shift 1
      ;;
    *)
      echo "Unknown argument: $1"
      exit 1
      ;;
  esac
done

# Check if container avpipe path was passed
if [[ -z "$AV_PIPE_PATH" ]]; then
  echo "Error: --av-pipe-path is required."
  exit 1
fi

# Check that avpipe dir is found in container
if [ ! -d "$AV_PIPE_PATH" ]; then
  echo "Error: Directory '$AV_PIPE_PATH' cannot be found in container. Check volume mounts argument for container run command."
  exit 1
fi

# Check if the media directoy is present in the avpipe directory
if [ ! -d "$AV_PIPE_MEDIA_DIR" ]; then
  echo "Error: Directory '$AV_PIPE_MEDIA_DIR' cannot be found. Check volume mounts argument for container run command."
  exit 1
fi

cd "$AV_PIPE_PATH"

if [ "$SHORT_ONLY" = "true" ]; then
    ./run_tests.sh --short
else
    ./run_tests.sh
fi
