#!/bin/bash
set -Eeuo pipefail

# Set environment for new processes started by launchd/Spotlight

fabric_dir=${1-"../content-fabric"}

set --  # unset args before we source
# shellcheck source=../init-env.sh
source "$(dirname "${BASH_SOURCE[0]}")/../init-env.sh" "$fabric_dir"

echo ELV_TOOLCHAIN_DIST_PLATFORM
launchctl getenv ELV_TOOLCHAIN_DIST_PLATFORM
launchctl setenv ELV_TOOLCHAIN_DIST_PLATFORM "${ELV_TOOLCHAIN_DIST_PLATFORM:-}"

echo "*** Replacing GOPRIVATE:"
launchctl getenv GOPRIVATE
launchctl setenv GOPRIVATE "${GOPRIVATE:-}"

echo "*** Replacing PKG_CONFIG_PATH:"
launchctl getenv PKG_CONFIG_PATH
launchctl setenv PKG_CONFIG_PATH "${PKG_CONFIG_PATH:-}"

echo "*** Replacing CGO_CFLAGS:"
launchctl getenv CGO_CFLAGS
launchctl setenv CGO_CFLAGS "${CGO_CFLAGS:-}"

echo "*** Replacing CGO_LDFLAGS:"
launchctl getenv CGO_LDFLAGS
launchctl setenv CGO_LDFLAGS "${CGO_LDFLAGS:-}"
