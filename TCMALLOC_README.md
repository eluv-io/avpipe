# Memory Leak Detection with tcmalloc

This directory contains tools for detecting memory leaks in avpipe using Google's tcmalloc.

## Prerequisites

Install tcmalloc (Google perftools):

```bash
# Ubuntu/Debian
sudo apt-get install libtcmalloc-minimal4 google-perftools

# For heap profiling analysis, also install:
sudo apt-get install google-perftools
```

## Usage

### 1. Quick Leak Detection (Recommended)

Run tests with basic leak detection:

```bash
# Test all
./run_tests_leak_check.sh

# Test specific test
./run_tests_leak_check.sh TestMXF_H265MezMaker

# More aggressive leak checking
HEAPCHECK=strict ./run_tests_leak_check.sh TestMXF_H265MezMaker
```

### 2. With Heap Profiling

Run tests with detailed heap profiling:

```bash
# Basic profiling
./run_tests_with_tcmalloc.sh TestProbe

# Strict leak checking
HEAPCHECK=strict ./run_tests_with_tcmalloc.sh TestProbe

# Most aggressive
HEAPCHECK=draconian ./run_tests_with_tcmalloc.sh TestProbe
```

### 3. Using Make Targets

```bash
# Quick leak detection
make test-tcmalloc

# Strict leak checking
make test-tcmalloc-strict
```

## Interpreting Results

### Exit Codes
- `0`: No leaks detected, tests passed
- `1`: Tests failed or memory leaks detected

### Leak Detection Levels

- **`HEAPCHECK=normal`** (default): Basic leak detection
- **`HEAPCHECK=strict`**: More thorough, catches more leaks but may have false positives
- **`HEAPCHECK=draconian`**: Most aggressive, highest false positive rate

### Understanding Output

When leaks are detected, you'll see output like:

```
Leak check _main_ detected leaks of 9342 bytes in 7 objects
The 5 largest leaks:
  Leak of 4096 bytes in 1 objects allocated from:
    @ 0x7f8b9c8d1234
    @ 0x7f8b9c8d5678
```

This indicates:
- Total leaked memory: 9342 bytes
- Number of leaked objects: 7
- Stack traces showing where memory was allocated

## Analyzing Heap Profiles

If heap profiles are generated (`/tmp/avpipe_heap*.heap`):

```bash
# Text summary
pprof --text ./avpipe.test /tmp/avpipe_heap*.heap

# Interactive web interface
pprof -http=:8080 ./avpipe.test /tmp/avpipe_heap*.heap

# Generate PDF
pprof --pdf ./avpipe.test /tmp/avpipe_heap*.heap > heap.pdf
```

## Common Issues

### "No such file or directory" for tcmalloc
Install the library:
```bash
sudo apt-get install libtcmalloc-minimal4
```

### False Positives
- Some libraries may report false leaks (especially with CGO)
- Use `HEAPCHECK=normal` (default) to reduce false positives
- Known false positives can be suppressed (see tcmalloc documentation)

### Tests Timeout
- Heap checking adds overhead
- Increase timeout: `-timeout 60m`

## Environment Variables

- `HEAPCHECK`: Leak detection level (normal/strict/draconian)
- `HEAPPROFILE`: Where to write heap profiles (default: /tmp/avpipe_heap)
- `TCMALLOC_VERBOSE`: Enable verbose output (0/1)
- `MALLOCSTATS`: Dump malloc stats at exit (0/1)
- `LD_PRELOAD`: Path to tcmalloc library (auto-detected)

## CGO Integration

The avpipe.go file includes:
```go
// #cgo LDFLAGS: -ltcmalloc
```

This links tcmalloc into the Go binary, but using `LD_PRELOAD` (as done in the scripts) is more reliable and flexible.

## References

- [tcmalloc Heap Checker](https://gperftools.github.io/gperftools/heap_checker.html)
- [Google Performance Tools](https://github.com/gperftools/gperftools)
- [pprof Documentation](https://github.com/google/pprof)
