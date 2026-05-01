# tnt-uniqfeed

This directory contains the standalone uniqFEED-enabled transcoding command copied from the working FFmpeg example integration.

This command is intended to be built and run inside the uniqFEED Docker image because the runtime dependencies include Vulkan and related libraries that are not expected to exist on the host.

## Build

Required environment:

```sh
export FFMPEG_DIST=/path/to/FFmpeg/dist
```

`FFMPEG_DIST` can point to either:

```sh
/path/to/FFmpeg/dist
/path/to/FFmpeg
```

The second form is the FFmpeg build tree layout, which matches the previously working setup under `FFmpeg/doc/examples/transcoding.c`.

Optional environment:

```sh
export PKG_CONFIG_PATH=/path/to/ffmpeg/pkgconfig
export UF_BASE_IMAGE=uf_render_interface:ubuntu_22
```

If `FFMPEG_DIST` points at an installed dist layout and `PKG_CONFIG_PATH` is unset, the container build defaults to `$(FFMPEG_DIST)/lib/pkgconfig`.
If `FFMPEG_DIST` points at an FFmpeg build tree, pkg-config is not required.

Then build from the avpipe repository root:

```sh
make -C cmd/tnt-uniqfeed
```

Or invoke the wrapper directly:

```sh
FFMPEG_DIST=/path/to/FFmpeg/dist ./cmd/tnt-uniqfeed/uniqfeed-container.sh build
```

The binary is written to:

```sh
bin/tnt-uniqfeed
```

## Run

```sh
make -C cmd/tnt-uniqfeed run \
	INPUT=/abs/path/input.mp4 \
	OUTPUT=/abs/path/output.mp4 \
	PROJECT=/runtime/project \
	METADATA=/runtime/example_input
```

`run` builds `bin/tnt-uniqfeed` automatically if it is missing.

Or invoke the wrapper directly:

```sh
FFMPEG_DIST=/path/to/FFmpeg/dist ./cmd/tnt-uniqfeed/uniqfeed-container.sh run \
	/abs/path/input.mp4 /abs/path/output.mp4 /runtime/project /runtime/example_input
```

Default environment behavior:

```sh
UF_RENDERLIB_PASSTHROUGH_ON_FAILURE=1
```

`run` now enables passthrough-on-failure by default. That mode disables uniqFEED and continues with the original filtered frames when the render path hits a recoverable failure, including when sample metadata is exhausted.

To disable that behavior explicitly:

```sh
UF_RENDERLIB_PASSTHROUGH_ON_FAILURE=0 make -C cmd/tnt-uniqfeed run ...
```

## Shell

To debug inside the uniqFEED image:

```sh
FFMPEG_DIST=/path/to/FFmpeg/dist ./cmd/tnt-uniqfeed/uniqfeed-container.sh shell
```

## Notes

- The current uniqFEED integration is guarded to exact `1280x720` video frames.
- The packaged metadata may cover only a subset of frames; with passthrough enabled, uniqFEED is disabled when metadata is exhausted.
- This command is currently standalone and is not wired into the top-level avpipe `Makefile`.
- The host `FFMPEG_DIST` directory is mounted into the container so the standalone binary can build and run against the supplied FFmpeg toolchain.