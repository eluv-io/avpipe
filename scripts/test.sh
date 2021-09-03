#!/bin/bash
set -Eeuxo pipefail

# Convenience script to run tests in one command, and without needing to source
# init-env.sh
#
# useful args:
#   -a               force rebuild
#   -count=1         disable cache
#   -short           skip long-running tests (HEVC/H265)
#   -timeout=99999s  wait longer for test to finish
#   -v               list all of the tests and their results

avp_proj_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
cd "$avp_proj_dir"

avp_args=("$@")
set -- # unset args before sourcing
source init-env.sh "$avp_proj_dir/../content-fabric"

# run all go tests
go test "${avp_args[@]:-}" ./...

# run single unit test
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestMezAudio\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestMezVideo\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestSingleABRTranscode\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestSingleABRTranscodeByStreamId\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestSingleABRTranscodeWithWatermark\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestSingleABRTranscodeWithOverlayWatermark\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestV2SingleABRTranscode\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestV2SingleABRTranscodeIOHandler\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestV2SingleABRTranscodeCancelling\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestNvidiaABRTranscode\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestConcurrentABRTranscode\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAAC2AACMezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAC3TsAC3MezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAC3TsAACMezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestMP2TsAACMezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestDownmix2AACMezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTest2Mono1Stereo\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTest2Channel1Stereo\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestIrregularTsMezMaker_1001_60000\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestIrregularTsMezMaker_1_24\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestIrregularTsMezMaker_1_10000\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAVPipeMXF_H265MezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAVPipeHEVC_H264MezMaker\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAVPipeHEVC_H265ABRTranscode\E$"
#FIXME go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestAVPipeStats\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestABRMuxing\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestMarshalParams\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestUnmarshalParams\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestProbe\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestProbeWithData\E$"
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe -run "^\QTestExtractImages"

# run package tests
#go test "${avp_args[@]:-}" github.com/qluvio/avpipe

