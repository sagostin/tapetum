// Tapetum NVR daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on DefaultServeMux (pprof listener)
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sagostin/tapetum/internal/api"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/config"
	"github.com/sagostin/tapetum/internal/db"
	"github.com/sagostin/tapetum/internal/detect"
	"github.com/sagostin/tapetum/internal/events"
	"github.com/sagostin/tapetum/internal/export"
	"github.com/sagostin/tapetum/internal/ingest"
	"github.com/sagostin/tapetum/internal/live"
	"github.com/sagostin/tapetum/internal/notify"
	"github.com/sagostin/tapetum/internal/onvif"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/settings"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/transcode"
	"github.com/sagostin/tapetum/internal/webrtc"
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
	hub.SetDev(cfg.Server.Dev)
	liveHub := live.NewHub()

	backend, err := storage.NewLocal(cfg.Server.DataDir)
	if err != nil {
		slog.Error("storage init failed", "err", err)
		os.Exit(1)
	}
	st, err := settings.NewStore(pool, serverKey)
	if err != nil {
		slog.Error("settings store init failed", "err", err)
		os.Exit(1)
	}
	s3m := storage.NewS3Manager(st)
	resolve := storage.NewResolver(backend, s3m)

	cams, err := camera.NewStore(pool, serverKey)
	if err != nil {
		slog.Error("camera store init failed", "err", err)
		os.Exit(1)
	}
	segs := record.NewStore(pool)
	ing := ingest.NewSupervisor(cams, segs, backend, hub, liveHub)
	exp := export.NewWorker(pool, segs, cams, backend, resolve, hub, cfg.Server.DataDir)
	janitor := record.NewJanitor(segs, cams, backend, resolve, hub)
	tierer := record.NewTierer(segs, cams, backend, s3m)

	// Phase 3: event pipeline — bus → events manager (rows, snapshots,
	// protection, WS) + notify worker; detect engines + ONVIF pull-point
	// publish motion signals onto the bus.
	bus := events.NewBus()
	evStore := events.NewStore(pool)
	if err := evStore.EnsurePartitions(ctx); err != nil {
		slog.Error("event partitions", "err", err)
		os.Exit(1)
	}
	if n, err := evStore.CloseAllOpen(ctx); err != nil {
		slog.Warn("closing stale open events", "err", err)
	} else if n > 0 {
		slog.Info("closed stale open events", "count", n)
	}
	evMgr := events.NewManager(evStore, cams, segs, backend, hub, bus, ing.Snapshot)
	detectSup := detect.NewSupervisor(cams, liveHub, bus)
	pullSup := onvif.NewPullSupervisor(cams, bus)
	notifyStore := notify.NewStore(pool, st)
	notifyWorker := notify.NewWorker(notifyStore, evStore, cams, bus, backend, cfg.Server.PublicURL)

	tc, err := transcode.NewService(cfg.Server.DataDir)
	if err != nil {
		slog.Error("transcode init failed", "err", err)
		os.Exit(1)
	}
	rtc := webrtc.NewServer(liveHub)
	defer rtc.Close()

	if err := ing.Start(ctx); err != nil {
		slog.Error("ingest start failed", "err", err)
		os.Exit(1)
	}
	defer ing.Close()
	if err := detectSup.Start(ctx); err != nil {
		slog.Error("detect start failed", "err", err)
		os.Exit(1)
	}
	defer detectSup.Close()
	if err := pullSup.Start(ctx); err != nil {
		slog.Error("onvif pull-point start failed", "err", err)
		os.Exit(1)
	}
	defer pullSup.Close()
	go janitor.Run(ctx)
	go tierer.Run(ctx)
	go evMgr.Run(ctx)
	go notifyWorker.Run(ctx)
	go bridgeMotionToRecorder(ctx, bus, ing)
	go partitionTicker(ctx, evStore)

	srv := api.NewServer(cfg, pool, hub, version, cams, segs, backend, resolve,
		s3m, st, ing, exp, rtc, tc, evStore, notifyStore, notifyWorker)
	srv.OnCameraChange = func(ctx context.Context, camID string, deleted bool) {
		if deleted {
			detectSup.Remove(camID)
			pullSup.Remove(camID)
			return
		}
		detectSup.Sync(ctx, camID)
		pullSup.Sync(ctx, camID)
	}

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

	// Optional pprof listener (TAPETUM_PPROF_ADDR / server.pprof_addr).
	// Separate port, no auth — bind it to localhost / keep it unpublished.
	var pprofSrv *http.Server
	if cfg.Server.PprofAddr != "" {
		pprofSrv = &http.Server{
			Addr:              cfg.Server.PprofAddr,
			Handler:           http.DefaultServeMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			slog.Info("pprof listening", "addr", cfg.Server.PprofAddr)
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("pprof server failed", "err", err)
			}
		}()
	}

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if pprofSrv != nil {
		_ = pprofSrv.Shutdown(shutdownCtx)
	}
}

// bridgeMotionToRecorder toggles record_mode=motion recording windows from
// bus motion signals.
func bridgeMotionToRecorder(ctx context.Context, bus *events.Bus, ing *ingest.Supervisor) {
	started, stopS := bus.Subscribe("motion.started")
	ended, stopE := bus.Subscribe("motion.ended")
	defer stopS()
	defer stopE()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-started:
			ing.MotionSignal(msg.CameraID, true)
		case msg := <-ended:
			ing.MotionSignal(msg.CameraID, false)
		}
	}
}

// partitionTicker keeps events partitions created ahead of time.
func partitionTicker(ctx context.Context, evStore *events.Store) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := evStore.EnsurePartitions(ctx); err != nil {
				slog.Warn("event partition maintenance", "err", err)
			}
		}
	}
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
