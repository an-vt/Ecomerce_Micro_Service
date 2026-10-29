// Command api-gateway runs the public REST API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/an-vt/ecom/api-gateway/internal/app"
	"github.com/an-vt/ecom/api-gateway/internal/config"
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
		fmt.Fprintln(os.Stderr, "api-gateway:", err)
		os.Exit(1)
	}
}
