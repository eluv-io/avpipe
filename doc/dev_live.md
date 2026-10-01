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
- The existing decimal-fraction encoding is used: for example, `15`, `50`, and `85` represent positions near 0.15, 0.50, and 0.85 of the scaled width.
- Reads block until a complete record is available.
- EOF after at least one complete record reuses the last value for subsequent frames.
- Streaming vertical data requires exactly one `elvxc` transcoding thread.

