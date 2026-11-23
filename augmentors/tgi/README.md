# SmartFeed Augmentor — integration notes

A C library that draws adverts onto video frames. The host decodes, pairs each frame with its
metadata, and calls one function per frame.

## What you get

| | |
|---|---|
| `smartfeed_augmentor.h` | the whole API. C, no C++, no CUDA and no libav types |
| `libsfa_node.so` | the implementation |

The library exports five symbols and nothing else. It does not use or link to FFmpeg.

## Requirements

- **Linux, x86-64.**
- **An NVIDIA GPU.** The build you receive names the compute capabilities it was compiled for.
  L4 is 8.9; the artefact also carries PTX, so a newer card works by JIT at first use.
- **CUDA** is statically linked and it carries its own runtime, needing only the driver

## Calling it

```c
sfa_node* node;
if (sfa_init(&params, &node) != SFA_OK) { /* sfa_last_error is not useful yet: node is NULL */ }

for (;;)
{
    switch (sfa_augment(node, &frame, &metadata))
    {
    case SFA_OK:            /* the frame now carries an advert - encode it */        break;
    case SFA_UNMODIFIED:    /* nothing was drawn; the frame is byte for byte as it
                               arrived - encode it unchanged */                      break;
    case SFA_FRAME_DAMAGED: /* discard this frame and log; see below */              break;
    default:                /* our fault or a bad argument; frame is untouched */    break;
    }
}

sfa_destroy(node);
```

`sfa_init` allocates device memory, uploads the advert images and starts the renderer. It is far
more expensive than `sfa_augment`, deliberately, so that the first frame costs what every other
frame costs.

## The contract

**In place.** `sfa_augment` rewrites the frame you hand it, where an advert is drawn and nowhere
else. It never allocates a frame for you and never copies one you did not ask it to.

**One thread, in order.** A node must not be used from two threads at once. Frames must also
arrive in presentation order: a node remembers the previous frame per placement, for fade-in and
for deferring an advert change while a placement is continuously on camera. Handing frames to a
pool that serialises access but not ordering will produce wrong adverts, not a crash.

**Lifetime.** The frame, the metadata and the mask must be valid for the duration of the call
only. Advert images are copied during `sfa_init` and need not outlive it.

**`SFA_FRAME_DAMAGED`.** The renderer writes the frame plane by plane. If the device fails part
way through, some planes carry the advert and the rest do not. The frame is then neither yours
nor ours: discard it. Every other result leaves the frame untouched and usable. In practice this
means the GPU has gone, and the frames after it will fail too.

**Several outputs** means several nodes, each with its own configuration. They share nothing.

## What we need from you, per frame

**The metadata document**, as UTF-8 JSON. It carries its own coordinate space — `processingWidth`
and `processingHeight`, which the placement corners are expressed in — and a `placeholders`
object naming each placement and its four corners. `timecode` is read when present and is not
required.

**The mask**, 8 bits per pixel, the size of that same coordinate space. **White draws and black
does not**: a player standing in front of a board is black. A frame passed with no mask is
returned unaugmented.

**Frames as I420** (YUV 4:2:0 planar), in host memory. 

## Checking a build

`sfa_augment_one_frame` ships alongside. It makes up a frame, a mask, a document and an advert,
augments once, and prints how many samples changed. It exits 0 only if the frame came back
augmented, so it is a usable smoke test for a new machine or a new driver:

```
$ ./sfa_augment_one_frame
ABI version 1
SFA_OK: 86400 of 921600 luma samples changed
```

86400 is exactly the area of the placement in that document, which is the useful part: it says
the advert landed where the corners asked rather than merely that something was drawn.

## Knowing what it did

`sfa_get_stats()` fills a struct with what the node has done since `sfa_init`.

```c
sfa_stats stats = { 0 };
if (sfa_get_stats(node, &stats) == SFA_OK)
    log("augmented %llu of %llu", stats.frames_augmented, stats.frames_seen);
```

`augmented_placements` counts placements, not frames: a frame carrying three adverts adds
three, so divided by `frames_augmented` it is the average number of adverts on a frame.

The rest say why nothing was drawn, which matters because "no advert appeared" has several
causes that look identical from outside:

- **`skipped_no_creative`** — the metadata named a placement and no advert image was supplied
  for it. On a new integration this is the first field to read: it is what a mismatch between
  your creative names and the producer's placement names looks like, and nothing else reports
  it.
- `skipped_no_mask` — a placement arrived without a mask, so it was not drawn.
- `not_live` — the producer marked the frame not live.
- `frames_fully_occluded` — drawn, then entirely masked away.

`augment_time_us_total` and `augment_time_us_max` cover the frames that were drawn on. The
total over `frames_augmented` is the mean; the maximum is the one that says whether a frame was
ever late.

The library writes one summary line through `log_cb` when the node is destroyed. It does not log
statistics on any other schedule: when to record them is yours to decide, not ours.

## Versions

The header defines `SFA_ABI_VERSION`; the library reports `sfa_abi_version()`. Check them against
each other before `sfa_init`:

```c
if (sfa_abi_version() != SFA_ABI_VERSION)
    fatal("built against %u, linked %u", SFA_ABI_VERSION, sfa_abi_version());
```

Treat a difference as fatal. The structs are passed by value, so a library from another version
reads them at the wrong offsets rather than refusing them. The number changes whenever a struct or a status in the
header changes, so always take the header and the `.so` from the same drop.

`sfa_build_info()` answers the other question — which build this is — for a log line at startup
and for anything you send us about a problem:

```
SmartFeed Augmentor Node ABI 1 (v0.1.1-79-g141cfcf6d7, CUDA 12.0.140,
sm 75,80,86,89,89-virtual, cudart static)
```

It carries the commit, the toolkit, the GPU architectures the artefact contains and how the CUDA
runtime is linked. It is never not a compatibility check.

## Known limits, deliberate for a first version

- **One output per node.**
- **Still images only.** Animated adverts need a decoder we do not want inside this library.
- **Host memory.** Frames are passed in and out of host memory; each one is uploaded to the GPU
  and downloaded again.
- **BT.709** colour, not configurable.
