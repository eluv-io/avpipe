
# Media Augmentation Data Stream

Draft protocol specification, version 1.

This protocol carries metadata associated with one live media program. A stream
is a sequence of protocol data units (PDUs), each containing **type, timecode,
length, and value (TTLV)**. The design favors fixed fields and straightforward
parsing over saving bytes.

## Stream format

PDUs are concatenated without separators or padding. All integers are unsigned
and use network byte order (big-endian). A byte is eight bits.

Frame, program, and change PDUs have a 14-byte header followed by their value:

| Byte offset | Size in bytes | Field |
| --- | --- | --- |
| 0 | 1 | Type |
| 1 | 1 | Timecode kind |
| 2 | 8 | Timecode ticks |
| 10 | 4 | Length |
| 14 | Length | Value |

Frame range PDUs (`Type = 0x04`) insert a two-byte frame count after the
timecode and before Length, giving a 16-byte header:

| Byte offset | Size in bytes | Field |
| --- | --- | --- |
| 0 | 1 | Type |
| 1 | 1 | Timecode kind |
| 2 | 8 | Timecode ticks |
| 10 | 2 | Frame count |
| 12 | 4 | Length |
| 16 | Length | Value |

The timecode consists of its kind and ticks fields and always occupies nine
bytes, including when no timestamp is present. Length counts only the value
bytes; the total PDU size is `16 + Length` bytes for frame range PDUs and
`14 + Length` bytes for all other types. Frame count is part of the header and
is not included in Length.

There is no stream header, checksum, compression, or alignment requirement. The
containing transport or application identifies this protocol and its version,
associates it with a media program, and provides reliable, ordered delivery.
Transport packet and read boundaries have no significance: a read may contain
part of a PDU or multiple PDUs.

## Type

| Code | Name | Meaning | Required timecode kind |
| --- | --- | --- | --- |
| `0x01` | Frame data | Metadata for the frame at the given presentation time. | `ts_pts` or `uw_pts` |
| `0x02` | Program data | Metadata for the whole associated program. | `none` |
| `0x03` | Change data | Metadata taking effect at the given presentation time and holding until the next change. | `ts_pts` or `uw_pts` |
| `0x04` | Frame range data | Metadata for a specified number of consecutive frames, starting at the given presentation time. | `ts_pts` or `uw_pts` |

Type `0x00` is invalid. All other codes are reserved for future types. Receivers
skip unknown nonzero types using Length. Reserved types use the 14-byte header;
adding any further header layout requires a new protocol version.

Frame data applies only to its identified frame. Change data applies from its
identified frame, inclusive, until the next change-data PDU's identified frame,
exclusive, or until the program ends. Each change value replaces the previous
change value in full; changes are not implicit patches. Before the first change,
no change data is defined. Frame, program, and frame range data do not reset
change data.

### Frame range data

Frame range data has the same meaning as frame data, except that the same value
applies to Frame count consecutive frames in presentation order. The count
includes the frame identified by the timecode. It is a two-byte unsigned
big-endian integer from 1 through 65,535; zero is invalid. A count of 1 is
equivalent to frame data for the identified frame.

The value stops applying after the counted frames, or when the program ends,
whichever comes first. Count frames on the associated media timeline, including
frames a receiver drops during playback. Frame count is not a duration in ticks
and does not require a fixed frame rate. The application supplies the media
context needed to identify the frames.

Ranges are independent and do not cancel earlier ranges. Overlapping ranges
and separate frame PDUs are delivered in stream order; their combination or
replacement rules belong to the application's value format.

Send change data in effective presentation order. If multiple changes identify
the same frame, the last one in stream order takes precedence. Multiple frame
or program PDUs are delivered in stream order; their combination or replacement
rules belong to the application's value format.

## Timecode

| Code | Name | Meaning of the eight-byte ticks field |
| --- | --- | --- |
| `0x00` | `none` | No timestamp. Ticks must be zero. |
| `0x01` | `ts_pts` | MPEG transport-stream presentation timestamp, in 90,000 ticks per second, modulo 2^33. |
| `0x02` | `uw_pts` | Unwrapped presentation timestamp, in 90,000 ticks per second, without the 33-bit wrap. |

All other timecode kinds are reserved. For known PDU types, an unknown kind or
a kind inconsistent with the type is an error.

A `ts_pts` value must be less than 2^33; its upper 31 bits must be zero. MPEG-TS
PTS requires 33 bits, so a four-byte field would be insufficient. This protocol
stores the numeric timestamp directly, without MPEG-TS marker bits or PES
timestamp packing.

For `uw_pts`, the sender adds a multiple of 2^33 to the corresponding `ts_pts`
value to preserve a continuous timeline across wraps. The application establishes
the wrap count and timeline origin shared by the metadata sender, receiver, and
media. The unsigned 64-bit ticks field must not wrap.

Timecodes refer to presentation time, not decode time or wall-clock time.

Must use one timestamp kind consistently within a data stream.  Mixing kinds will result
in errors.

A stream represents one continuous program timeline. A program switch, timestamp
reset, or discontinuity starts a new stream association and clears held change
data. The application signals that boundary outside this protocol.

## Length and value

Length is a four-byte unsigned integer in the range 0 through 4,294,967,295.
Zero-length values are valid and have no special protocol meaning, including no
implicit deletion or reset.

Value is an opaque sequence of bytes. Its encoding and schema are agreed by the
application outside this protocol. A type describes the metadata's scope, not
its encoding. There is no implicit text encoding, terminator, or nested TTLV
structure.

Receivers may enforce a documented maximum value length smaller than the wire
format permits. Check Length against that limit before allocating memory, and
use arithmetic wide enough to represent `header size + Length` without overflow.

## Specific data types

This section describes only the custom Value encoding for each application-specific
data type. Framing is defined by the general protocol above.

### Vertical crop

Value is exactly four bytes containing one unsigned integer in big-endian byte
order. The integer's units represent an x-coordinate times 10,000 (eg. 5,000 represents 'middle' 0.5).

For example, value `5000` is encoded as:

```text
00 00 13 88
```

## Parsing and errors

1. Read exactly 10 bytes for Type and timecode, retaining partial reads until
   complete. Type `0x00` is an error.
2. If Type is `0x04`, read the two-byte Frame count and validate that it is
   nonzero. For all other types, there is no Frame count field.
3. Read the four-byte Length and check the receiver's size limit. For a known
   type, validate the timecode fields and their permitted combination.
4. Read exactly Length value bytes. Deliver a known PDU only after all its bytes
   arrive. For an unknown nonzero type, discard exactly Length bytes instead.
5. Continue with the next PDU.

End of stream between PDUs is valid. End of stream inside a header or value is
a truncated-stream error. An invalid field or a length exceeding the receiver's
limit is a stream error. Do not scan for a possible next header: the format has no
resynchronization marker. Timecode validation is skipped for unknown PDU types,
but length limits and truncation checks still apply.

The sender is responsible for delivering metadata in time for its intended use.
Buffering and handling of late metadata are application policies.

## Examples

Program data with no timestamp and the three illustrative value bytes `abc`:

```text
Type  Kind  Ticks                    Length       Value
02    00    00 00 00 00 00 00 00 00  00 00 00 03  61 62 63
```

Frame data at PTS 90,000 (one second after the timestamp origin), with two
illustrative value bytes `BE EF`:

```text
Type  Kind  Ticks                    Length       Value
01    01    00 00 00 00 00 01 5F 90  00 00 00 02  BE EF
```

Frame range data starting at PTS 90,000 and applying to 30 consecutive frames,
with two illustrative value bytes `BE EF` (18 bytes total):

```text
Type  Kind  Ticks                    Frame count  Length       Value
04    01    00 00 00 00 00 01 5F 90  00 1E        00 00 00 02  BE EF
```
