// Command server runs payment-service.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stratus/payment-service/internal/handler"
	"github.com/stratus/payment-service/internal/repo"
)

func main() {
	// JSON logs to stdout: what `docker logs`, `kubectl logs` and Fluent Bit read.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "payment-service")

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	h := handler.New(logger, repo.NewMemory(), &http.Client{Timeout: cfg.providerTimeout})

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second, // protects against slow-header (Slowloris) attacks
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.port)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr: // e.g. the port is already in use
		logger.Error("server failed", "error", err)
		os.Exit(1)
	case <-ctx.Done(): // SIGTERM from docker stop / Kubernetes
	}

	// Graceful shutdown: fail readiness, stop accepting, finish in-flight requests.
	logger.Info("shutdown started")
	h.StartDraining()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown timed out", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
