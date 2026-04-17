Purpose
-------
This file gives concise, actionable guidance to AI coding agents working in the `avpipe` repository so they can be productive immediately.

Quick summary
-------------
- Mixed-language codebase: Go + C. Native libraries and bindings are central: build steps must include the C components before running Go programs or tests.
- Key entry points and packages: [avpipe.go](avpipe.go), [goavpipe/goavpipe.go](goavpipe/goavpipe.go), [libavpipe.c](libavpipe.c) and [avpipe.c](avpipe.c). Tools and apps live under `elvxc/` and `cmd/`.
- Tests: many Go tests under packages like `live/`, `mp4e/`, `ts/`. Use `go test ./...` and the provided shell helpers.

Big picture (what to know fast)
--------------------------------
- Architecture: native C library (core transport/muxing code) + Go wrappers and tooling. The C code implements low-level muxing/transport logic; the Go code provides higher-level tooling, validation, and test harnesses.
- Dataflow: media input -> native mux/transport (C) -> wrapper/CLI (Go). Look at [libavpipe.c](libavpipe.c)/[avpipe.c](avpipe.c) for low-level behavior and [goavpipe/goavpipe.go](goavpipe/goavpipe.go) for the cgo bridge.
- Where to change behavior: change algorithmic or performance-sensitive code in C sources under the repo root or `lib/`; change orchestration, tests, and CLI features in the Go packages (`elvxc/`, `cmd/`, `goavpipe/`).

Developer workflows (commands to run)
------------------------------------
- Build native + Go (recommended):
  - `make` (builds C libs and top-level binaries)
  - `go build ./...` (build Go components)
- Tests and validation:
  - `go test ./...` — run all Go tests
  - `./run_tests.sh` — repository test harness (used by CI)
  - `./run_live_tests.sh` — live/stress test harness
- Quick smoke run: build then run `elvxc` tool from `bin/` or `elvxc/elvxc` (see `elvxc/Makefile` and [elvxc/main.go](elvxc/main.go)).

Project-specific conventions & patterns
-------------------------------------
- Mixed-language edits: when editing behavior spanning C and Go, update the C implementation first, then the cgo bridge in `goavpipe/` and Go consumer code. Keep API shape stable in headers under `include/` (e.g. `avpipe_utils.h`, `avpipe.h`).
- Logging: centralized in [log.go](log.go). Follow its patterns for structured logs used across Go packages and tests.
- Errors: see [avpipe_errors.go](avpipe_errors.go) for repository error conventions. Match error constants and handling when adding new error conditions.
- Tests produce artifacts in `test_out/` — CI expectations and golden files live there; follow existing test patterns when adding new tests.

Integration points & external dependencies
----------------------------------------
- Native code interacts with libav/FFmpeg APIs (check C files and build flags). Some tests/tools may require `ffmpeg` installed in the environment.
- Tools and validators: `cmd/fmp4-validate/`, `cmd/mez-validator/` — look here for validation logic and how outputs are consumed.

Editing and PR guidance for agents
----------------------------------
- Be conservative with cross-language API changes. If adding or changing a C header, update all dependents: header, C impl, `goavpipe` bridge, and Go callers.
- Preserve license files at top-level (COPYING.*). Do not change licensing lines.
- Run `make` and `go test ./...` locally before proposing changes. Use `./run_tests.sh` if altering test harnesses.

Examples (where to look for common patterns)
--------------------------------------------
- cgo bridge and Go wrappers: [goavpipe/goavpipe.go](goavpipe/goavpipe.go)
- Top-level Go app and entrypoints: [avpipe.go](avpipe.go), [elvxc/main.go](elvxc/main.go)
- Native core: [libavpipe.c](libavpipe.c), [avpipe.c](avpipe.c)
- Tests + harnesses: `live/`, `mp4e/`, `ts/`, and scripts `run_tests.sh`, `run_live_tests.sh`.

When you are unsure
-------------------
- Check the repository `Makefile` and package `Makefile`s (e.g. `elvxc/Makefile`) to understand build targets and expected artifacts.
- Search for similar patterns (error constants, logging call sites) before introducing new conventions.

If anything above is unclear or you'd like more examples (build logs, typical failing tests, or CI commands), tell me which area to expand and I will iterate.
