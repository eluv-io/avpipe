


# How To

## Test - capture MPEG-TS and create mezzanine test files

Terminal A

``` bash
cd ./live
go test --run TestUdpToMp4V2

```

Terminal B

``` bash
.../udpreplay --use-timestamps --host 127.0.0.1 --port 21001 test-file.cap

```


## Appendix

### Install `udpreplay`

Mac prerequisites

  - brew install boost autoconf automake libtool

``` bash
  git clone git@github.com:elv-serban/udpreplay.git
  ./bootsrap.sh
  ./configure
  make
```

### Encoding parameters - FS-1 MPGEG-TS UDP cap files

``` golang

	audioParamsTs := &avpipe.TxParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		AudioBitrate:    384000, // FS1-19-10-14.ts audio bitrate
		SegDurationTs:   -1,
		SegDuration:     "30.03", // seconds
		Dcodec:          "ac3",
		Ecodec:          "aac", // "aac"
		TxType:          avpipe.TxAudio,
		SampleRate:      48000,
		AudioIndex:      -0,
	}

	videoParamsTs := &avpipe.TxParams{
		Format:          "fmp4-segment",
		Seekable:        false,
		DurationTs:      -1,
		StartSegmentStr: "1",
		VideoBitrate:    20000000, // fox stream bitrate
		SegDurationTs:   -1,
		ForceKeyInt:     120,
		SegDuration:     "30.03",   // seconds
		Ecodec:          "libx264", // libx264 software / h264_videotoolbox mac hardware
		EncHeight:       720,
		EncWidth:        1280,
		TxType:          avpipe.TxVideo,
	}
```
