#!/bin/bash

# For use with https://github.com/qluvio/elv-toolchain output

script_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
export UNIQFEED_DIST="${UNIQFEED_DIST:-$HOME/.local}"

export FFMPEG_DIST="${FFMPEG_DIST:-$HOME/.local}"
export SRT_DIST="${SRT_DIST:-$HOME/.local/bin}"


if [ -f "$HOME/.local/lib/pkgconfig/libavcodec.pc" ]; then
	export FFMPEG_DIST="$HOME/.local"
elif [ ! -f "$FFMPEG_DIST/lib/pkgconfig/libavcodec.pc" ]; then
	for cand in \
		"$HOME/ELV/elv-toolchain/dist/linux-glibc.2.35" \
		"$HOME/ELV/elv-toolchain/FFmpeg/FFmpeg/dist"; do
		if [ -f "$cand/lib/pkgconfig/libavcodec.pc" ]; then
			export FFMPEG_DIST="$cand"
			break
		fi
	done
fi

export PKG_CONFIG_PATH="$HOME/.local/lib/pkgconfig:$FFMPEG_DIST/lib/pkgconfig:${PKG_CONFIG_PATH:-}"
if [ -d "$UNIQFEED_DIST/lib/pkgconfig" ]; then
	export PKG_CONFIG_PATH="$UNIQFEED_DIST/lib/pkgconfig:$PKG_CONFIG_PATH"
fi
if [ -d "$HOME/ELV/elv-toolchain/dist/linux-glibc.2.35/lib/pkgconfig" ]; then
	export PKG_CONFIG_PATH="$PKG_CONFIG_PATH:$HOME/ELV/elv-toolchain/dist/linux-glibc.2.35/lib/pkgconfig"
fi
if [ -d "$UNIQFEED_DIST/lib" ]; then
	export LD_LIBRARY_PATH="$UNIQFEED_DIST/lib:$UNIQFEED_DIST/lib/uf:$UNIQFEED_DIST/lib/3rdparty:${LD_LIBRARY_PATH:-}"
fi
echo UNIQFEED_DIST=$UNIQFEED_DIST
echo FFMPEG_DIST=$FFMPEG_DIST
echo SRT_DIST=$SRT_DIST
echo PKG_CONFIG_PATH=$PKG_CONFIG_PATH
