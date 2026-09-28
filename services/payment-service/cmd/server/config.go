package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// config is read from environment variables only, so one image runs in every
// environment.
type config struct {
	port            string
	providerURL     string
	providerAPIKey  string
	providerTimeout time.Duration
	shutdownTimeout time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		port:           getenv("PORT", "8083"),
		providerURL:    getenv("PROVIDER_URL", "https://sandbox.payprovider.example/v1/charges"),
		providerAPIKey: os.Getenv("PROVIDER_API_KEY"),
	}
	if cfg.providerAPIKey == "" {
		return cfg, errors.New("PROVIDER_API_KEY is required")
	}
	var err error
	if cfg.providerTimeout, err = time.ParseDuration(getenv("PROVIDER_TIMEOUT", "5s")); err != nil {
		return cfg, fmt.Errorf("PROVIDER_TIMEOUT: %w", err)
	}
	if cfg.shutdownTimeout, err = time.ParseDuration(getenv("SHUTDOWN_TIMEOUT", "20s")); err != nil {
		return cfg, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
