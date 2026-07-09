#!/bin/bash
#
# elv-toolchain build env: point avpipe at the FFmpeg fork + SRT installed under ~/.local.

export FFMPEG_DIST="$HOME/.local"
export SRT_DIST="$HOME/.local/bin"
export PKG_CONFIG_PATH="$HOME/.local/lib/pkgconfig"
echo FFMPEG_DIST=$FFMPEG_DIST
echo SRT_DIST=$SRT_DIST
echo PKG_CONFIG_PATH=$PKG_CONFIG_PATH
