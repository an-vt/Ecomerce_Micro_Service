// Command order-service runs orders and the checkout saga orchestrator (gRPC + Kafka reply consumer + outbox relay + timeout sweeper).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/an-vt/ecom/order-service/internal/app"
	"github.com/an-vt/ecom/order-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
	"github.com/an-vt/ecom/platform-go/httpx"
)

func main() {
	if httpx.HealthcheckRequested(os.Args[1:]) {
		os.Exit(httpx.RunHealthcheck(platform.NewLoader().Int("HTTP_PORT", config.DefaultHTTPPort)))
	}
	cfg, err := config.Load(platform.NewLoader())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "order-service:", err)
		os.Exit(1)
	}
}
