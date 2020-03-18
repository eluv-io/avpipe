module github.com/qluvio/avpipe

require (
	github.com/grafov/m3u8 v0.11.1
	github.com/qluvio/content-fabric v0.0.0-20200317211333-a1edc2ec7cc7
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.4.0
)

replace (
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	github.com/qluvio/avpipe => ./
	github.com/zencoder/go-dash/v3 => github.com/jslching/go-dash/v3 v3.0.1-0.20191008005824-55e0140f9cf0
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)

go 1.13
