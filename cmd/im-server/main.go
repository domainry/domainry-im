package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/domainry/domainry-im/internal/assembly/saas"
	"github.com/domainry/domainry-im/internal/infrastructure/config"
)

func main() {
	if err := run(); err != nil {
		slog.Error("domainry-im stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.FromEnvironment()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service, err := saas.Open(ctx, settings)
	if err != nil {
		return err
	}
	defer service.Close(context.Background())
	server := &http.Server{Addr: settings.ListenAddress, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	slog.Info("domainry-im listening", "address", settings.ListenAddress, "database", settings.DatabasePath)
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}
