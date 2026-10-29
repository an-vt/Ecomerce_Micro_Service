package grpcx_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/retry"
)

// ---- a tiny hand-written service so tests do not depend on generated code -------------------------------------

type handlers struct {
	get    func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
	create func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
}

func method(name string, fn func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)) grpc.MethodDesc {
	return grpc.MethodDesc{MethodName: name, Handler: func(_ any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
		in := new(wrapperspb.StringValue)
		if err := dec(in); err != nil {
			return nil, err
		}
		call := func(ctx context.Context, req any) (any, error) { return fn(ctx, req.(*wrapperspb.StringValue)) }
		if ic == nil {
			return call(ctx, in)
		}
		return ic(ctx, in, &grpc.UnaryServerInfo{FullMethod: "/test.Thing/" + name}, call)
	}}
}

func (h *handlers) desc() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "test.Thing", HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{method("GetThing", h.get), method("CreateThing", h.create)},
	}
}

type env struct {
	conn *grpc.ClientConn
	m    *metrics.Metrics
	logs *bytes.Buffer
}

func start(t *testing.T, h *handlers, cfg grpcx.ClientConfig) *env {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	m := metrics.New()
	logs := &bytes.Buffer{}
	log := logger.New(logger.Options{Writer: logs, Level: "debug"})
	srv := grpcx.NewServer("thing-service", m, log)
	srv.RegisterService(h.desc(), h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cfg.Target = "passthrough:///bufnet"
	cfg.Metrics = m
	conn, err := grpcx.Dial(cfg, grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &env{conn: conn, m: m, logs: logs}
}

func (e *env) call(ctx context.Context, method string) (*wrapperspb.StringValue, error) {
	out := new(wrapperspb.StringValue)
	err := e.conn.Invoke(ctx, "/test.Thing/"+method, wrapperspb.String("in"), out)
	return out, err
}

func fastRetry() retry.Policy {
	return retry.Policy{Sleep: func(context.Context, time.Duration) error { return nil }}
}

// ---- status mapping ------------------------------------------------------------------------------------------

func TestStatusMappingRoundTripKeepsDomainCode(t *testing.T) {
	for _, kind := range []errs.Kind{errs.KindInvalidArgument, errs.KindNotFound, errs.KindAlreadyExists, errs.KindUnauthenticated,
		errs.KindPermissionDenied, errs.KindFailedPrecondition, errs.KindAborted, errs.KindResourceExhausted, errs.KindUnavailable, errs.KindDeadlineExceeded} {
		in := errs.New(kind, "SOME_CODE", "msg")
		wire := grpcx.ToStatus(in)
		require.Error(t, wire)
		assert.Equal(t, grpcx.CodeOf(kind), status.Code(wire))

		back := grpcx.FromStatus(wire)
		assert.Equal(t, kind, errs.KindOf(back), kind.String())
		assert.Equal(t, "SOME_CODE", errs.CodeOf(back))
		assert.Equal(t, "msg", errs.MessageOf(back))
		assert.Equal(t, kind, grpcx.KindOf(grpcx.CodeOf(kind)))
	}
}

func TestToStatusHidesInternalDetails(t *testing.T) {
	wire := grpcx.ToStatus(errors.New("pq: password authentication failed for user admin"))
	assert.Equal(t, codes.Internal, status.Code(wire))
	assert.Equal(t, "internal error", status.Convert(wire).Message())
	assert.NoError(t, grpcx.ToStatus(nil))

	own := status.Error(codes.PermissionDenied, "nope")
	assert.Equal(t, own, grpcx.ToStatus(own), "existing status errors pass through")
}

func TestFromStatusDefaults(t *testing.T) {
	assert.NoError(t, grpcx.FromStatus(nil))
	plain := errors.New("not a status")
	assert.Equal(t, plain, grpcx.FromStatus(plain))
	e := grpcx.FromStatus(status.Error(codes.Unavailable, "down"))
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(e))
	assert.Equal(t, errs.CodeUnavailable, errs.CodeOf(e), "no ErrorInfo: default code per kind")
	assert.Equal(t, errs.CodeNotFound, errs.CodeOf(grpcx.FromStatus(status.Error(codes.NotFound, "x"))))
	assert.Equal(t, errs.CodeValidationFailed, errs.CodeOf(grpcx.FromStatus(status.Error(codes.InvalidArgument, "x"))))
	assert.Equal(t, "FAILED_PRECONDITION", errs.CodeOf(grpcx.FromStatus(status.Error(codes.FailedPrecondition, "x"))))
	already := errs.NotFound("X", "")
	assert.Same(t, already, grpcx.FromStatus(already))
	assert.Equal(t, errs.KindInternal, grpcx.KindOf(codes.DataLoss))
	assert.Equal(t, codes.Internal, grpcx.CodeOf(errs.Kind(99)))
}

func TestInfraFailureClassification(t *testing.T) {
	for _, c := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Unknown, codes.ResourceExhausted} {
		assert.True(t, grpcx.IsInfraFailure(status.Error(c, "x")), c.String())
	}
	for _, c := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.PermissionDenied, codes.Unauthenticated, codes.Aborted, codes.Canceled} {
		assert.False(t, grpcx.IsInfraFailure(status.Error(c, "x")), c.String())
	}
	assert.False(t, grpcx.IsInfraFailure(nil))
	assert.True(t, grpcx.IsIdempotent("/ecom.catalog.v1.CatalogService/GetSku"))
	assert.True(t, grpcx.IsIdempotent("/x/CheckStock"))
	assert.False(t, grpcx.IsIdempotent("/x/CreateProduct"))
}

// ---- server ---------------------------------------------------------------------------------------------------

func TestServerMapsDomainErrorsAndRecordsMetrics(t *testing.T) {
	e := start(t, &handlers{
		get: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			return nil, errs.FailedPrecondition("INSUFFICIENT_STOCK", "only 2 left")
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			return wrapperspb.String("ok"), nil
		},
	}, grpcx.ClientConfig{Name: "thing"})

	_, err := e.call(context.Background(), "GetThing")
	require.Error(t, err)
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))
	assert.Equal(t, "INSUFFICIENT_STOCK", errs.CodeOf(err), "domain code crosses the wire")
	assert.Equal(t, "only 2 left", errs.MessageOf(err))

	out, err := e.call(context.Background(), "CreateThing")
	require.NoError(t, err)
	assert.Equal(t, "ok", out.GetValue())

	assert.Equal(t, 2, testutil.CollectAndCount(e.m.GRPCServer), "one series per (method, code)")
	assert.Contains(t, e.logs.String(), "grpc call rejected")
	assert.Contains(t, e.logs.String(), "INSUFFICIENT_STOCK")
}

func TestServerRecoversFromPanicAndHidesCause(t *testing.T) {
	e := start(t, &handlers{
		get: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			panic("boom: secret detail")
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			return nil, errors.New("db down")
		},
	}, grpcx.ClientConfig{Name: "thing", Breakers: nil, Retries: 1, Retry: fastRetry()})

	_, err := e.call(context.Background(), "GetThing")
	assert.Equal(t, errs.KindInternal, errs.KindOf(err))
	assert.NotContains(t, err.Error(), "secret detail")
	assert.Contains(t, e.logs.String(), "panic in gRPC handler")

	_, err = e.call(context.Background(), "CreateThing")
	assert.Equal(t, errs.KindInternal, errs.KindOf(err))
	assert.NotContains(t, err.Error(), "db down", "internal causes never reach the client")
	assert.Contains(t, e.logs.String(), "db down", "...but are logged")
}

func TestRequestIDAndPrincipalPropagate(t *testing.T) {
	var got principal.Principal
	var reqID string
	e := start(t, &handlers{
		get: func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			got, _ = principal.From(ctx)
			reqID = logger.RequestID(ctx)
			return wrapperspb.String("ok"), nil
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) { return nil, nil },
	}, grpcx.ClientConfig{Name: "thing"})

	ctx := logger.WithRequestID(context.Background(), "req-42")
	ctx = principal.With(ctx, principal.Principal{ID: "shop-1", Type: principal.TypeShop, Roles: []string{"shop", "writer"}, SessionID: "sid-9"})
	_, err := e.call(ctx, "GetThing")
	require.NoError(t, err)
	assert.Equal(t, "req-42", reqID)
	assert.Equal(t, principal.Principal{ID: "shop-1", Type: principal.TypeShop, Roles: []string{"shop", "writer"}, SessionID: "sid-9"}, got)

	// anonymous call: nothing is invented
	_, err = e.call(context.Background(), "GetThing")
	require.NoError(t, err)
	assert.Equal(t, "", reqID)
	assert.Equal(t, "", got.ID)
}

// ---- client resilience ------------------------------------------------------------------------------------------

func TestReadsRetryOnUnavailableWritesDoNot(t *testing.T) {
	var gets, creates atomic.Int32
	e := start(t, &handlers{
		get: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			if gets.Add(1) < 3 {
				return nil, errs.Unavailable("X", "warming up")
			}
			return wrapperspb.String("finally"), nil
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			creates.Add(1)
			return nil, errs.Unavailable("X", "down")
		},
	}, grpcx.ClientConfig{Name: "thing", Retry: fastRetry()})

	out, err := e.call(context.Background(), "GetThing")
	require.NoError(t, err)
	assert.Equal(t, "finally", out.GetValue())
	assert.Equal(t, int32(3), gets.Load(), "1 try + 2 retries")

	_, err = e.call(context.Background(), "CreateThing")
	require.Error(t, err)
	assert.Equal(t, int32(1), creates.Load(), "writes are never retried")
}

func TestBusinessErrorsAreNotRetriedAndDoNotOpenTheBreaker(t *testing.T) {
	var gets atomic.Int32
	reg := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{MinRequests: 3, ErrorRatio: 0.5, ResetTimeout: time.Minute}))
	e := start(t, &handlers{
		get: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			gets.Add(1)
			return nil, errs.NotFound("SKU_NOT_FOUND", "no such sku")
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) { return nil, nil },
	}, grpcx.ClientConfig{Name: "catalog", Breakers: reg, Retry: fastRetry()})

	for i := 0; i < 10; i++ {
		_, err := e.call(context.Background(), "GetThing")
		assert.Equal(t, "SKU_NOT_FOUND", errs.CodeOf(err))
	}
	assert.Equal(t, int32(10), gets.Load(), "NOT_FOUND is final: one attempt per call")
	assert.Equal(t, breaker.Closed, reg.Get("catalog", "GetThing").State())
}

func TestBreakerOpensAndFailsFast(t *testing.T) {
	var creates atomic.Int32
	reg := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{MinRequests: 3, ErrorRatio: 0.5, ResetTimeout: time.Minute}))
	e := start(t, &handlers{
		get: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) { return nil, nil },
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			creates.Add(1)
			return nil, errs.Unavailable("X", "down")
		},
	}, grpcx.ClientConfig{Name: "inventory", Breakers: reg})

	for i := 0; i < 3; i++ {
		_, err := e.call(context.Background(), "CreateThing")
		require.Error(t, err)
	}
	require.Equal(t, breaker.Open, reg.Get("inventory", "CreateThing").State())
	before := creates.Load()

	start := time.Now()
	_, err := e.call(context.Background(), "CreateThing")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 50*time.Millisecond, "open circuit fails fast")
	assert.Equal(t, before, creates.Load(), "the dependency was not called")
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))
	d, ok := breaker.RetryAfter(err)
	assert.True(t, ok)
	assert.Equal(t, time.Minute, d)

	// a different method of the same target is unaffected
	_, err = e.call(context.Background(), "GetThing")
	assert.NoError(t, err)
	rec := httptest.NewRecorder()
	e.m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	assert.Contains(t, rec.Body.String(), `code="BREAKER_OPEN"`)
}

func TestDefaultDeadlineIsApplied(t *testing.T) {
	e := start(t, &handlers{
		get: func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		create: func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) { return nil, nil },
	}, grpcx.ClientConfig{Name: "thing", Timeout: 50 * time.Millisecond, Retries: 1, Retry: fastRetry()})

	start := time.Now()
	_, err := e.call(context.Background(), "CreateThing") // not a read -> single attempt
	_ = err
	_, err = e.call(context.Background(), "GetThing")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, errs.KindDeadlineExceeded, errs.KindOf(err))

	// a caller-supplied deadline wins over the default
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = e.call(ctx, "GetThing")
	assert.Equal(t, errs.KindDeadlineExceeded, errs.KindOf(err))
}

func TestServerInterceptorsWithoutMetrics(t *testing.T) {
	chain := grpcx.ServerInterceptors("x", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Len(t, chain, 4, "recover, context, errors, logging")
	chain = grpcx.ServerInterceptors("x", metrics.New(), slog.Default())
	assert.Len(t, chain, 5)
}
