# Live Development Reference

## Live vertical crop data

`elvxc transcode` can read vertical crop data incrementally instead of loading a complete data file. Best option is a named FIFO (unix-domain sockets and UDP sockets are not accepted).

```bash
mkfifo /tmp/vertical-crop.fifo

elvxc transcode \
  ... \
  --vertical 1 \
  --vertical-data /tmp/vertical-crop.fifo \
  --threads 1
```

The stream format and behavior are:

- One 4-byte little-endian `uint32` record per decoded video frame.
- Each value is the crop window centre as a fraction of the scaled frame width, with denominator `VERTICAL_DATA_SCALE` (10000): `0` = left edge, `5000` = centre, `10000` = right edge.
- Reads block until a complete record is available.
- EOF after at least one complete record holds the last value for all remaining frames. This is logged once as a warning; for a live stream it means the crop stays frozen until the recording ends.
- EOF before the first complete record, a record cut short, or any other read error fails the transcode with `EAV_READ_INPUT`. Frames already decoded are still encoded at the last crop position and the output is finalised, so the file is complete but the job reports the failure. Before the first value there is nothing to hold, so the transcode fails before encoding those frames.
- The last few frames (the decoder's reorder delay) are flushed after the input ends and still consume one record each, so the end of a transcode waits for the producer.
- Cancelling a transcode closes the reader to unblock a pending read. That works for pipes, sockets and FIFOs on Linux; on macOS a FIFO read is not interrupted by `Close` and returns only when the producer writes or closes.
- `elvxc` streams only when `--vertical-data` is not a regular file. A regular file is loaded whole (the `VerticalData` buffer), validated before the job starts, and can be shared by several `--threads`.
- Streaming vertical data requires exactly one `elvxc` transcoding thread.

To verify the streaming path end to end:

```bash
# Library: streamed crop data - through a pipe and through a real named FIFO fed
# at live rate - must produce output byte-identical to the in-memory VerticalData
# buffer path, at the 9:16 size; also covers EOF hold, an empty source, a
# mid-stream read error, a malformed buffer, and cancelling a transcode blocked
# on a reader that never produces.
go test . -run 'TestVerticalStream|TestVerticalData'

# elvxc: the file-vs-stream decision and the --threads gate, including through
# the real command's flags. No media needed.
go test ./elvxc/... -run 'TestVerticalDataSource|TestTranscodeRefuses|TestCopyPackaging'

# Filter level, independent of any codec: the crop follows the data frame by
# frame on a synthesized gradient, from the buffer and from a streaming reader
# including the hold after EOF (libavpipe/test/test_vertical_crop.c).
make -C libavpipe test
```

