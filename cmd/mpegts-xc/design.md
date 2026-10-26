# mpegts-xc — MPEGTS video-PID downscaler

## Objective

Read an MPEGTS stream, transcode **only the video PID** (downscale), and emit a
transport stream that is **byte-structurally identical to the source** — same
PIDs, PAT, PMT, PCR, data/SCTE/private PIDs, descriptors and multiplex timing —
with only the video pictures changed.

## Why not a full FFmpeg remux

The avpipe C layer is container-oriented; its only output formats are
`dash / hls / mp4 / fmp4 / segment / image2` (`libavpipe/src/avpipe_xc.c`). The
`copy_mpegts` path runs input through FFmpeg's mpegts **muxer**
(`av_interleaved_write_frame`, `avpipe_copy_mpegts.c:345`), which:

- rebuilds PAT/PMT and **reassigns PIDs** — original PID numbers and PMT
  descriptors are lost;
- regenerates PCR and **re-interleaves** all streams — original PCR placement
  gone;
- drops PIDs it doesn't model (arbitrary data/SCTE/private PIDs).

It is a *remux*, not a passthrough, and therefore structurally cannot produce
"identical except the video PID". FFmpeg is the wrong **container** engine here.

## Chosen architecture — Go container + avpipe as a pure video codec

Do all container work in Go (reuse `broadcastproto` + `gots`), and use
`avpipe_xc` **only as a video elementary-stream transcoder**. We feed avpipe a
minimal video-only TS, let it decode → scale → re-encode, and then **throw away
its container**, keeping only the re-encoded video access units. The container
of record stays 100% our Go passthrough, so FFmpeg never touches our output
PIDs/PMT/PCR.

```
source TS ──► Go demux (gots) ──┬─► [all non-video packets] ───────────────┐
                                │                                          │
                                └─► video PID packets                      │ splice
                                       │  (+ copied PAT/PMT)               │ (in-place,
                                       ▼                                   │  null-padded)
                            avpipe_xc (custom AVIO in/out)                 │
                              decode → scale → re-encode                   │
                              format="segment" → mpegts out               │
                                       │                                   │
                                       ▼                                   ▼
                            Go parse avpipe output TS ──► re-encoded video AUs ──► output TS
```

### What we feed avpipe

Not bare video packets — the mpegts demuxer needs a program definition. We feed
a minimal valid single-program TS: **copied PAT + PMT + the video PID packets**
(PCR rides along if it is carried on the video PID). avpipe demuxes one video ES
and ignores the absent audio.

### What we keep from avpipe's output

We parse avpipe's segment TS, reassemble the video PES, and recover the coded
access units. Its PAT/PMT/PID/PCR choices are discarded.

## Timing — the two things that must be preserved

### 1. Presentation timing (PTS/DTS)

We do **not** require avpipe to reproduce timestamps bit-exactly. On splice we
write our own PES headers, so we **re-stamp each re-encoded access unit with the
original source frame's PTS/DTS** (which we already hold from demuxing the source
video PID). This reduces the requirement to a checkable one:

> avpipe must emit **exactly one coded frame per input frame, in a known order**,
> so output frame *i* maps to source frame *i*.

If we also constrain the encoder GOP structure to match the source (same GOP
size, same B-frame count, closed GOP), decode order is preserved and we can
reapply both PTS and DTS directly. A constant timestamp offset from avpipe is a
non-issue because we overwrite it.

### 2. Multiplex grid (byte/packet positions)

Re-stamping fixes presentation timing; keeping audio/PCR/data byte positions
fixed requires **in-place null-padding**:

- copy every non-video packet at its exact slot;
- lay re-encoded video into the source's video slots; pad surplus slots with
  null packets (PID `0x1FFF`);
- cap the video encoder bitrate ≤ source video bitrate so each window fits.

### The one thing to verify first

**Is PCR carried on the video PID?** (common in broadcast). If yes, the
PCR-bearing video packets' adaptation fields must be preserved in place — swap
payload only, never the PCR. `ffprobe -show_programs` on a representative source
answers this immediately.

## Reuse map

| Need | Reused from |
| --- | --- |
| UDP / RTP transport | `broadcastproto/transport` (`NewUDPTransport`, `Open() io.ReadCloser`) |
| TS packet parse, PID, CC, PCR, adaptation field | `gots/v2/packet` (as used in `broadcastproto/mpegts`, `smpte20xx`) |
| PAT/PMT discovery → video PID | `gots/v2/psi` (`NewPAT`/`ProgramMap`, `NewPMT`/`ElementaryStreams`) |
| Video decode → scale → re-encode | `avpipe.Xc` with `XcType=XcVideo`, `EncWidth/EncHeight`, custom `InputOpener`/`OutputOpener` |
| Segment/CC/PCR-aware passthrough skeleton | `smpte20xx/transport/mpegts.go`, `broadcastproto/mpegts/mpegts.go` |

## Scope

- **Input:** live UDP first (RTP later); file input later.
- **Output:** **file only** initially (re-multiplex grid offline). Live output
  is a later phase needing a bounded PTS-realignment buffer (transcode latency
  means the downscaled frame for source position *i* arrives after we've
  buffered the passthrough packets around it).

## Phases

- **Phase 0 (this commit):** Spike to prove the mechanism and settle the PTS/DTS
  question with real numbers.
  - UDP reader (reuse `broadcastproto/transport`).
  - Per-UDP-datagram debug log with per-packet classification (video vs other)
    and the discovered video PID.
  - A simple `avpipe_xc` loop: feed the video-only TS (PAT+PMT+video) to
    `avpipe.Xc` (`XcType=XcVideo`, downscale), with custom `InputOpener` (reads
    the forwarded packets) and `OutputOpener` (logs the output bytes).
  - Print as we process avpipe's output.
  - **Exit criteria:** confirm input/output video frame-count and ordering
    parity, and dump per-frame PTS/DTS for comparison.
- **Phase 1:** Go demux + byte-identical passthrough of all non-video packets
  (no video change) — the baseline.
- **Phase 2:** Video PES reassembly + synthesize video-only TS + drive avpipe
  via custom AVIO; capture output.
- **Phase 3:** Harvest re-encoded AUs, re-stamp PTS/DTS, re-packetize onto the
  original video PID, and time-domain interleave them with the passthrough
  "other" packets, regenerating PCR and video CC. See the detailed design below.
- **Phase 4:** Constant-bitrate output — cap the encoder below a target rate, add
  a pacer that emits at exactly the target by inserting null-packet padding, and
  move PCR onto the output (CBR) clock. See the detailed design below.
- **Phase 5:** Live output refinements — phase-lock the pacer to the source PCR
  (below), and RTP output.

## Phase 3 — interleave / splice (detailed design)

Phase 3 merges two streams into the output TS: the re-encoded video access units
coming back (late) from avpipe, and the passthrough "other" packets read from
the source. The merge is **time-domain**, not byte-exact: we preserve the clock
(PCR/PTS/DTS), and emit packets in source-time order, rather than reproducing the
source's exact byte offsets. This supersedes the earlier "byte-grid + null
padding" idea (which required the downscaled video to fit the old video's exact
packet slots). Strict CBR/byte reproduction is deferred to Phase 4.

### Master clock

One clock: 27 MHz STC, taken from PCR. PTS/DTS (90 kHz) convert by ×300; the lead
window and PCR cadence (ms) convert by ×27000. Everything below is compared in
27 MHz ticks.

### Queues / stores

While reading the source linearly we route each packet and maintain a running
source STC (latest PCR, interpolated by packet position between PCR samples).
Every packet is tagged with the STC at read time.

1. **videoCh** (exists) — video-PID packets + a copy of PAT/PMT → avpipe. We also
   capture each video PES's PTS/DTS (to re-stamp later) and, if PCR rides on the
   video PID, its PCR samples (see PCR below).
2. **otherQ** — every non-video packet (audio, data, a separate-PID PCR, PAT/PMT,
   null), in source order, each tagged with its source STC. This is the
   passthrough store. PAT/PMT go to *both* videoCh (avpipe must demux) and otherQ
   (output passthrough).
3. **videoAUQ** — re-encoded access units recovered by parsing avpipe's output,
   each `{es bytes, pts, dts}` re-stamped to the original source values.
4. **pcrTimeline** — ordered PCR samples from the PCR_PID (see PCR below).

### Q1 — storing the "other" unaltered TS

otherQ is a bounded FIFO handed reader → muxer. A Go channel works for the
hand-off, but the muxer must **peek the head's STC** to decide emit-vs-wait, and
channels can't peek. Two options:

- channel + a single "held" lookahead packet in the muxer (pull one, inspect its
  STC, emit or hold), or
- **(preferred)** a mutex-guarded ring buffer that supports peek, is bounded, and
  drops-oldest-with-a-counter on overflow.

Element granularity is per TS packet: `{188-byte packet, stcTag, isPcrPid}`. Size
the buffer to hold at least `(transcode latency + lead window)` worth of other
packets. The reader must **never block** on otherQ — it also feeds avpipe — so on
overflow we count drops (a sign video has starved), we don't stall the reader.

### Q2 — how far ahead we may emit "other"

A constant `MaxOtherLeadMs` (default e.g. 20 ms, tunable), held in 27 MHz ticks.
The muxer loop:

- while `otherQ.head.stc ≤ lastVideoStc + lead`: emit the head **byte-for-byte**
  (CC preserved);
- otherwise: take the next AU from videoAUQ, re-packetize and emit it on the
  video PID, set `lastVideoStc = AU.dts × 300`; repeat.

Bootstrap: before the first AU is known, hold "other" rather than racing ahead.

Tradeoffs: the lead must **exceed the source's natural audio-ahead-of-video
interleave**, or the muxer stalls/underflows waiting for video that legitimately
trails the audio in the source; too large costs buffer memory, latency, and
looser A/V grouping. 20 ms may be too tight for streams that packetize audio
hundreds of ms ahead — treat it as tunable and consider auto-deriving a floor
from the observed source `max(otherStc − videoStc)`. The lead also sets the
otherQ depth.

### Q3 — PCR: where to store, how to re-inject

**Case A — PCR on a separate PID.** Those packets are "other" → passed through
verbatim, in STC order. Nothing to regenerate; just don't reorder them (the lead
window already preserves order).

**Case B — PCR on the video PID** (our streams — `pcrOnVideoPID=true`). The
original PCR-bearing video packets are dropped along with the rest of the old
video, so PCR must be **regenerated** on the new video packets.

- **Store:** `pcrTimeline`, an ordered ring buffer of `(pcr27, srcStc)` samples
  captured from the video-PID adaptation fields, plus the measured insertion
  interval. It must span ≥ `transcode latency + lead` so we can interpolate the
  PCR at the STC of any video packet we are about to emit. (Conceptually a clock
  model — anchor PCR + rate — with the ring buffer handling discontinuities
  exactly.)
- **Re-inject:** while re-packetizing video, track output STC since the last
  emitted PCR; when it reaches the source cadence (and always keeping the
  interval ≤ the 40 ms spec max), mark the next video packet PCR-bearing: build
  an adaptation field, set `adaptation_field_control` to adaptation+payload, set
  the PCR flag, and write the 33-bit base (90 kHz) + 9-bit extension (27 MHz).
  The value is `interpolate(pcrTimeline, that packet's STC)`. Such a packet
  carries ~6–8 fewer payload bytes — the packetizer must account for it.
- **Consistency:** because video PTS/DTS are re-stamped to the source values and
  PCR is regenerated from the *same* source clock, the PCR↔PTS↔DTS relationships
  (and the T-STD buffer model) are preserved. The output is timing-faithful, not
  byte-identical. Mirror the PCR `discontinuity_indicator` if the source sets it.
- **Pacing:** for Phase-3 file output, monotonic PCR matching the PTS offset is
  enough. Pacing emission to wall-clock vs PCR belongs to the live phase.

### Continuity counters and null packets

- **Other PIDs (incl. PAT/PMT):** copied verbatim; CC stays valid because we emit
  them all, in order.
- **Video PID:** the muxer regenerates a 0–15 CC for every emitted video packet
  (we emit a different number of packets than the source).
- **Nulls (0x1FFF):** source nulls pass through as "other". Adding extra nulls to
  hold a constant bitrate (the downscaled video frees capacity) is a Phase-4
  concern; timing correctness here comes from PCR/PTS, not byte rate.

## Phase 4 — constant-bitrate output (detailed design)

Goal: emit a transport stream at an **exact** target bitrate. Because the
downscaled video + passthrough audio/data is well under the target, we make up
the difference with **null packets** (PID 0x1FFF) and **pace** the send so the
output is true CBR. Two cooperating parts: an encoder bitrate cap, and a pacer.

### Master clock

CBR ties the clock to byte position: at rate `R` bits/s, one 188-byte TS packet
occupies `Tpkt = 188*8 / R` seconds, i.e. `tpp = 188*8*27_000_000 / R` ticks at
27 MHz. The output packet index `n` defines the output STC: `outSTC(n) = anchor
+ n*tpp`. This is the clock a decoder recovers from PCR under constant-rate
delivery, so in CBR **PCR must be derived from `n`, not from DTS**.

### 1. Encoder bitrate cap (keep content under target)

`-target-bitrate R` is the exact output rate. `-reserve-bitrate` (explicit) is
the budget left for passthrough audio/data/PSI plus safety, so the transcoder is
capped at `video_max = R - reserve`. Set avpipe `VideoBitrate = video_max`,
`RcMaxRate = video_max`, and `RcBufferSize` (e.g. video_max * 1s) for a hard cap,
not just an average. The cap only needs to be conservative — the pacer enforces
the exact rate regardless — so the operator picks `reserve` to comfortably cover
the source's audio/data.

### 2. The pacer (CBR slots + null padding)

A new stage after the muxer, before the output sink:

- Emit exactly one TS packet per slot of `Tpkt`. Per slot: take a content packet
  from the muxer if one is ready (non-blocking), else emit a **null packet**.
  Result: exactly `R` bits/s.
- Count every emitted packet (content + null) as `n` — that drives `outSTC(n)`.
- **PCR ownership moves here.** The pacer sets PCR on the video PID at a fixed
  *output* cadence (≤ 40 ms ⇒ every `⌊40ms/Tpkt⌋` packets) with value
  `outSTC(n)`. It either overwrites a content PCR field or emits an
  adaptation-only PCR packet when one is due. The Phase-3 DTS-based PCR
  regeneration in the parser is **removed** (PTS/DTS in the PES stay preserved).
- `anchor` is chosen so PCR leads the first frame's DTS by the target decoder
  buffer delay (a few hundred ms).

### 3. Pacing (wall clock) for live output

- **Free-running (chosen for v1):** a timer releases one packet (or one 7-packet
  datagram) per slot off the local clock. Simple and exact-CBR; can drift versus
  the source encoder's clock over long runs.
- **Phase-locked (later):** drive the slot clock from the recovered source PCR so
  output STC tracks input STC with no long-term drift. Pairs with RTP output.
- **File output:** no wall-clock pacing needed — just write the CBR-structured
  bytes (PCR still slot-based).

### Composition

```
muxer (lead-window interleave, no PCR) -> content packets -> pacer (CBR slots,
   null padding, PCR on output clock) -> sink (file / UDP)
```

### Constraints & validation

- `R` must exceed the peak content rate over any PCR window, or the pacer
  underflows and frames arrive late — hence the encoder cap + margin.
- Validate with TSDuck: `tsp -P analyze` (TS bitrate == target), `tsp -P
  pcrverify` (PCR jitter), and `tsp -P tr101290` (DVB buffer/PCR compliance).

## Phase 5 — phase-lock the pacer to the source (detailed design)

Phase-lock = slave our output clock to the **source** stream's clock (recovered
from its PCR with a phase-locked loop) instead of the local machine crystal, so
the output advances at exactly the source's rate. (In broadcast this role is
called "genlock"; we lock to the source stream rather than a house reference and
don't align frame phase, so "phase-lock" describes the mechanism more precisely.)

### The problem with the free-running pacer

The Phase-4 pacer paces on the local wall clock. The source encoder uses its own
27 MHz crystal; the two drift by a few ppm (seconds/hour). We preserve source
PTS/DTS but pace + PCR on the local clock, so they diverge:

- **local faster:** output PCR outruns the frames' DTS → decoder buffer drains →
  underflow/freeze (and our content buffers empty → harmless extra nulls);
- **local slower:** content arrives faster than we emit → our FIFO/pacer buffers
  fill → eventually **content is dropped**.

Phase-lock keeps "one output second" equal to "one source second", bounding
buffers forever.

### Source clock recovery

The processor already reads the input PCR (`updatePCR`). For phase-lock it feeds
`(sourcePCR, localArrivalTime)` samples to a shared **sourceClock**, a 2nd-order
phase-locked loop (broadcast-grade jitter rejection, smooth convergence):

- **NCO (estimate):** the loop holds an estimated source STC plus a frequency
  (rate of source ticks per local second, ≈ 27e6 ± ppm). Between PCRs,
  `Now(localTime) = stcEst + freq*(localTime - tEst)`.
- **Phase detector:** on each input PCR, `error = sourcePCR - Now(arrivalTime)`.
- **Loop filter (PI):** `freq += Ki*error`, and the estimate is nudged
  `stcEst += Kp*error` (proportional) — a standard 2nd-order loop. Pick the loop
  bandwidth for a slow time constant (≈ seconds) with damping ζ ≈ 0.707; derive
  `Kp, Ki` from bandwidth and the PCR sample interval.
- Handle the 33-bit×300 PCR wrap and PCR discontinuities (see below).

`sourceClock` exposes `Now(localTime) -> source STC` and the locked `freq`; it
reports `locked` once the phase error stays within a small band for a while.

### Pacer change

The pacer's leaky bucket switches its time base from local elapsed to source
elapsed:

```
target_packets = (sourceClock.Now() - anchorSTC) / tppTicks
```

(`tppTicks = 188*8*27e6 / R`.) The wake timer stays local (~2 ms) for
granularity, but the *target* is source-driven, so the long-term output rate is
exactly `R` bits per **source** second. PCR stays `anchorPCR + (n-anchorN)*
tppTicks`; because `n` now advances on the source clock, PCR tracks the source
and no longer drifts from PTS/DTS. The fixed offset between sourceClock and PCR
(set at anchor) is the pipeline latency, and phase-lock maintains it.

### Activation

A `-phase-lock` flag (default on) controls it, independent of `-stream-bitrate`.
When enabled and the input has a usable PCR, the pacer is source-paced; if the
flag is off, or there's no input PCR, it free-runs (Phase-4 behavior). Keeping it
a separate knob makes locked-vs-free-running easy to A/B test.

### Edge cases

- **Startup:** free-run until the PLL reports `locked` (phase error settled),
  then anchor and switch to source-paced.
- **PCR discontinuity** (`discontinuity_indicator` or a large jump in the input):
  re-anchor the clock model and set `discontinuity_indicator` on the next output
  PCR packet.
- **No usable input PCR:** fall back to the Phase-4 free-running pacer.
- **Jitter:** the estimator/PLL time constant filters input PCR arrival jitter so
  output PCR stays smooth (well under the `pcrverify` 1 ms threshold).

### Validation

Long run (minutes): output PCR rate vs input rate stays locked (no slope),
buffers stay bounded, `tsp -P pcrverify` clean, and `tsp -P tr101290`
PCR-accuracy / buffer checks pass.

## CLI

```
mpegts-xc -url udp://239.255.0.1:1234 [-packaging ts|rtp] -width 1280 -height 720 [-ecodec libx264] [-vb N] [-o out.ts]
```

`-packaging` selects raw MPEGTS-over-UDP (`ts`) vs RTP-wrapped (`rtp`, header
stripped via `broadcastproto/transport`). The default `auto` derives it from the
url scheme — `rtp://` => RTP, `udp://` => raw TS — matching the broadcastproto
idiom; an explicit `ts`/`rtp` overrides the scheme (useful when an RTP feed is
addressed as `udp://`). There is no `InputPackaging` parameter in this repo.

## Phase 0 findings (verified)

- **avpipe input must use a non-network pseudo-URL.** avpipe's C opener
  dispatches by URL scheme: a `udp://` URL routes to `udp_in_opener`
  (`avpipe.c:310`) which binds the socket itself and never calls our Go
  `InputOpener` (first run failed `EAV_OPEN_INPUT` / bind errno 48 on the port
  our Go transport already held). A plain name (`mpegts-xc-video.ts`) makes
  `is_custom_input()` true and routes through our custom AVIO reader. Verified:
  avpipe then decodes → scales → re-encodes with `xcErr=<nil>`.
- **`UseCustomLiveReader` is not the fix and is not needed.** In `Xc()` it only
  sets `use_preprocessed_input=1` (it does not install our reader); in `XcInit`
  it installs broadcastproto's `NewAutoInputOpener`, which opens *its own* UDP
  socket — not what we want. The pseudo-URL already yields `is_custom_input()=1`.
  Trade-off: a non-network URL makes avpipe treat the input as a file
  (`is_live_source()=0`), which is fine because we re-stamp PTS/DTS from the
  source anyway.
- **Data flow is Go-reader-only.** The Go UDP/classify loop feeds
  video-substream TS packets (video PID + PAT/PMT) into a buffered channel; the
  avpipe custom AVIO reader pulls directly from that channel (no C reader, no
  io.Pipe). Closing the channel signals EOF.
- **avpipe now emits continuous MPEGTS** via a new `"mpegts"` output format
  (libavpipe). Because avpipe never `avio_open`s the encoder's main output and a
  plain muxer (unlike segment/dash/hls) opens no child files via `io_open`, the
  `"mpegts"` path explicitly calls `io_open` for the main `pb` before
  `write_header` (avpipe_xc.c). The CLI writes one `.ts` file per run.
- **PTS/DTS parity confirmed**: avpipe preserves source video PTS exactly and
  frame count matches (the `"mpegts"` path skips the fmp4-segment PTS rebasing),
  so the splice can reuse avpipe's PTS/DTS directly — no re-stamping needed.
  Caveat: avpipe's libx264 defaulted to Constrained Baseline (no B-frames), so
  output DTS==PTS while the source had B-frame DTS reordering; harmless for
  timing fidelity. Set encoder profile + bframes only if exact B-frame structure
  must be reproduced.
