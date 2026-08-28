#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AVPIPE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

SRC_DIST="${1:-${AVPIPE_DIR}/../tnt-uniqfeed}"
FFMPEG_SRC="${FFMPEG_SRC:-${HOME}/ELV/FFmpeg}"
LOCAL_DIR="${HOME}/.local"

if [[ ! -d "${SRC_DIST}" ]]; then
    echo "error: source uniqfeed directory not found: ${SRC_DIST}" >&2
    exit 1
fi

if [[ ! -d "${SRC_DIST}/lib" ]]; then
    echo "error: source uniqfeed lib directory not found: ${SRC_DIST}/lib" >&2
    exit 1
fi

if [[ ! -d "${SRC_DIST}/include" ]]; then
    echo "error: source uniqfeed include directory not found: ${SRC_DIST}/include" >&2
    exit 1
fi

mkdir -p "${LOCAL_DIR}/lib" "${LOCAL_DIR}/include"

cp -a "${SRC_DIST}/lib/." "${LOCAL_DIR}/lib/"
cp -a "${SRC_DIST}/include/." "${LOCAL_DIR}/include/"

if [[ -d "${FFMPEG_SRC}/include" ]]; then
    for d in libavcodec libavdevice libavfilter libavformat libavresample libavutil libswresample libswscale; do
        if [[ -d "${FFMPEG_SRC}/include/${d}" ]]; then
            mkdir -p "${LOCAL_DIR}/include/${d}"
            cp -a "${FFMPEG_SRC}/include/${d}/." "${LOCAL_DIR}/include/${d}/"
        fi
    done
fi

for d in libavcodec libavdevice libavfilter libavformat libavresample libavutil libswresample libswscale; do
    if [[ -d "${FFMPEG_SRC}/${d}" ]]; then
        mkdir -p "${LOCAL_DIR}/include/${d}"
        cp -a "${FFMPEG_SRC}/${d}"/*.h "${LOCAL_DIR}/include/${d}/" 2>/dev/null || true
    fi
done

for d in libavcodec libavdevice libavfilter libavformat libavresample libavutil libswresample libswscale; do
    if [[ -d "${FFMPEG_SRC}/${d}" ]]; then
        cp -a "${FFMPEG_SRC}/${d}"/lib*.so* "${LOCAL_DIR}/lib/" 2>/dev/null || true
    fi
done

mkdir -p "${LOCAL_DIR}/lib/pkgconfig"

write_ffmpeg_pc() {
    local name="$1"
    local desc="$2"
    cat > "${LOCAL_DIR}/lib/pkgconfig/${name}.pc" <<EOF
prefix=${LOCAL_DIR}
exec_prefix=\${prefix}
libdir=\${exec_prefix}/lib
includedir=\${prefix}/include

Name: ${name}
Description: ${desc}
Version: 62.0.0
Libs: -L\${libdir} -l${name#lib}
Cflags: -I\${includedir}
EOF
}

write_ffmpeg_pc libavutil "FFmpeg utility library"
write_ffmpeg_pc libswresample "FFmpeg audio resampling library"
write_ffmpeg_pc libswscale "FFmpeg image scaling library"
write_ffmpeg_pc libavcodec "FFmpeg codec library"
write_ffmpeg_pc libavformat "FFmpeg container format library"
write_ffmpeg_pc libavfilter "FFmpeg filter library"
write_ffmpeg_pc libavdevice "FFmpeg device library"
write_ffmpeg_pc libavresample "FFmpeg legacy audio resampling library"

echo "Staged uniqfeed libs and headers into: ${LOCAL_DIR}"
echo "  libs:    ${LOCAL_DIR}/lib"
echo "  headers: ${LOCAL_DIR}/include"
echo "  ffmpeg pkgconfig: ${LOCAL_DIR}/lib/pkgconfig"
