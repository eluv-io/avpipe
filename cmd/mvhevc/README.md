
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
