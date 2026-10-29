// Package clienttest starts an in-process gRPC server and dials it through the production client stack (deadline,
// circuit breaker, retries), for testing the adapters in internal/client.
package clienttest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/metrics"
)

// Conn serves register's services in memory and returns a client connection to them. reg may be nil (no breaker).
func Conn(t *testing.T, register func(*grpc.Server), name string, reg *breaker.Registry) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("stub", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpcx.Dial(grpcx.ClientConfig{Target: "passthrough:///stub", Name: name, Timeout: time.Second, Breakers: reg, Retries: 1},
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}
