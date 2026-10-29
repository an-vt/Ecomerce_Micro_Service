package grpcx

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/metrics"
)

// NewServer builds a gRPC server with tracing and the standard interceptor chain, outermost first:
// recover → request id / principal → metrics → error mapping → logging.
func NewServer(service string, m *metrics.Metrics, log *slog.Logger, extra ...grpc.ServerOption) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(ServerInterceptors(service, m, log)...),
	}
	return grpc.NewServer(append(opts, extra...)...)
}

// ServerInterceptors returns the chain used by NewServer (exposed for tests).
func ServerInterceptors(service string, m *metrics.Metrics, log *slog.Logger) []grpc.UnaryServerInterceptor {
	chain := []grpc.UnaryServerInterceptor{recoverInterceptor(log), contextInterceptor()}
	if m != nil {
		chain = append(chain, metricsInterceptor(service, m))
	}
	return append(chain, errorInterceptor(), loggingInterceptor(log))
}

func shortMethod(full string) string { return path.Base(full) }

func recoverInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(ctx, "panic in gRPC handler", "method", info.FullMethod, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return h(ctx, req)
	}
}

func contextInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(incoming(ctx), req)
	}
}

func metricsInterceptor(service string, m *metrics.Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := h(ctx, req)
		m.GRPCServer.WithLabelValues(service, shortMethod(info.FullMethod), status.Code(err).String()).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// errorInterceptor converts domain errors to gRPC status errors (client-safe message, domain code in ErrorInfo).
func errorInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		resp, err := h(ctx, req)
		return resp, ToStatus(err)
	}
}

// loggingInterceptor sees the original error (before conversion): business rejections at debug/warn, internal
// errors at error level with the real cause.
func loggingInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := h(ctx, req)
		attrs := []any{"method", shortMethod(info.FullMethod), "duration_ms", time.Since(start).Milliseconds()}
		switch {
		case err == nil:
			log.DebugContext(ctx, "grpc call", attrs...)
		case errs.KindOf(err) == errs.KindInternal:
			log.ErrorContext(ctx, "grpc call failed", append(attrs, "error", err.Error())...)
		default:
			log.WarnContext(ctx, "grpc call rejected", append(attrs, "code", errs.CodeOf(err), "error", err.Error())...)
		}
		return resp, err
	}
}
