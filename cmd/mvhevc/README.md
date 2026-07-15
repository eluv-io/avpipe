
# MV-HEVC TOOLS

Produce an MP4 with full spatial information (including Apple specifics) from two
separate 'left-eye' and 'right-eye' sources (ProRes or MP4).

Supports SDR and HDR.

Basic flow:

- SDR: use `mvhevc_encoder` to produce a raw MV-HEVC file from two source files (left eye, right eye), then use `mvhevc add` to create an mp4 from the raw hevc file.
- HDR (10bit): use `mvhevc_apple` to produce an MV-HEVC mov file from two source file (left eye, right eye), the use `mvhevc fix` to add missing mp4 signaling and create the final mp4 file.


### 1) Encode left/right sources to raw MV-HEVC

`mvhevc_encoder` writes a raw Annex B MV-HEVC elementary stream.

```bash
./bin/mvhevc_encoder \
  -keyint 48 \
  -bframes 0 \
  -fps 24000/1001 \
  -w 2560 -h 1440  -bitrate 8500 \
  left_eye.mov \
  right_eye.mov \
  output_mvhevc.hevc
```

Where:

- `-fps` is optional if the left-eye input reports the correct frame rate.
  - use `24000/1001` for 23.976 fps, `24/1` for 24 fps, etc.

Important: libx264 only supports 8bit MV-HEVC (for 10 bit we use the Apple toolkit)


### Direct Apple MV-HEVC encode

`mvhevc_apple` can encode separate left/right sources directly to a `.mov` using AVFoundation/VideoToolbox.

For 8-bit SDR output from 10-bit SDR ProRes sources:

```bash
./bin/mvhevc_apple \
  -bitdepth 8 \
  -profile main \
  -keyint 48 \
  -bframes 0 \
  -fps 24000/1001 \
  -bitrate 8500 \
  left_eye_sdr.mov \
  right_eye_sdr.mov \
  output_spatial_sdr.mov
```

The SDR encoder preserves color primaries, transfer characteristics, and matrix
coefficients from the ProRes track metadata. The left and right sources must
have compatible color metadata. Do not use `-hdr`, `-master-display`, or
`-max-cll` for SDR output.

For 10-bit HDR10 output:

```bash
./bin/mvhevc_apple \
  -bitdepth 10 \
  -profile main10 \
  -level 5.2 \
  -hdr \
  -master-display "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10)" \
  -max-cll "0,0" \
  -keyint 48 \
  -bframes 0 \
  -fps 24/1 \
  -w 2560 -h 1440  -bitrate 8500 \
  -quality 0.75 \
  -duration 30.0 \
  left_eye_hdr.mov \
  right_eye_hdr.mov \
  output_spatial_hdr10.mov
```

The Apple encoder accepts the same options as `mvhevc_encoder`, but x265-only
options such as `-crf`, `-preset`, `-tune`, `-level`, `-hightier`, `-bufsize`,
and `-scenecut` are currently accepted for compatibility and ignored.
Use `-duration <seconds>` to encode only the first portion of the source.
Use `-quality <0.0-1.0>` to pass a VideoToolbox compression quality hint; it is
optional and defaults to VideoToolbox's native behavior.
For `mvhevc_apple`, requested bitrates above 10 Mbps are automatically adjusted
up before being passed to VideoToolbox: +10% above 10 Mbps, +20% above 25 Mbps,
and +25% above 35 Mbps. If `-maxrate` is set, the adjusted bitrate is capped to
that value. VideoToolbox's stereo MV-HEVC encoder rejects its hard
`DataRateLimits` property, so `-maxrate` is not a guaranteed VBV peak.

To encode every video rung from an ABR profile in one pass, add
`-abr-profile`. The profile supplies each rung's bitrate, width, height,
profile, and level; `segment_specs.video.bit_depth` supplies bit depth. An
optional per-rung `max_bit_rate` caps the adjusted average target. An explicit
CLI `-maxrate` overrides `max_bit_rate` for every rung. It is not a guaranteed
VBV peak for stereo MV-HEVC.

```bash
./bin/mvhevc_apple \
  -abr-profile /Users/serban/ELV/CODE/elv-utils-js/abr/abr_mv_hevc_hdr_25-30.json \
  -hdr \
  -master-display "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10)" \
  -max-cll "0,0" \
  -keyint 48 \
  -bframes 0 \
  -fps 24000/1001 \
  -duration 67 \
  left_eye_hdr.mov \
  right_eye_hdr.mov \
  output_mvhevc_hdr.mov
```

This produces one output per video rung using the output argument as a base,
for example `output_mvhevc_hdr_2560x1440@11.50.mov`. If the profile has
`no_upscale: true`, rungs larger than the source are skipped. The output
argument may also use `%w`, `%h`, `%b`, `%m`, and `%n` placeholders for width,
height, kbps, Mbps, and rung index.

Always 'fix' the output of the Apple encoder:

```
./bin/mvhevc fix output_spatial_hdr10.mov output_spatial_hdr10.mp4
```

The 'fix' command ads oinf/linf MV-HEVC signaling and if the input is HDR10, ads the 'colr' box. 
The Apple toolkig doesn't create these.


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
