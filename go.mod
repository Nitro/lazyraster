module github.com/nitro/lazyraster/v2

go 1.23.6

require (
	github.com/Nitro/urlsign v0.0.0-20181015102600-5c9420004fa4
	github.com/aws/aws-sdk-go v1.55.7
	github.com/go-chi/chi/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/nitro/lazypdf/v2 v2.0.0-20250205120532-48aae54bfd96
	github.com/rs/zerolog v1.33.0
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/smartystreets/goconvey v1.6.4 // indirect
	github.com/stretchr/testify v1.10.0
	github.com/tinylib/msgp v1.2.5 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/time v0.10.0 // indirect
	gopkg.in/DataDog/dd-trace-go.v1 v1.71.1
)

require (
	github.com/DataDog/appsec-internal-go v1.10.0 // indirect
	github.com/DataDog/datadog-agent/pkg/obfuscate v0.62.1 // indirect
	github.com/DataDog/datadog-agent/pkg/remoteconfig/state v0.62.1 // indirect
	github.com/DataDog/datadog-go/v5 v5.6.0 // indirect
	github.com/DataDog/go-libddwaf/v3 v3.5.1 // indirect
	github.com/DataDog/go-sqllexer v0.0.20 // indirect
	github.com/DataDog/go-tuf v1.1.0-0.5.2 // indirect
	github.com/DataDog/gostackparse v0.7.0 // indirect
	github.com/DataDog/sketches-go v1.4.6 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/eapache/queue/v2 v2.0.0-20230407133247-75960ed334e4 // indirect
	github.com/ebitengine/purego v0.8.2 // indirect
	github.com/google/pprof v0.0.0-20250202011525-fc3143867406 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.1.9 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.2 // indirect
	github.com/hashicorp/go-sockaddr v1.0.7 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/mapstructure v1.5.1-0.20231216201459-8508981c8b6c // indirect
	github.com/outcaste-io/ristretto v0.2.3 // indirect
	github.com/philhofer/fwd v1.1.3-0.20240916144458-20a13a1f6b7c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/richardartoul/molecule v1.0.1-0.20240531184615-7ca0df43c0b3 // indirect
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.9.0 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/mod v0.23.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	google.golang.org/grpc v1.70.0 // indirect
	google.golang.org/protobuf v1.36.4 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Version v1.71.1 is breaking the build.
replace gopkg.in/DataDog/dd-trace-go.v1 v1.71.1 => gopkg.in/DataDog/dd-trace-go.v1 v1.67.1

// lazypdf version needs to be pinned as well because of the dd-trace-go version pinning which prevents the lazypdf version from being
// updated.
replace github.com/nitro/lazypdf/v2 v2.0.0-20250205120532-48aae54bfd96 => github.com/nitro/lazypdf/v2 v2.0.0-20250220163903-ee81e115f55e
