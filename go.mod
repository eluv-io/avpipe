module github.com/qluvio/avpipe

require (
	github.com/Comcast/gots v0.0.0-20200213175321-9799558ed3e2
	github.com/grafov/m3u8 v0.11.1
	github.com/qluvio/content-fabric v0.0.0-20210903031516-e8e99f9bbac1
	github.com/qluvio/legacy_imf_dash_extract v0.0.0-20210828002916-75c85e7fe519
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.7.0
)

replace (
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	github.com/qluvio/avpipe => ./
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)

go 1.13
