package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres:///chwr")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" || c.Env != "dev" || c.ShutdownTimeout != 15*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Prod() {
		t.Fatal("dev config reported as prod")
	}
}

// A misconfigured deploy should need one restart to diagnose, not one per fault.
func TestLoadReportsEveryProblem(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ENV", "staging")
	t.Setenv("SHUTDOWN_TIMEOUT", "nope")

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "ENV", "SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %s: %v", want, err)
		}
	}
}
