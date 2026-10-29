package httpx_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/httpx"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(b)
}

func TestLiveAlwaysOK(t *testing.T) {
	code, body := get(t, httpx.NewOpsHandler(nil, map[string]httpx.Check{"db": func(context.Context) error { return errors.New("down") }}), "/health/live")
	assert.Equal(t, 200, code)
	assert.Contains(t, body, `"ok"`)
}

func TestReadyReflectsChecks(t *testing.T) {
	healthy := httpx.NewOpsHandler(nil, map[string]httpx.Check{"db": func(context.Context) error { return nil }})
	code, _ := get(t, healthy, "/health/ready")
	assert.Equal(t, 200, code)

	sick := httpx.NewOpsHandler(nil, map[string]httpx.Check{
		"db":    func(context.Context) error { return errors.New("connection refused") },
		"kafka": func(context.Context) error { return nil },
	})
	code, body := get(t, sick, "/health/ready")
	assert.Equal(t, 503, code)
	assert.Contains(t, body, "connection refused")
	assert.NotContains(t, body, "kafka")
}

func TestMetricsRouteOnlyWhenProvided(t *testing.T) {
	with := httpx.NewOpsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("m 1")) }), nil)
	code, body := get(t, with, "/metrics")
	assert.Equal(t, 200, code)
	assert.Equal(t, "m 1", body)

	code, _ = get(t, httpx.NewOpsHandler(nil, nil), "/metrics")
	assert.Equal(t, 404, code)
}

func TestServeAndGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- httpx.ServeListener(ctx, ln, httpx.NewOpsHandler(nil, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + ln.Addr().String() + "/health/live")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}, 3*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeReportsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	err = httpx.Serve(context.Background(), ln.Addr().String(), http.NewServeMux(), slog.Default())
	assert.ErrorContains(t, err, "listen")
}

func TestHealthcheckFlagAndRun(t *testing.T) {
	assert.True(t, httpx.HealthcheckRequested([]string{"app", "-healthcheck"}))
	assert.True(t, httpx.HealthcheckRequested([]string{"--healthcheck"}))
	assert.False(t, httpx.HealthcheckRequested([]string{"app"}))

	ok := httptest.NewServer(httpx.NewOpsHandler(nil, nil))
	defer ok.Close()
	assert.Equal(t, 0, httpx.RunHealthcheck(portOf(t, ok)))

	sick := httptest.NewServer(httpx.NewOpsHandler(nil, map[string]httpx.Check{"x": func(context.Context) error { return errors.New("no") }}))
	defer sick.Close()
	assert.Equal(t, 1, httpx.RunHealthcheck(portOf(t, sick)))

	assert.Equal(t, 1, httpx.RunHealthcheck(1), "nothing listens on port 1")
}

func portOf(t *testing.T, s *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(s.Listener.Addr().String())
	require.NoError(t, err)
	var n int
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return n
}
