package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"campusretail/internal/app"
	"campusretail/internal/config"
	"campusretail/internal/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if cfg.Env == "local" {
		logger.Info("local environment", "paymentMode", cfg.PaymentMode, "demoSeed", cfg.SeedDemo)
	}
	if cfg.PaymentMode == "wechat" && (cfg.WeChatMchID == "" || cfg.WeChatPrivateKey == "" || cfg.WeChatAPIv3Key == "") {
		logger.Error("PAYMENT_MODE=wechat requires merchant credentials; refusing to pretend payments work")
		os.Exit(2)
	}

	database, err := db.Open(cfg.MySQLDSN)
	if err != nil {
		logger.Error("mysql unavailable; check MYSQL_DSN and server", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	if cfg.AUTO_MIGRATE {
		if err := db.Migrate(database, cfg.MigrationsDir); err != nil {
			logger.Error("migration failed", "error", err)
			os.Exit(1)
		}
	}
	if applied, err := verifySchema(database, cfg.MigrationsDir); err != nil {
		logger.Error("schema verification failed", "error", err)
		os.Exit(1)
	} else if applied > 0 {
		logger.Info("schema verified", "migrations", applied)
	}

	handler, err := app.New(cfg, database, logger)
	if err != nil {
		logger.Error("application initialization failed", "error", err)
		os.Exit(1)
	}
	defer handler.Close()
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", cfg.HTTPAddr)
		errCh <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

// verifySchema refuses to serve a database that is behind the shipped migrations.
// db.Migrate records a file only after every statement in it succeeded, so a lower
// count always means "this database was never migrated to the running version" -
// which otherwise shows up later as missing tables during a business call.
func verifySchema(database *sql.DB, dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		// The image may ship without the migrations directory; never guess.
		return 0, nil
	}
	var applied int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	if applied != len(files) {
		return applied, fmt.Errorf("schema mismatch: %d migrations applied, %d shipped in %s; migrate this database or point MYSQL_DSN at a migrated one", applied, len(files), dir)
	}
	return applied, nil
}
