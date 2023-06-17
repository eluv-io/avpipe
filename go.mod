module github.com/eluv-io/avpipe

require (
	github.com/Comcast/gots v0.0.0-20200213175321-9799558ed3e2
	github.com/abema/go-mp4 v0.10.1
	github.com/eluv-io/errors-go v1.0.0
	github.com/eluv-io/log-go v1.0.1
	github.com/grafov/m3u8 v0.11.1
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.7.0
)

replace (
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)

go 1.13
