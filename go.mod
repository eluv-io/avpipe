module github.com/qluvio/avpipe

require (
	github.com/grafov/m3u8 v0.11.1
	github.com/qluvio/content-fabric v0.0.0-20191008040520-343fa7c62539
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.4.0
)

replace (
	github.com/qluvio/avpipe => ./
	github.com/zencoder/go-dash/v3 => github.com/jslching/go-dash/v3 v3.0.1-0.20191008005824-55e0140f9cf0
)

go 1.13
