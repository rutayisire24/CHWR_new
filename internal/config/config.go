// Package config parses process configuration from the environment.
//
// Everything the server needs to start is read once, at startup, and any
// problem is reported as a single error before anything else is dialled.
package config

import (
	"fmt"
	"net/netip"
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

	// TrustedProxies are the peers whose X-Forwarded-For header may be
	// believed. Empty — the default — means no header is trusted and the
	// client address is always the peer's own, which is right for a service
	// reached directly.
	//
	// It is a list rather than a boolean because trusting "whatever sent the
	// header" is trusting the client: anyone can set X-Forwarded-For, and the
	// audit trail is what would carry the lie.
	TrustedProxies []netip.Prefix
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

	proxies, err := parsePrefixes(os.Getenv("TRUSTED_PROXY"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.TrustedProxies = proxies

	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if c.Env != "dev" && c.Env != "prod" {
		problems = append(problems, fmt.Sprintf("ENV must be dev or prod, got %q", c.Env))
	}

	d, derr := durationOr("SHUTDOWN_TIMEOUT", 15*time.Second)
	err = derr
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.ShutdownTimeout = d

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return c, nil
}

// TrustsProxy reports whether an address is one of the configured proxies.
func (c Config) TrustsProxy(addr netip.Addr) bool {
	for _, prefix := range c.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// parsePrefixes reads TRUSTED_PROXY: a comma-separated list of addresses or
// CIDR blocks. A bare address is the single host, so the common case —
// "127.0.0.1", a proxy on the same machine — needs no mask.
func parsePrefixes(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(field); err == nil {
			out = append(out, prefix)
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY: %q is not an address or CIDR block", field)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
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
