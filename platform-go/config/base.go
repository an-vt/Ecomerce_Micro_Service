package config

import "time"

// Base holds the settings every service shares (docs/11 §3.1).
type Base struct {
	ServiceName string
	Env         string // local | staging | production
	HTTPPort    int    // ops port (gateway: public REST port)
	GRPCPort    int    // 0 when the service has no gRPC server
	LogLevel    string

	KafkaBrokers []string
	RedisAddr    string
	OTLPEndpoint string // empty disables tracing export

	GRPCTimeout  time.Duration
	BreakerError float64 // failure ratio that opens the circuit, 0..1
	BreakerMin   int     // minimum requests in the window before it can open
	BreakerWin   time.Duration
	BreakerReset time.Duration
}

// IsProduction reports whether the service runs in production (stricter checks, no destructive CLI commands).
func (b Base) IsProduction() bool { return b.Env == "production" }

// LoadBase reads the shared settings. defaultHTTP / defaultGRPC are the service's own ports.
func LoadBase(l *Loader, service string, defaultHTTP, defaultGRPC int) Base {
	pct := l.Int("BREAKER_ERROR_PCT", 50)
	if pct < 1 || pct > 100 {
		l.fail("BREAKER_ERROR_PCT: %d must be between 1 and 100", pct)
		pct = 50
	}
	return Base{
		ServiceName:  l.String("SERVICE_NAME", service),
		Env:          l.String("APP_ENV", "local"),
		HTTPPort:     l.Int("HTTP_PORT", defaultHTTP),
		GRPCPort:     l.Int("GRPC_PORT", defaultGRPC),
		LogLevel:     l.String("LOG_LEVEL", "info"),
		KafkaBrokers: l.Strings("KAFKA_BROKERS", []string{"localhost:9094"}),
		RedisAddr:    l.String("REDIS_ADDR", "localhost:6379"),
		OTLPEndpoint: l.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		GRPCTimeout:  l.DurationMS("GRPC_TIMEOUT_MS", 2*time.Second),
		BreakerError: float64(pct) / 100,
		BreakerMin:   l.Int("BREAKER_VOLUME", 10),
		BreakerWin:   l.DurationMS("BREAKER_WINDOW_MS", 10*time.Second),
		BreakerReset: l.DurationMS("BREAKER_RESET_MS", 30*time.Second),
	}
}
