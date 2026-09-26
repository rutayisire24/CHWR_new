// Command server runs the National Community Health Worker Registry.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chwr/internal/auth"
	"chwr/internal/config"
	"chwr/internal/db"
	"chwr/internal/domain"
	apphttp "chwr/internal/http"
	"chwr/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate", false, "apply migrations and exit")
	adminEmail := flag.String("create-admin", "", "provision a national admin with this email, print a temporary password, and exit")
	adminName := flag.String("name", "", "full name for -create-admin")
	apiClientID := flag.String("create-api-client", "", "provision a read-only interoperability API client with this client_id, print a secret, and exit")
	apiClientName := flag.String("api-name", "", "human label for -create-api-client (e.g. \"eCHIS UMT\")")
	apiDistrict := flag.Int64("api-district", 0, "restrict the -create-api-client client to this district id; 0 (default) is national")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelFor(cfg),
	})))

	// Signals cancel this context; the pool and the listener both hang off it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migrations run on every start: one binary, one schema, no separate step
	// to forget during a deploy.
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	version, err := db.Version(ctx, pool)
	if err != nil {
		return err
	}
	slog.Info("schema ready", "version", version)

	if *migrateOnly {
		return nil
	}
	if *adminEmail != "" {
		return bootstrapAdmin(ctx, pool, *adminEmail, *adminName)
	}
	if *apiClientID != "" {
		return bootstrapAPIClient(ctx, pool, *apiClientID, *apiClientName, *apiDistrict)
	}

	handler, err := apphttp.New(pool, cfg)
	if err != nil {
		return err
	}

	// Expired sessions are refused by Authenticate whether or not they have
	// been swept; this keeps the table from growing without bound.
	go purgeSessions(ctx, store.New(pool))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		slog.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func levelFor(cfg config.Config) slog.Level {
	if cfg.Prod() {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// bootstrapAdmin provisions the first national administrator, for a database
// that has none. The password is generated here and printed once: an admin
// account is a handover, and must_reset makes the printed value good for
// exactly one sign-in.
func bootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, email, fullName string) error {
	if fullName == "" {
		return fmt.Errorf("-create-admin also needs -name")
	}

	password, err := generatePassword()
	if err != nil {
		return err
	}

	st := store.New(pool)
	user, err := st.Users.Create(ctx, auth.National(), store.NewUser{
		Email:    email,
		FullName: fullName,
		Role:     domain.RoleNationalAdmin,
		Password: password,
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("an account already exists for %s", email)
		}
		return err
	}

	fmt.Printf("\nCreated national administrator %s (id %d)\n", user.Email, user.ID)
	fmt.Printf("Temporary password: %s\n", password)
	fmt.Printf("It must be changed at first sign-in.\n\n")
	return nil
}

// bootstrapAPIClient provisions a read-only interoperability API client and
// prints its secret once, the same handover shape as -create-admin. The secret
// is stored only as an argon2id hash; this printed value is the only copy.
func bootstrapAPIClient(ctx context.Context, pool *pgxpool.Pool, clientID, name string, districtID int64) error {
	if name == "" {
		return fmt.Errorf("-create-api-client also needs -api-name")
	}

	secret, err := generatePassword()
	if err != nil {
		return err
	}

	in := store.NewAPIClient{
		Name:     name,
		ClientID: clientID,
		Secret:   secret,
		Scope:    domain.APIScopeNational,
	}
	if districtID != 0 {
		in.Scope = domain.APIScopeDistrict
		in.DistrictID = &districtID
	}

	st := store.New(pool)
	client, err := st.APIClients.Create(ctx, in)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("an API client already exists with client_id %q", clientID)
		}
		return err
	}

	fmt.Printf("\nCreated read-only API client %q (id %d, scope %s)\n", client.ClientID, client.ID, client.Scope)
	fmt.Printf("Client secret: %s\n", secret)
	fmt.Printf("Store it now: only its hash is kept, and it cannot be shown again.\n\n")
	return nil
}

// generatePassword draws until the result satisfies the same policy the web
// form enforces.
func generatePassword() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		candidate := base64.RawURLEncoding.EncodeToString(raw)
		if auth.CheckPassword(candidate) == "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("generate password: no acceptable candidate in 8 draws")
}

// purgeSessions deletes rows past their absolute expiry, hourly, until the
// process is shutting down.
func purgeSessions(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := st.Sessions.DeleteExpired(ctx)
			if err != nil {
				slog.Error("session purge failed", "err", err)
			} else if n > 0 {
				slog.Info("purged expired sessions", "count", n)
			}
			if m, err := st.APIClients.DeleteExpiredTokens(ctx); err != nil {
				slog.Error("api token purge failed", "err", err)
			} else if m > 0 {
				slog.Info("purged expired api tokens", "count", m)
			}
		}
	}
}
