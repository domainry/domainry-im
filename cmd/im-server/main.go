package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/domainry/domainry-foundation/processhost"
	"github.com/domainry/domainry-im/internal/assembly/saas"
	"github.com/domainry/domainry-im/internal/infrastructure/config"
)

func main() {
	if run() != nil {
		os.Exit(1)
	}
}

func run() (runErr error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	observability, err := processhost.Open(ctx, processhost.Config{ServiceName: "domainry-im"})
	if err != nil {
		return err
	}
	defer func() { runErr = observability.Finish(context.Background(), nil, runErr) }()
	settings, err := config.FromEnvironment()
	if err != nil {
		return err
	}
	service, err := saas.Open(ctx, settings)
	if err != nil {
		return err
	}
	defer service.Close(context.Background())
	server := &http.Server{Addr: settings.ListenAddress, Handler: observability.Handler("public", service.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	observability.Started(ctx, map[string]any{"listener_group": "public"})
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		observability.Stopping(context.Background(), map[string]any{"reason": "signal"})
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}
