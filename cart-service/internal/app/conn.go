package app

import (
	"google.golang.org/grpc"

	"github.com/an-vt/ecom/platform-go/grpcx"
)

type grpcConn = grpc.ClientConn

func dialConn(cfg grpcx.ClientConfig) (*grpcConn, error) { return grpcx.Dial(cfg) }
