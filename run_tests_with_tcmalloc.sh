#!/bin/bash
#
# Run avpipe tests with tcmalloc heap checking
#
# Usage:
#   ./run_tests_with_tcmalloc.sh [test_name]
#
# Examples:
#   ./run_tests_with_tcmalloc.sh                    # Run all tests
#   ./run_tests_with_tcmalloc.sh TestMXF_H265MezMaker  # Run specific test
#

set -e

# Find tcmalloc library
TCMALLOC_LIB=""
for lib in libtcmalloc.so.4 libtcmalloc.so libtcmalloc_minimal.so.4 libtcmalloc_minimal.so; do
    LIB_PATH=$(ldconfig -p | grep "$lib" | awk '{print $NF}' | head -1)
    if [ -n "$LIB_PATH" ] && [ -f "$LIB_PATH" ]; then
        TCMALLOC_LIB="$LIB_PATH"
        break
    fi
done

if [ -z "$TCMALLOC_LIB" ]; then
    echo "ERROR: tcmalloc library not found!"
    echo "Please install: sudo apt-get install libtcmalloc-minimal4"
    exit 1
fi

# tcmalloc heap checker environment variables
# HEAPCHECK=normal   - Check for memory leaks
# HEAPCHECK=strict   - More aggressive leak checking
# HEAPCHECK=draconian - Most aggressive leak checking
export HEAPCHECK="${HEAPCHECK:-normal}"

# Where to write the heap profile
export HEAPPROFILE="${HEAPPROFILE:-/tmp/avpipe_heap}"

# Enable tcmalloc verbose output
export TCMALLOC_VERBOSE="${TCMALLOC_VERBOSE:-1}"

# Dump heap stats at exit
export MALLOCSTATS="${MALLOCSTATS:-1}"

# Preload tcmalloc
export LD_PRELOAD="$TCMALLOC_LIB${LD_PRELOAD:+:$LD_PRELOAD}"

# Disable pprof symbol resolution to avoid the -symbols error
# tcmalloc will still report leaks, just without attempting to symbolize
export PPROF_PATH="${PPROF_PATH:-/bin/false}"

# Set pprof path if available and user wants symbolization
if [ "${USE_PPROF:-0}" = "1" ] && command -v pprof &> /dev/null; then
    export PPROF_PATH=$(command -v pprof)
fi

# Go test flags for better debugging
GO_TEST_FLAGS="${GO_TEST_FLAGS:--v -timeout 30m}"

echo "======================================"
echo "Running avpipe tests with tcmalloc"
echo "======================================"
echo "TCMALLOC_LIB: $TCMALLOC_LIB"
echo "LD_PRELOAD: $LD_PRELOAD"
echo "HEAPCHECK: $HEAPCHECK"
echo "HEAPPROFILE: $HEAPPROFILE"
echo "TCMALLOC_VERBOSE: $TCMALLOC_VERBOSE"
echo "GO_TEST_FLAGS: $GO_TEST_FLAGS"
echo "======================================"
echo ""

# Run the tests with symbols and debug info
# -gcflags="all=-N -l" keeps symbols for better stack traces
if [ -z "$1" ]; then
    # Run all tests
    go test $GO_TEST_FLAGS -gcflags="all=-N -l"
else
    # Run specific test
    go test $GO_TEST_FLAGS -gcflags="all=-N -l" -run "$1"
fi

EXIT_CODE=$?

echo ""
echo "======================================"
echo "Test run complete (exit code: $EXIT_CODE)"
echo "======================================"
if [ -f "${HEAPPROFILE}"* ]; then
    echo "Heap profiles written to: ${HEAPPROFILE}*"
    echo ""
    echo "To analyze heap profiles, use:"
    echo "  pprof --text ./avpipe.test ${HEAPPROFILE}.*.heap"
fi

exit $EXIT_CODE
