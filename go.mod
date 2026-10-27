module meshcraft

go 1.24.12

require meshcraft/wire v0.0.0

replace meshcraft/wire => ./wire

toolchain go1.24.13

require (
	github.com/coder/websocket v1.8.15
	github.com/go-chi/chi/v5 v5.2.1
	github.com/gosnmp/gosnmp v1.44.0
	github.com/hashicorp/yamux v0.1.2
	github.com/jackc/pgx/v5 v5.7.5
	github.com/openconfig/gnmi v0.14.1
	github.com/openconfig/gnmic/pkg/api v0.1.11
	golang.org/x/crypto v0.46.0
	golang.org/x/net v0.48.0
	google.golang.org/grpc v1.78.0
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/AlekSi/pointer v1.2.0 // indirect
	github.com/bufbuild/protocompile v0.14.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jhump/protoreflect v1.17.0 // indirect
	github.com/openconfig/grpctunnel v0.1.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251029180050-ab9386a59fda // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
