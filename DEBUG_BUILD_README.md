# Debug Build Guide for avpipe

## Overview
This guide explains how to build avpipe with debug symbols for debugging with GDB, analyzing memory leaks, and investigating crashes.

## Quick Start

### Build Debug Version
```bash
make avpipe-debug
```

This creates:
- `avpipe.debug` - Go binary with debug symbols
- C libraries in subdirectories built with `-g -O0 -DDEBUG` flags

### Run with GDB
```bash
gdb ./avpipe.debug
(gdb) run <arguments>
```

### Run Tests with Debug Symbols
```bash
go test -v -gcflags="all=-N -l" -run TestMXF_H265MezMaker
```

## Debug Build Features

### Go Binary Debug Flags
- `-gcflags="all=-N -l"`
  - `-N` - Disables optimizations (preserves variable values)
  - `-l` - Disables inlining (preserves function call stack)

### C Library Debug Flags
- `-g` - Includes debug symbols (DWARF format)
- `-O0` - Disables optimizations (preserves code structure)
- `-DDEBUG` - Defines DEBUG macro for conditional debug code

## Debug Build Control

The `DEBUG` variable controls C compilation:

### Build Everything with Debug Symbols
```bash
make DEBUG=1
```

### Build Only C Libraries with Debug Symbols
```bash
make debug-libs
```

### Build Individual Components with Debug
```bash
make -C utils DEBUG=1
make -C libavpipe DEBUG=1
make -C exc DEBUG=1
make -C elvxc DEBUG=1
```

## Debugging Workflow

### 1. Build with Debug Symbols
```bash
make clean
make avpipe-debug
```

### 2. Run with GDB
```bash
# Basic debugging
gdb ./avpipe.debug

# With arguments
gdb --args ./avpipe.debug --config sample.json

# Attach to running process
gdb -p <pid>
```

### 3. Useful GDB Commands
```gdb
# Set breakpoint
break avpipe.c:123
break xcTranscode

# Run program
run

# Step through code
next          # Step over
step          # Step into
continue      # Continue execution

# Inspect variables
print variable_name
print *pointer
info locals

# Backtrace
bt            # Full backtrace
bt full       # Backtrace with local variables

# Core dump analysis
gdb ./avpipe.debug core
```

## Debugging with tcmalloc

Combine debug builds with memory leak detection:

```bash
# Build debug version
make avpipe-debug

# Run tests with tcmalloc and debug symbols
LD_PRELOAD=/lib/x86_64-linux-gnu/libtcmalloc.so.4 \
HEAPCHECK=normal \
PPROF_PATH=/bin/false \
go test -v -gcflags="all=-N -l" -run TestMXF_H265MezMaker
```

Or use the provided script:
```bash
./run_tests_leak_check.sh -run TestMXF_H265MezMaker
```

## Symbol Verification

### Check Go Binary for Symbols
```bash
go tool nm ./avpipe.debug | grep main.main
objdump -g ./avpipe.debug | head -20
```

### Check C Libraries for Symbols
```bash
objdump -g lib/libavpipe.a | head -20
nm -C lib/libavpipe.a | grep avpipe
```

### Check Shared Library Symbols
```bash
nm -D /lib/x86_64-linux-gnu/libtcmalloc.so.4 | grep malloc
```

## Core Dump Analysis

### Enable Core Dumps
```bash
ulimit -c unlimited
```

### Generate Core on Crash
```bash
./avpipe.debug <args>
# If it crashes, core file is created
```

### Analyze Core Dump
```bash
gdb ./avpipe.debug core
(gdb) bt full
(gdb) info threads
(gdb) thread <n>
(gdb) frame <n>
(gdb) print <variable>
```

## Performance Considerations

### Debug Build Performance
- Debug builds are **significantly slower** than release builds
- `-O0` disables all optimizations
- Use debug builds only for debugging, not production

### Storage Requirements
- Debug symbols increase binary size substantially
- Plan for 5-10x larger binaries
- Temporary build artifacts also larger

## Common Issues

### Missing Symbols in Backtrace
**Problem**: Stack traces show `??` or memory addresses
**Solution**: Ensure built with `-g` flag and `-gcflags="all=-N -l"`

### Optimized Code Behavior
**Problem**: Variables show `<optimized out>` in GDB
**Solution**: Use `-O0` flag (already set in debug builds)

### CGO Symbol Resolution
**Problem**: Can't see C function names in Go stack traces
**Solution**: Both Go and C must be built with debug flags

### PPROF Errors During Leak Detection
**Problem**: tcmalloc tries to run pprof but fails
**Solution**: Set `PPROF_PATH=/bin/false` (included in scripts)

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make avpipe` | Build release version (optimized, no symbols) |
| `make avpipe-debug` | Build debug version (symbols, no optimization) |
| `make debug-libs` | Build only C libraries with debug symbols |
| `make clean` | Clean all build artifacts |
| `make test-tcmalloc` | Run tests with tcmalloc leak detection |

## Environment Variables

### Debug Build Control
- `DEBUG=1` - Enable debug flags in C compilation

### GDB Configuration
- `set print pretty on` - Pretty-print structures
- `set pagination off` - Disable paging for long output
- `set logging on` - Log GDB output to file

### Core Dump Configuration
- `ulimit -c unlimited` - Enable unlimited core dump size
- `sysctl kernel.core_pattern` - Check core dump location

## See Also

- [TCMALLOC_README.md](TCMALLOC_README.md) - Memory leak detection setup
- [TCMALLOC_SETUP_SUMMARY.md](TCMALLOC_SETUP_SUMMARY.md) - Quick tcmalloc reference
- [doc/dev.md](doc/dev.md) - Development documentation

## Example Debug Session

```bash
# 1. Build debug version
make clean
make avpipe-debug

# 2. Run specific test with leak detection and symbols
LD_PRELOAD=/lib/x86_64-linux-gnu/libtcmalloc.so.4 \
HEAPCHECK=normal \
PPROF_PATH=/bin/false \
go test -v -gcflags="all=-N -l" -run TestMXF_H265MezMaker 2>&1 | tee test.log

# 3. If crash occurs, analyze with GDB
gdb ./avpipe.test core
(gdb) bt full
(gdb) frame 5
(gdb) print *ctx
(gdb) info locals

# 4. Set breakpoint and re-run
gdb --args ./avpipe.test -test.run TestMXF_H265MezMaker
(gdb) break avpipe.c:456
(gdb) run
(gdb) next
(gdb) print variable_name
```

## Tips

1. **Always clean before debug builds**: `make clean` prevents mixing debug and release object files
2. **Use test binaries**: Go creates `<package>.test` binary that can be run directly or with GDB
3. **Combine with tcmalloc**: Debug symbols make tcmalloc leak reports much more useful
4. **Check symbols early**: Verify symbols exist before lengthy debug sessions
5. **Save GDB sessions**: Use `set logging on` to record debugging sessions
