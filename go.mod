module github.com/eluv-io/avpipe

require (
	github.com/Comcast/gots v0.0.0-20200213175321-9799558ed3e2
	github.com/Comcast/gots/v2 v2.2.1
	github.com/Eyevinn/mp4ff v0.49.0
	github.com/eluv-io/errors-go v1.0.0
	github.com/eluv-io/log-go v1.0.1
	github.com/grafov/m3u8 v0.11.1
	github.com/modern-go/gls v0.0.0-20250215024828-78308f6bb19d
	github.com/spf13/cobra v0.0.5
	github.com/stretchr/testify v1.7.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/eluv-io/apexlog-go v1.9.1-elv3 // indirect
	github.com/eluv-io/stack v1.8.2 // indirect
	github.com/eluv-io/utc-go v1.0.0 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.3 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
)

replace (
	github.com/modern-go/gls => github.com/eluv-io/gls v1.0.0-elv1
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)

go 1.22
