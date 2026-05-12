
# MV-HEVC TOOLS

## Full MV-HEVC process

Produce an MP4 with full spatial information (including Apple specifics) from two
separate 'left-eye' and 'right-eye' sources (ProRes or MP4).


### 1) Encode left/right sources to raw MV-HEVC

`mvhevc_encoder` writes a raw Annex B MV-HEVC elementary stream.

```bash
./bin/mvhevc_encoder \
  -keyint 48 \
  -bframes 0 \
  -fps 24000/1001 \
  left_eye.mov \
  right_eye.mov \
  output_mvhevc.hevc
```

Where:

- `-fps` is optional if the left-eye input reports the correct frame rate.
  - use `24000/1001` for 23.976 fps, `24/1` for 24 fps, etc.

### HDR10 encode example

For HDR10 output, enable the 10-bit pipeline and HDR10 x265 settings during the
raw MV-HEVC encode step.

```bash
./bin/mvhevc_encoder \
  -bitdepth 10 \
  -hdr \
  -max-cll "1000,400" \
  -master-display "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)" \
  -keyint 48 \
  -bframes 0 \
  -fps 24000/1001 \
  left_eye_hdr.mov \
  right_eye_hdr.mov \
  output_mvhevc_hdr10.hevc
```

Where:

- `-bitdepth 10` converts decoded input frames to 10-bit `yuv420p10le` before
  passing them to x265.
- `-hdr` enables the HDR10 x265/VUI settings: `hdr10=1`, `hdr10-opt=1`,
  `colorprim=bt2020`, `transfer=smpte2084`, and `colormatrix=bt2020nc`.
- `-max-cll` and `-master-display` pass HDR10 mastering metadata to x265.
- The current MP4 `add` step does not require extra HDR flags; it packages the
  encoded MV-HEVC stream as-is.

Requires an x265 multiview build that supports 10-bit MV-HEVC. Some x265
multiview builds enforce 8-bit-only output and will reject this combination.

### Direct Apple MV-HEVC encode

On macOS 14+, `mvhevc_apple` can encode separate left/right sources directly to
a `.mov` or `.mp4` using AVFoundation/VideoToolbox. This avoids the raw `.hevc`
intermediate and the `mvhevc add` packaging step.

```bash
./bin/mvhevc_apple \
  -bitdepth 10 \
  -profile main10 \
  -hdr \
  -master-display "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10)" \
  -max-cll "0,0" \
  -keyint 48 \
  -bframes 0 \
  -fps 24/1 \
  left_eye_hdr.mov \
  right_eye_hdr.mov \
  output_spatial_hdr10.mov
```

The Apple encoder accepts the same options as `mvhevc_encoder`, but x265-only
options such as `-crf`, `-preset`, `-tune`, `-level`, `-hightier`, `-bufsize`,
and `-scenecut` are currently accepted for compatibility and ignored.


### 2) Package the raw MV-HEVC stream into MP4

Create an MP4 including multi-view and spatial information.

```bash
./bin/mvhevc add \
  -fps 23.976 \
  -spatial \
  -baseline 63500 \
  -hfov 63500 \
  -hero left \
  output_mvhevc.hevc \
  output_spatial.mp4
```

where:
- `-spatial` adds Apple spatial metadata (`vexu`/`hfov`).

HDR10 packaging uses the same command form:

```bash
./bin/mvhevc add \
  -fps 23.976 \
  -spatial \
  -baseline 63500 \
  -hfov 63500 \
  -hero left \
  output_mvhevc_hdr10.hevc \
  output_spatial_hdr10.mp4
```

### 3) Inspect/test the MP4

```bash
./bin/mvhevc info -idr output_spatial.mp4
```

Expected signs of success:

- `VPS: layers=2 views=2 multiLayer=true`
- `lhvC (enhancement layer config)`
- `oinf (Operating Points Information)`
- `linf (Layer Information)`
- `trgr (Track Group)`
- If `-spatial` was used: `vexu (Spatial Video)` and `hfov`

Optional avpipe 'fmp4 ingest' test:

```bash
./bin/exc \
  -f output_spatial.mp4 \
  -xc-type video \
  -format fmp4-segment \
  -seg-duration 30.03 \
  -bypass 1
```

Go package/CLI tests:

```bash
go test ./mp4e ./mp4e/mvhevc ./cmd/mvhevc/mvhevc
```
