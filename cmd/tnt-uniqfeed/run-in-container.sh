#!/usr/bin/env bash

set -euo pipefail

build_ffmpeg_library_path() {
    local ffmpeg_root=${FFMPEG_DIST%/}
    local -a lib_dirs=()
    local dir

    if [[ -d "${ffmpeg_root}/lib" ]]; then
        printf '%s\n' "${ffmpeg_root}/lib"
        return 0
    fi

    for dir in \
        libavfilter \
        libavformat \
        libavcodec \
        libavdevice \
        libswscale \
        libswresample \
        libavresample \
        libpostproc \
        libavutil; do
        [[ -d "${ffmpeg_root}/${dir}" ]] && lib_dirs+=( "${ffmpeg_root}/${dir}" )
    done

    IFS=':'
    printf '%s\n' "${lib_dirs[*]}"
}

check_runtime_dependencies() {
    local missing_libs

    missing_libs=$(ldd /workspace/avpipe/bin/tnt-uniqfeed 2>/dev/null | awk '/=> not found/ {print $1}')
    if [[ -z "${missing_libs}" ]]; then
        return 0
    fi

    echo "tnt-uniqfeed runtime dependency check failed." >&2
    echo "Missing shared libraries:" >&2
    printf '  %s\n' ${missing_libs} >&2
    echo "The FFmpeg libraries from FFMPEG_DIST are incompatible with the libraries available in this container image." >&2

    if grep -q '^libnpp' <<< "${missing_libs}"; then
        echo "Detected missing NVIDIA NPP libraries. This usually means FFmpeg was built against a different CUDA/NPP major version than the container provides." >&2
    fi

    exit 1
}

if [[ ! -x /workspace/avpipe/bin/tnt-uniqfeed ]]; then
    echo "bin/tnt-uniqfeed is not built yet. Run 'make -C cmd/tnt-uniqfeed' first." >&2
    exit 1
fi

if [[ -z "${FFMPEG_DIST:-}" ]]; then
    echo "FFMPEG_DIST must be set for container runs" >&2
    exit 1
fi

ffmpeg_library_path=$(build_ffmpeg_library_path)
if [[ -z "${ffmpeg_library_path}" ]]; then
    echo "Could not locate FFmpeg shared libraries under FFMPEG_DIST=${FFMPEG_DIST}" >&2
    exit 1
fi

export LD_LIBRARY_PATH="${ffmpeg_library_path}:/runtime/lib:/runtime/lib/uf:/runtime/lib/3rdparty:${LD_LIBRARY_PATH:-}"

check_runtime_dependencies

exec /workspace/avpipe/bin/tnt-uniqfeed "$@"