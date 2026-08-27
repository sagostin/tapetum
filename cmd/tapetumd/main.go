// Tapetum NVR daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sagostin/tapetum/internal/api"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/config"
	"github.com/sagostin/tapetum/internal/db"
	"github.com/sagostin/tapetum/internal/export"
	"github.com/sagostin/tapetum/internal/ingest"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// version is set via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	setupLogger(cfg.Log.Level)

	// Ensure the server key exists (encrypts camera passwords at rest).
	serverKey, err := cfg.ServerKey()
	if err != nil {
		slog.Error("server key", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.Database.URL)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	slog.Info("database migrated")

	hub := ws.NewHub()

	backend, err := storage.NewLocal(cfg.Server.DataDir)
	if err != nil {
		slog.Error("storage init failed", "err", err)
		os.Exit(1)
	}
	cams, err := camera.NewStore(pool, serverKey)
	if err != nil {
		slog.Error("camera store init failed", "err", err)
		os.Exit(1)
	}
	segs := record.NewStore(pool)
	ing := ingest.NewSupervisor(cams, segs, backend, hub)
	exp := export.NewWorker(pool, segs, cams, backend, hub, cfg.Server.DataDir)
	janitor := record.NewJanitor(segs, cams, backend, hub)

	if err := ing.Start(ctx); err != nil {
		slog.Error("ingest start failed", "err", err)
		os.Exit(1)
	}
	defer ing.Close()
	go janitor.Run(ctx)

	srv := api.NewServer(cfg, pool, hub, version, cams, segs, backend, ing, exp)

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming/WebSocket — no write deadline
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		slog.Info("tapetum listening", "addr", cfg.Server.Addr, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func setupLogger(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}
