module github.com/iris-connect/eps

go 1.16

require (
	github.com/golang/protobuf v1.5.2
	github.com/kiprotect/go-helpers v0.0.0-20210719141457-5b87e3cc7847
	github.com/protocolbuffers/protobuf v3.15.8+incompatible // indirect
	github.com/prometheus/client_golang v1.11.0
	github.com/sirupsen/logrus v1.8.1
	github.com/urfave/cli v1.22.5
	google.golang.org/grpc v1.37.0
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.1.0 // indirect
	google.golang.org/protobuf v1.26.0
)

// for local testing against a modified go-helpers library
// replace github.com/kiprotect/go-helpers => ../../../geordi/kiprotect/go-helpers
