module github.com/eluv-io/avpipe

go 1.26

require (
	github.com/Comcast/gots v0.0.0-20200213175321-9799558ed3e2
	github.com/Comcast/gots/v2 v2.2.1
	github.com/Eyevinn/mp4ff v0.51.0
	github.com/eluv-io/common-go v1.1.21
	github.com/eluv-io/errors-go v1.0.5
	github.com/eluv-io/log-go v1.0.10-0.20260314163338-6554b440b488
	github.com/grafov/m3u8 v0.11.1
	github.com/modern-go/gls v0.0.0-20250215024828-78308f6bb19d
	github.com/spf13/cobra v1.8.1
	github.com/stretchr/testify v1.11.1
	go.uber.org/atomic v1.11.0
	go.uber.org/goleak v1.3.0
	golang.org/x/net v0.21.0
	golang.org/x/sys v0.32.0
)

require (
	github.com/PaesslerAG/gval v1.1.2 // indirect
	github.com/PaesslerAG/jsonpath v0.1.1 // indirect
	github.com/beevik/etree v1.1.0 // indirect
	github.com/benburkert/openpgp v0.0.0-20160410205803-c2471f86866c // indirect
	github.com/datarhei/gosrt v0.9.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/eluv-io/apexlog-go v1.9.1-elv4 // indirect
	github.com/eluv-io/stack v1.8.2 // indirect
	github.com/eluv-io/utc-go v1.0.1 // indirect
	github.com/fxamacker/cbor/v2 v2.8.0 // indirect
	github.com/gammazero/deque v0.1.0 // indirect
	github.com/ghodss/yaml v1.0.0 // indirect
	github.com/gin-gonic/gin v1.7.7 // indirect
	github.com/go-playground/locales v0.13.0 // indirect
	github.com/go-playground/universal-translator v0.17.0 // indirect
	github.com/go-playground/validator/v10 v10.4.1 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.9 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/leodido/go-urn v1.2.0 // indirect
	github.com/mitchellh/mapstructure v1.4.3 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mr-tron/base58 v1.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	github.com/satori/go.uuid v1.2.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/ugorji/go/codec v1.1.7 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/exp v0.0.0-20231006140011-7918f672742d // indirect
	golang.org/x/text v0.24.0 // indirect
	google.golang.org/protobuf v1.31.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/fxamacker/cbor/v2 => github.com/eluv-io/cbor/v2 v2.8.1-0.20250506081522-e7b11bfa1dad
	github.com/modern-go/gls => github.com/eluv-io/gls v1.0.0-elv1
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.7.1-0.20170814170113-3101606756c5
	gopkg.in/urfave/cli.v1 => github.com/urfave/cli v1.22.0
)
