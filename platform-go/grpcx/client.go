package grpcx

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/retry"
)

// ClientConfig configures a client connection to another service.
type ClientConfig struct {
	Target   string // host:port
	Name     string // label for breaker and metrics, e.g. "catalog"
	Timeout  time.Duration
	Retries  int // extra attempts for idempotent reads (default 2)
	Breakers *breaker.Registry
	Metrics  *metrics.Metrics
	// Retry tuning, overridable in tests.
	Retry retry.Policy
}

// idempotentPrefixes: methods starting with these are safe to retry (docs/09 §2).
var idempotentPrefixes = []string{"Get", "List", "Check", "Calculate", "Review", "Validate", "Search"}

// IsIdempotent reports whether a full method name ("/pkg.Service/GetSku") is a read.
func IsIdempotent(fullMethod string) bool {
	m := shortMethod(fullMethod)
	for _, p := range idempotentPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

// Dial creates a lazily-connecting client with tracing and the resilience interceptor.
func Dial(cfg ClientConfig, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(ClientInterceptor(cfg)),
	}
	return grpc.NewClient(cfg.Target, append(opts, extra...)...)
}

// ClientInterceptor implements, for every unary call:
// request id + principal propagation → metrics → per-attempt deadline → breaker around each attempt →
// retries (idempotent reads, UNAVAILABLE only) → conversion of status errors to *errs.Error.
func ClientInterceptor(cfg ClientConfig) grpc.UnaryClientInterceptor {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Retries == 0 {
		cfg.Retries = 2
	}
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = outgoing(ctx)
		short := shortMethod(method)
		start := time.Now()

		attempt := func(ctx context.Context) error {
			call := func() error {
				actx, cancel := withDefaultTimeout(ctx, cfg.Timeout)
				defer cancel()
				return invoker(actx, method, req, reply, cc, opts...)
			}
			if cfg.Breakers == nil {
				return call()
			}
			_, err := breaker.Call(cfg.Breakers.Get(cfg.Name, short), func() (struct{}, error) { return struct{}{}, call() })
			return err
		}

		var err error
		if IsIdempotent(method) {
			p := cfg.Retry
			p.MaxAttempts = cfg.Retries + 1
			p.Retryable = func(e error) bool {
				var oe *breaker.OpenError
				return !errors.As(e, &oe) && status.Code(e) == codes.Unavailable
			}
			err = retry.Do(ctx, p, attempt)
		} else {
			err = attempt(ctx)
		}

		if cfg.Metrics != nil {
			cfg.Metrics.GRPCClient.WithLabelValues(cfg.Name, short, codeLabel(err)).Observe(time.Since(start).Seconds())
		}
		return FromStatus(err)
	}
}

func codeLabel(err error) string {
	var oe *breaker.OpenError
	if errors.As(err, &oe) {
		return "BREAKER_OPEN"
	}
	return status.Code(err).String()
}

func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// BreakerSettings builds breaker settings whose failure classifier is IsInfraFailure.
func BreakerSettings(s breaker.Settings) breaker.Settings {
	s.IsFailure = IsInfraFailure
	return s
}
