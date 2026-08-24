// Package config parses process configuration from the environment.
//
// Everything the server needs to start is read once, at startup, and any
// problem is reported as a single error before anything else is dialled.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	// DatabaseURL is a libpq connection string. Required; there is no default,
	// because a wrong-database default is worse than a refusal to start.
	DatabaseURL string
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Env is "dev" or "prod". Production hardening keys off this.
	Env string
	// ShutdownTimeout bounds how long in-flight requests may finish.
	ShutdownTimeout time.Duration
}

// Prod reports whether the process is running in production configuration.
func (c Config) Prod() bool { return c.Env == "prod" }

// Load reads the environment. It returns every problem it finds, not just
// the first, so a misconfigured deploy needs one restart to diagnose.
func Load() (Config, error) {
	var problems []string

	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Addr:        envOr("ADDR", ":8080"),
		Env:         envOr("ENV", "dev"),
	}

	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if c.Env != "dev" && c.Env != "prod" {
		problems = append(problems, fmt.Sprintf("ENV must be dev or prod, got %q", c.Env))
	}

	d, err := durationOr("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.ShutdownTimeout = d

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
