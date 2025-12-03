module github.com/eluv-io/avpipe

go 1.23.0

require (
	github.com/Comcast/gots v0.0.0-20200213175321-9799558ed3e2
	github.com/Comcast/gots/v2 v2.2.1
	github.com/Eyevinn/mp4ff v0.49.0
	github.com/eluv-io/common-go v1.1.12-0.20251203000544-e8939052d1e7
	github.com/eluv-io/errors-go v1.0.4
	github.com/eluv-io/log-go v1.0.8
	github.com/grafov/m3u8 v0.11.1
	github.com/modern-go/gls v0.0.0-20250215024828-78308f6bb19d
	github.com/spf13/cobra v1.8.1
	github.com/stretchr/testify v1.11.1
	go.uber.org/atomic v1.11.0
)

require (
	github.com/benburkert/openpgp v0.0.0-20160410205803-c2471f86866c // indirect
	github.com/datarhei/gosrt v0.9.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/eluv-io/apexlog-go v1.9.1-elv4 // indirect
	github.com/eluv-io/stack v1.8.2 // indirect
	github.com/eluv-io/utc-go v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/exp v0.0.0-20231006140011-7918f672742d // indirect
	golang.org/x/sys v0.32.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/modern-go/gls => github.com/eluv-io/gls v1.0.0-elv1
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)
