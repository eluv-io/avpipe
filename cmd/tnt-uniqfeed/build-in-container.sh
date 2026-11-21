#!/usr/bin/env bash

set -euo pipefail

ensure_build_toolchain() {
    local needs_install=0

    command -v cc >/dev/null 2>&1 || needs_install=1
    command -v make >/dev/null 2>&1 || needs_install=1
    command -v pkg-config >/dev/null 2>&1 || needs_install=1

    if [[ ${needs_install} -eq 0 ]]; then
        return
    fi

    apt-get update
    apt-get install --no-install-recommends --yes \
        build-essential \
        ca-certificates \
        pkg-config
    apt-get clean
    rm -rf /var/lib/apt/lists/*
}

ffmpeg_uses_build_tree() {
    [[ -f "${FFMPEG_DIST%/}/libavcodec/avcodec.h" ]]
}

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

if [[ -z "${FFMPEG_DIST:-}" ]]; then
    echo "FFMPEG_DIST must be set for container builds" >&2
    exit 1
fi

if [[ -z "${PKG_CONFIG_PATH:-}" ]] && ! ffmpeg_uses_build_tree; then
    PKG_CONFIG_PATH="${FFMPEG_DIST%/}/lib/pkgconfig"
fi

if [[ ! -f "${FFMPEG_DIST%/}/libavcodec/avcodec.h" && ! -d "${PKG_CONFIG_PATH%%:*}" ]]; then
    echo "PKG_CONFIG_PATH=${PKG_CONFIG_PATH} does not exist inside the container" >&2
    exit 1
fi

ensure_build_toolchain

if [[ -n "${PKG_CONFIG_PATH:-}" ]]; then
    export PKG_CONFIG_PATH
fi

ffmpeg_library_path=$(build_ffmpeg_library_path)
if [[ -z "${ffmpeg_library_path}" ]]; then
    echo "Could not locate FFmpeg shared libraries under FFMPEG_DIST=${FFMPEG_DIST}" >&2
    exit 1
fi

export LD_LIBRARY_PATH="${ffmpeg_library_path}:/runtime/lib:/runtime/lib/uf:/runtime/lib/3rdparty:${LD_LIBRARY_PATH:-}"

make local-build FFMPEG_DIST="${FFMPEG_DIST}" UF_RUNTIME_DIR=/runtime