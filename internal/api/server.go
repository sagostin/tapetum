package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/config"
	"github.com/sagostin/tapetum/internal/events"
	"github.com/sagostin/tapetum/internal/export"
	"github.com/sagostin/tapetum/internal/ingest"
	"github.com/sagostin/tapetum/internal/live"
	"github.com/sagostin/tapetum/internal/notify"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/settings"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/transcode"
	"github.com/sagostin/tapetum/internal/ws"
)

// Server holds the API's dependencies.
type Server struct {
	cfg          *config.Config
	pool         *pgxpool.Pool
	auth         *auth.Manager
	limiter      *auth.LoginLimiter
	hub          *ws.Hub
	cams         *camera.Store
	segs         *record.Store
	backend      storage.Backend
	resolve      storage.Resolver
	s3m          *storage.S3Manager
	settings     *settings.Store
	ingest       *ingest.Supervisor
	exporter     *export.Worker
	transcode    *transcode.Service
	liveHub      *live.Hub
	evStore      *events.Store
	notifyStore  *notify.Store
	notifyWorker *notify.Worker
	// OnCameraChange fans out camera CRUD to subsystems (detect, ONVIF pull)
	// beyond ingest, which Server already owns.
	OnCameraChange func(ctx context.Context, camID string, deleted bool)
	probeLimiter   *probeLimiter
	version        string
	started        time.Time
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool, hub *ws.Hub, version string,
	cams *camera.Store, segs *record.Store, backend storage.Backend,
	resolve storage.Resolver, s3m *storage.S3Manager, st *settings.Store,
	ing *ingest.Supervisor, exp *export.Worker, tc *transcode.Service,
	liveHub *live.Hub,
	evStore *events.Store, notifyStore *notify.Store, notifyWorker *notify.Worker,
) *Server {
	return &Server{
		cfg:          cfg,
		pool:         pool,
		auth:         auth.NewManager(pool, cfg.Server.Dev),
		limiter:      auth.NewLoginLimiter(),
		hub:          hub,
		cams:         cams,
		segs:         segs,
		backend:      backend,
		resolve:      resolve,
		s3m:          s3m,
		settings:     st,
		ingest:       ing,
		exporter:     exp,
		transcode:    tc,
		liveHub:      liveHub,
		evStore:      evStore,
		notifyStore:  notifyStore,
		notifyWorker: notifyWorker,
		probeLimiter: &probeLimiter{hits: map[string][]time.Time{}},
		version:      version,
		started:      time.Now(),
	}
}

// syncCamera fans a camera change out to ingest + registered subsystems
// (detect engines, ONVIF pull-point clients).
func (s *Server) syncCamera(ctx context.Context, camID string, deleted bool) {
	if deleted {
		s.ingest.Remove(camID)
	} else {
		s.ingest.Sync(ctx, camID)
	}
	if s.OnCameraChange != nil {
		s.OnCameraChange(ctx, camID, deleted)
	}
}

// Router assembles all routes and middleware.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.logRequests)
	r.Use(securityHeaders)
	r.Use(s.auth.Middleware)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.auth.CSRF)

		// Setup & health (unauthenticated).
		r.Get("/setup/status", s.setupStatus)
		r.With(s.requireSetupPending).Post("/setup", s.setup)
		r.Get("/system/health", s.health)

		// Auth.
		r.Post("/auth/login", s.login)
		r.With(s.requireAuth).Post("/auth/logout", s.logout)
		r.With(s.requireAuth).Get("/auth/me", s.me)
		r.With(s.requireAuth).Post("/auth/password", s.changePassword)
		r.With(s.requireAuth).Get("/auth/tokens", s.listTokens)
		r.With(s.requireAuth).Post("/auth/tokens", s.createToken)
		r.With(s.requireAuth).Delete("/auth/tokens/{id}", s.deleteToken)

		// Users & roles (admin).
		r.With(s.require(auth.PermUsersWrite)).Get("/users", s.listUsers)
		r.With(s.require(auth.PermUsersWrite)).Post("/users", s.createUser)
		r.With(s.require(auth.PermUsersWrite)).Get("/users/{id}", s.getUser)
		r.With(s.require(auth.PermUsersWrite)).Patch("/users/{id}", s.updateUser)
		r.With(s.require(auth.PermUsersWrite)).Delete("/users/{id}", s.deleteUser)
		r.With(s.require(auth.PermUsersWrite)).Get("/roles", s.roles)
		r.With(s.require(auth.PermUsersWrite)).Get("/audit-log", s.auditLog)

		// System.
		r.With(s.requireAuth).Get("/system/info", s.info)
		r.With(s.require(auth.PermSettingsWrite)).Get("/system/settings", s.getSettings)
		r.With(s.require(auth.PermSettingsWrite)).Put("/system/settings", s.putSettings)
		r.With(s.require(auth.PermSettingsWrite)).Get("/system/storage", s.storageOverview)

		// Cameras.
		r.With(s.require(auth.PermLive)).Get("/cameras", s.listCameras)
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras", s.createCamera)
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras/probe", s.probeCamera)
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras/discover", s.discoverCameras)
		r.With(s.require(auth.PermLive)).Get("/cameras/{id}", s.getCamera)
		r.With(s.require(auth.PermCamerasWrite)).Patch("/cameras/{id}", s.updateCamera)
		r.With(s.require(auth.PermCamerasWrite)).Delete("/cameras/{id}", s.deleteCamera)
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras/{id}/enable", s.setCameraEnabled(true))
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras/{id}/disable", s.setCameraEnabled(false))
		r.With(s.require(auth.PermCamerasWrite)).Post("/cameras/{id}/onvif/sync", s.onvifSync)
		r.With(s.require(auth.PermLive)).Get("/cameras/{id}/snapshot", s.cameraSnapshot)
		r.With(s.require(auth.PermLive)).Get("/cameras/{id}/stats", s.cameraStats)
		r.With(s.require(auth.PermCamerasWrite)).Patch("/cameras/{id}/display", s.updateCameraDisplay)

		// PTZ & imaging.
		r.With(s.require(auth.PermPTZ)).Post("/cameras/{id}/ptz/move", s.ptzMove)
		r.With(s.require(auth.PermPTZ)).Post("/cameras/{id}/ptz/stop", s.ptzStop)
		r.With(s.require(auth.PermPTZ)).Get("/cameras/{id}/ptz/presets", s.ptzPresets)
		r.With(s.require(auth.PermPTZ)).Post("/cameras/{id}/ptz/presets", s.ptzSavePreset)
		r.With(s.require(auth.PermPTZ)).Post("/cameras/{id}/ptz/presets/{token}/goto", s.ptzGotoPreset)
		r.With(s.require(auth.PermPTZ)).Delete("/cameras/{id}/ptz/presets/{token}", s.ptzDeletePreset)
		r.With(s.require(auth.PermPTZ)).Get("/cameras/{id}/imaging", s.imagingGet)
		r.With(s.require(auth.PermPTZ)).Put("/cameras/{id}/imaging", s.imagingPut)

		// Live streaming — HLS only (docs/03-api.md). MJPEG is the in-band
		// fallback when the recorder hasn't produced a segment yet.
		r.With(s.require(auth.PermLive)).Get("/streams/{cameraId}/mjpeg", s.mjpeg)
		r.With(s.require(auth.PermLive)).Get("/streams/{cameraId}/live.m3u8", s.livePlaylist)
		r.With(s.require(auth.PermLive)).Get("/streams/{cameraId}/live.mp4", s.liveMP4)

		// Recordings & playback.
		r.With(s.require(auth.PermPlayback)).Get("/recordings/timeline", s.timeline)
		r.With(s.require(auth.PermPlayback)).Get("/recordings/availability", s.availability)
		r.With(s.require(auth.PermPlayback)).Get("/playback/{cameraId}/playlist.m3u8", s.playbackPlaylist)
		r.With(s.require(auth.PermPlayback)).Get("/playback/{cameraId}/init.mp4", s.initMP4)
		r.With(s.require(auth.PermPlayback)).Get("/segments/{id}", s.serveSegment)
		r.With(s.require(auth.PermPlayback)).Post("/segments/protect", s.protectSegments)
		r.With(s.require(auth.PermPlayback)).Delete("/segments/protect/{id}", s.unprotectSegment)

		// Exports.
		r.With(s.require(auth.PermExport)).Post("/exports", s.createExport)
		r.With(s.require(auth.PermExport)).Get("/exports", s.listExports)
		r.With(s.require(auth.PermExport)).Get("/exports/{id}/download", s.downloadExport)

		// Events.
		r.With(s.require(auth.PermEvents)).Get("/events", s.listEvents)
		r.With(s.require(auth.PermEvents)).Get("/events/{id}", s.getEvent)
		r.With(s.require(auth.PermEvents)).Get("/events/{id}/snapshot.jpg", s.eventSnapshot)
		r.With(s.require(auth.PermEvents)).Get("/events/{id}/clip.m3u8", s.eventClip)
		r.With(s.require(auth.PermEvents)).Post("/events/{id}/ack", s.ackEvent)
		r.With(s.require(auth.PermCamerasWrite)).Delete("/events/{id}", s.deleteEvent)

		// Notifications.
		r.With(s.require(auth.PermSettingsWrite)).Get("/notify/channels", s.listChannels)
		r.With(s.require(auth.PermSettingsWrite)).Post("/notify/channels", s.createChannel)
		r.With(s.require(auth.PermSettingsWrite)).Patch("/notify/channels/{id}", s.updateChannel)
		r.With(s.require(auth.PermSettingsWrite)).Delete("/notify/channels/{id}", s.deleteChannel)
		r.With(s.require(auth.PermSettingsWrite)).Post("/notify/channels/{id}/test", s.testChannel)
		r.With(s.require(auth.PermSettingsWrite)).Get("/notify/rules", s.listRules)
		r.With(s.require(auth.PermSettingsWrite)).Post("/notify/rules", s.createRule)
		r.With(s.require(auth.PermSettingsWrite)).Patch("/notify/rules/{id}", s.updateRule)
		r.With(s.require(auth.PermSettingsWrite)).Delete("/notify/rules/{id}", s.deleteRule)
		r.With(s.require(auth.PermSettingsWrite)).Get("/notify/log", s.notifyLog)
	})

	// WebSocket.
	r.With(s.requireAuth).Get("/ws", s.hub.ServeHTTP)

	// SPA fallback for everything else.
	r.NotFound(s.spaHandler())

	return r
}

// --- middleware ------------------------------------------------------------

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserFrom(r.Context()) == nil {
			Error(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) require(perm auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context())
			if u == nil {
				Error(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
			if !u.Role.Has(perm) {
				Error(w, http.StatusForbidden, "forbidden", "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/api/v1/system/health" {
			return // health checks are noise
		}
		slog.Debug("http", "method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "dur", time.Since(start).Round(time.Microsecond))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; "+
				"connect-src 'self' ws: wss:; worker-src 'self' blob:")
		next.ServeHTTP(w, r)
	})
}
