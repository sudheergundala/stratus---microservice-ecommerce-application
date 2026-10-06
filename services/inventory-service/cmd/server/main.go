// Command server runs inventory-service.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/stratus/inventory-service/internal/handler"
	"github.com/stratus/inventory-service/internal/repo"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "inventory-service")

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	// Credentials come from the standard AWS chain: EKS Pod Identity in the
	// cluster, environment variables locally. Never from code or the image.
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.region))
	if err != nil {
		logger.Error("load AWS config", "error", err)
		os.Exit(2)
	}
	db := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if cfg.dynamoEndpoint != "" {
			o.BaseEndpoint = aws.String(cfg.dynamoEndpoint) // DynamoDB Local
		}
	})
	endpoint := cfg.dynamoEndpoint
	if endpoint == "" {
		endpoint = "aws"
	}

	h := handler.New(logger, repo.NewDynamoStore(db, cfg.table), cfg.dynamoTimeout)

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.port, "table", cfg.table, "dynamodb", endpoint)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		logger.Error("server failed", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

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
