#!/bin/bash
#
# Run avpipe tests with tcmalloc heap leak detection (minimal profiling)
#
# Usage:
#   ./run_tests_leak_check.sh [test_name]
#
# Examples:
#   ./run_tests_leak_check.sh                    # Run all tests
#   ./run_tests_leak_check.sh TestMXF_H265MezMaker  # Run specific test
#

set -e

# Important: We need to build the test binary FIRST without LD_PRELOAD,
# otherwise tcmalloc will check the compiler itself for leaks!

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
HEAPCHECK="${HEAPCHECK:-normal}"

# Disable verbose output unless requested
TCMALLOC_VERBOSE="${TCMALLOC_VERBOSE:-0}"

# Disable pprof symbol resolution to avoid errors
# The -gcflags will provide symbols in stack traces anyway
PPROF_PATH="/bin/false"

echo "======================================"
echo "Running avpipe tests with leak detection"
echo "======================================"
echo "Using: $TCMALLOC_LIB"
echo "HEAPCHECK: $HEAPCHECK"
echo "======================================"
echo ""

# Step 1: Build the test binary WITHOUT tcmalloc to avoid checking the compiler
echo "Building test binary with debug symbols..."
if [ -z "$1" ]; then
    go test -c -gcflags="all=-N -l" -o avpipe.test
else
    go test -c -gcflags="all=-N -l" -o avpipe.test
fi

if [ $? -ne 0 ]; then
    echo "ERROR: Failed to build test binary"
    exit 1
fi
echo "Build successful!"
echo ""

# Step 2: Run the test binary with tcmalloc preloaded
echo "Running tests with leak detection..."
if [ -z "$1" ]; then
    LD_PRELOAD="$TCMALLOC_LIB" \
    HEAPCHECK="$HEAPCHECK" \
    PPROF_PATH="$PPROF_PATH" \
    TCMALLOC_VERBOSE="$TCMALLOC_VERBOSE" \
    ./avpipe.test -test.v -test.timeout 30m
else
    LD_PRELOAD="$TCMALLOC_LIB" \
    HEAPCHECK="$HEAPCHECK" \
    PPROF_PATH="$PPROF_PATH" \
    TCMALLOC_VERBOSE="$TCMALLOC_VERBOSE" \
    ./avpipe.test -test.v -test.timeout 30m -test.run "$1"
fi

EXIT_CODE=$?
echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo "✓ Tests passed with no memory leaks detected"
else
    echo "✗ Tests failed or memory leaks detected (exit code: $EXIT_CODE)"
fi

exit $EXIT_CODE
