package main

import (
	"fmt"
	"os"
	"time"
)

// config is read from environment variables only, so one image runs in every
// environment.
type config struct {
	port            string
	region          string
	table           string
	dynamoEndpoint  string // empty in AWS; set to DynamoDB Local's URL locally
	dynamoTimeout   time.Duration
	shutdownTimeout time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		port:           getenv("PORT", "8084"),
		region:         getenv("AWS_REGION", "us-east-1"),
		table:          getenv("TABLE_NAME", "stratus-inventory"),
		dynamoEndpoint: os.Getenv("DYNAMODB_ENDPOINT"),
	}
	var err error
	if cfg.dynamoTimeout, err = time.ParseDuration(getenv("DYNAMODB_TIMEOUT", "2s")); err != nil {
		return cfg, fmt.Errorf("DYNAMODB_TIMEOUT: %w", err)
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
