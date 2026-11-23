
# Media Augmentation Data Stream

Draft protocol specification - v1

This protocol carries data/metadata associated with one live media program. A stream
is a sequence of protocol data units (PDUs), each containing **type, time spec,
time, length, and payload**.

## Stream format

PDUs are concatenated without separators or padding. All integers are unsigned
and use network byte order (big-endian). A byte is eight bits.

Every PDU has a 14-byte header followed by its payload:

| Byte offset | Size in bytes | Field |
| --- | --- | --- |
| 0 | 1 | Type |
| 1 | 1 | Time Spec |
| 2 | 8 | Time |
| 10 | 4 | Length |
| 14 | Length | Payload |

Every PDU carries a Time Spec and a Time. Length counts only the payload bytes; the total PDU size is `14 + Length` bytes.

## Type

Type gives the scope of the metadata: which frames it applies to.

| Code | Name | Meaning |
| --- | --- | --- |
| `0x01` | Program data | Metadata for the whole associated program. |
| `0x02` | Frame data | Metadata for the frame at the given time. |
| `0x03` | Change data | Metadata taking effect at the given time and holding until the next change. |

Program data applies to the whole program regardless of its Time. Its Time is
the stream time at which the producer sent it, normally the Time of the next
frame data PDU. Receivers may use it for logging and ordering, but it does not
limit the program data's scope.

Type `0x00` is invalid. All other codes are reserved for future types. Receivers
skip unknown nonzero types using Length. Reserved types use the 14-byte header;
adding any further header layout requires a new protocol version.

Frame data applies only to its identified frame. Change data applies from its
identified frame, inclusive, until the next change-data PDU's identified frame,
exclusive, or until the program ends. Each change payload replaces the previous
change payload in full; changes are not implicit patches. Before the first change,
no change data is defined. Frame and program data do not reset change data.

Send change data in effective presentation order. If multiple changes identify
the same frame, the last one in stream order takes precedence. Multiple frame
or program PDUs are delivered in stream order; their combination or replacement
rules belong to the application's payload format.

## Time Spec

Time Spec describes what the 8-byte Time field means.

| Code | Name | Meaning of Time |
| --- | --- | --- |
| `0x01` | `ts_pts` | MPEG transport-stream presentation timestamp, in 90,000 ticks per second, modulo 2^33. |
| `0x02` | `uw_pts` | Unwrapped presentation timestamp, in 90,000 ticks per second, without the 33-bit wrap. |
| `0x03` | `frame_offset` | Frame offset in nanoseconds on the producer's timeline. |

Time Spec `0x00` is invalid. All other Time Specs are reserved.

Every PDU in a stream uses the same Time Spec. Mixing Time Specs within one
stream is an error.

A stream represents one continuous program timeline. A program switch, timestamp
reset, or discontinuity starts a new stream association and clears held change
data. The application signals that boundary outside this protocol.

### PTS Time Specs

A `ts_pts` value must be less than 2^33; its upper 31 bits must be zero. MPEG-TS
PTS requires 33 bits, so a four-byte field would be insufficient. This protocol
stores the numeric timestamp directly, without MPEG-TS marker bits or PES
timestamp packing.

For `uw_pts`, the sender adds a multiple of 2^33 to the corresponding `ts_pts`
value to preserve a continuous timeline across wraps. The application establishes
the wrap count and timeline origin shared by the metadata sender, receiver, and
media. The unsigned 64-bit Time field must not wrap.

### Frame offset Time Spec (`frame_offset`)

Time is the frame's presentation time on the producer's own timeline, in
nanoseconds from the producer's origin. For frame number `n` at a frame rate of
`num/den` frames per second:

```text
time_ns = round(n * den * 1,000,000,000 / num)
```

Each frame's value is rounded on its own to the nearest nanosecond, so rounding
error does not accumulate. A receiver recovers the frame number exactly with
`n = round(time_ns * num / (den * 1,000,000,000))`. Receivers match frames by
that frame number, not by comparing nanosecond values. The unsigned 64-bit field
does not wrap for about 584 years.

The producer's origin and frame rate are carried in the TGI program data. The
producer's timeline is not the media's PTS timeline; the application establishes
the mapping between the two, for example with one known pair of frame offset and
PTS. The program data can carry such an anchor (see the protobuf schema).

At 59.94 fps (`60000/1001`), one frame lasts 16,683,333.33 ns. The TGI field
`frameHash.offset` is the same timeline in milliseconds, so
`time_ns = round(offset_ms * 1,000,000)` after rounding to the frame grid.

## Length and payload

Length is a four-byte unsigned integer in the range 0 through 4,294,967,295.
Zero-length payloads are valid and have no special protocol meaning, including no
implicit deletion or reset.

Payload is an opaque sequence of bytes. Its encoding and schema are agreed by
the application outside this protocol (for example [TGI metadata](#tgi-metadata)). A type
describes the metadata's scope, not its encoding. There is no implicit text
encoding, terminator, or nested PDU structure.

Receivers may enforce a documented maximum payload length smaller than the wire
format permits. Check Length against that limit before allocating memory, and
use arithmetic wide enough to represent `header size + Length` without overflow.

## Specific data types

This section describes the Payload encoding for each application-specific data
type. Framing is defined by the general protocol above.

### Vertical crop

Used with the PTS Time Specs. Payload is exactly four bytes containing one unsigned
integer in big-endian byte order. The integer's units represent an x-coordinate
times 10,000 (eg. 5,000 represents 'middle' 0.5).

For example, value `5000` is encoded as:

```text
00 00 13 88
```

### TGI metadata

TGI streams use Time Spec `frame_offset`. Payload is one Protobuf message (proto3 binary encoding)
from the schema in [mads_tgi.proto](mads_tgi.proto), package `mads.tgi.v1`. The
PDU type selects the message, so the payload carries no extra type tag:

| PDU type | Message | Content |
| --- | --- | --- |
| Program data | `Program` | Producer and schema version, processing and source formats, frame rate and origin of the time field, optional PTS anchor, placement names. |
| Frame data | `Frame` | TGI's per-frame JSON document, unchanged, plus the occlusion mask and a few typed copies of JSON fields. |
| Change data | none | Not used in schema version 1. Receivers skip it. |

The producer sends program data first, and again whenever a receiver may join
the stream (for example on reconnect). Receivers ignore frame data received
before the first program data.

The mask is sent as one or more rectangles cropped from the full processing-size
mask (for example 1280×720) and compressed with zlib. Pixels outside every
rectangle are 0, meaning "do not draw". Cropping to the placements reduces a
921,600-byte mask to a few kilobytes per frame (about 4 KB on average in the
sample clip). The schema file gives the exact
rules.

The payloads are ordinary Protobuf messages, so producers in Python, JavaScript or
Go use the classes generated from `mads_tgi.proto`. Only the 14-byte header
is written by hand.

## Parsing and errors

1. Read exactly 14 header bytes, retaining partial reads until complete.
   Type `0x00` is an error.
2. Check Length against the receiver's size limit. For a known type, validate
   Time Spec and Time, and check that Time Spec matches the rest of the stream.
3. Read exactly Length payload bytes. Deliver a known PDU only after all its bytes
   arrive. For an unknown nonzero type, discard exactly Length bytes instead.
4. Continue with the next PDU.

End of stream between PDUs is valid. End of stream inside a header or payload is
a truncated-stream error. An invalid field or a length exceeding the receiver's
limit is a stream error. Do not scan for a possible next header: the format has no
resynchronization marker. Time Spec and Time validation are skipped for unknown PDU
types, but length limits and truncation checks still apply.

A payload that fails to decode under the application's encoding (for example
invalid Protobuf in a TGI stream) is an error for that PDU only. The framing is still
intact, so the receiver may discard the PDU and continue.

The sender is responsible for delivering metadata in time for its intended use.
Buffering and handling of late metadata are application policies.

## Examples

Program data sent at PTS 90,000, with the three illustrative payload bytes `abc`:

```text
Type  Spec  Time                     Length       Payload
01    01    00 00 00 00 00 01 5F 90  00 00 00 03  61 62 63
```

Frame data at PTS 90,000 (one second after the timestamp origin), with two
illustrative payload bytes `BE EF`:

```text
Type  Spec  Time                     Length       Payload
02    01    00 00 00 00 00 01 5F 90  00 00 00 02  BE EF
```

TGI program data sent just before the frame below, so its Time is that frame's
Time. It is followed by an encoded `Program` message of 150 bytes (processing
and source formats, 59.94 time rate, origin, and eight placement names):

```text
Type  Spec  Time                     Length       Payload
01    03    00 00 00 37 E1 7F 8A 35  00 00 00 96  <Program, 150 bytes>
```

TGI frame data for the sample clip's `Metadata/723.json`. Its
`frameHash.offset` is 240006.43333 ms, which is producer frame 14,386, so Time
is 240,006,433,333 ns. The encoded `Frame` is 7,522 bytes: the 1,419-byte JSON
document plus two zlib-compressed mask regions, one per placement:

```text
Type  Spec  Time                     Length       Payload
02    03    00 00 00 37 E1 7F 8A 35  00 00 1D 62  <Frame, 7,522 bytes>
```
