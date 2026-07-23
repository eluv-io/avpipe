package avpipe

import "embed"

// The cgo build compiles the avpipe C library from source: the package-level C
// files (avpipe.c, libavpipe.c) are a unity build that #includes the .c files
// under libavpipe/src and utils/src. Go's build cache tracks the package-level
// cgo files but NOT the files they #include, so a change to e.g.
// libavpipe/src/avpipe_xc.c would otherwise be a silent cache hit and never
// recompile (you would test a stale binary).
//
// Embedding those included sources (and their headers) here makes them
// build-cache inputs, so editing any of them forces a rebuild of this package —
// and thus a cgo recompile — with no `make` step and no `go clean -cache`.
//
// The variable is blank and never read, so the linker's dead-data elimination
// discards the embedded bytes: this adds nothing to the binary (the payload is a
// build-cache input, computed at compile time, independent of what survives
// linking). Verified: binary size is identical whether the embedded payload is a
// few bytes or the whole source tree.
//
//go:embed libavpipe/src/*.c libavpipe/include/*.h utils/src/*.c utils/include/*.h
var _ embed.FS
