#!/bin/bash
set -Eeuxo pipefail

fabric_dir=$1

set --  # unset args before we source
source "$( dirname "${BASH_SOURCE[0]}" )/../init-env.sh" $fabric_dir

# Set environment for new processes started by launchd/Spotlight
launchctl setenv GOPATH ${GOPATH:-}
launchctl setenv ELV_TOOLCHAIN_DIST_PLATFORM ${ELV_TOOLCHAIN_DIST_PLATFORM:-}
launchctl setenv CGO_CFLAGS "${CGO_CFLAGS:-}"
launchctl setenv CGO_LDFLAGS "${CGO_LDFLAGS:-}"
