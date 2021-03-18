#!/bin/bash
set -Eeuxo pipefail

# Convenience script to run tests in one command, and without needing to source
# init-env.sh
#
# useful args:
#   -a               force rebuild
#   -count=1         disable cache
#   -timeout=99999s  wait longer for test to finish

avp_proj_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$avp_proj_dir"

avp_args=("$@")
set -- # unset args before sourcing
source init-env.sh "$avp_proj_dir/../content-fabric"

# FIXME run all go tests
#go test "${avp_args[@]:-}" ./...

# run single unit test
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe/linear -run TestMock1

# run package tests
go test "${avp_args[@]:-}" github.com/qluvio/avpipe
