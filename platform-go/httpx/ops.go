// Package httpx serves the operational endpoints every service exposes: /health/live, /health/ready and
// /metrics (docs/14-observability.md), and supports the container's `-healthcheck` flag.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"time"
)

// Check reports whether a dependency is usable; it must respect ctx's deadline.
type Check func(ctx context.Context) error

// NewOpsHandler serves /health/live (process is up), /health/ready (all checks pass) and /metrics.
func NewOpsHandler(metrics http.Handler, checks map[string]Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		failed := map[string]string{}
		names := make([]string, 0, len(checks))
		for n := range checks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if err := checks[n](ctx); err != nil {
				failed[n] = err.Error()
			}
		}
		if len(failed) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "failed": failed})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	if metrics != nil {
		mux.Handle("GET /metrics", metrics)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Serve runs handler on addr until ctx is cancelled, then shuts down gracefully (up to 10 s).
func Serve(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	return ServeListener(ctx, ln, handler, log)
}

// ServeListener is Serve on an existing listener (used by tests with port 0).
func ServeListener(ctx context.Context, ln net.Listener, handler http.Handler, log *slog.Logger) error {
	// Timeouts bound slow clients (docs/09 §3: 15 s per request at the edge).
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	log.Info("http server listening", "addr", ln.Addr().String())
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// Serve returns http.ErrServerClosed after a clean Shutdown; anything else is a real failure.
		if serr := <-errCh; err == nil && serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			err = serr
		}
		return err
	}
}

// HealthcheckRequested reports whether args contain -healthcheck (the container HEALTHCHECK runs `/app -healthcheck`).
func HealthcheckRequested(args []string) bool {
	for _, a := range args {
		if a == "-healthcheck" || a == "--healthcheck" {
			return true
		}
	}
	return false
}

// RunHealthcheck calls GET http://127.0.0.1:<port>/health/ready and returns the process exit code (0 healthy).
func RunHealthcheck(port int) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/health/ready", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
