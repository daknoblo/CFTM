// Package server exposes the HTTP interface: server-rendered pages, htmx
// partials and a small read-only JSON API.
package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/logbuf"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/version"
	"github.com/daknoblo/CFTM/internal/web"
)

// maxRequestBody caps request bodies; every endpoint here takes at most a form.
const maxRequestBody = 1 << 20

// Config carries the non-secret settings surfaced in the UI.
type Config struct {
	AccountID     string
	PollInterval  time.Duration
	RetentionDays int
	ProbeEnabled  bool
	ProbeToken    bool
	AuditEnabled  bool

	// ExpectedPublic names hostnames that are published without Access on
	// purpose, keyed lowercase.
	ExpectedPublic map[string]bool
}

// Server renders the UI and serves the JSON API.
type Server struct {
	store     *store.Store
	collector *collector.Collector
	prober    collector.Prober
	logs      *logbuf.Buffer
	log       *slog.Logger
	cfg       Config

	assetVersion string
	static       http.Handler
}

// New wires a Server. The prober may be nil when probing is disabled.
func New(st *store.Store, coll *collector.Collector, p collector.Prober, logs *logbuf.Buffer, cfg Config, log *slog.Logger) (*Server, error) {
	staticSub, err := fs.Sub(web.StaticFS, "assets/static")
	if err != nil {
		return nil, err
	}
	staticHandler, err := newStaticHandler(staticSub)
	if err != nil {
		return nil, err
	}

	return &Server{
		store:        st,
		collector:    coll,
		prober:       p,
		logs:         logs,
		log:          log,
		cfg:          cfg,
		assetVersion: assetVersion(),
		static:       staticHandler,
	}, nil
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", s.static)

	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /tunnels/{id}", s.handleTunnel)
	mux.HandleFunc("GET /ingress", s.handleIngress)
	mux.HandleFunc("GET /audit", s.handleAudit)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /logs", s.handleLogs)
	mux.HandleFunc("GET /about", s.handleAbout)

	mux.HandleFunc("GET /partials/tunnels", s.handlePartialTunnels)
	mux.HandleFunc("GET /partials/poll-status", s.handlePartialPollStatus)
	mux.HandleFunc("GET /partials/events", s.handlePartialEvents)
	mux.HandleFunc("GET /partials/log", s.handlePartialLog)

	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("POST /probe", s.handleProbe)

	mux.HandleFunc("GET /api/tunnels", s.handleAPITunnels)
	mux.HandleFunc("GET /api/tunnels/{id}", s.handleAPITunnel)
	mux.HandleFunc("GET /api/ingress", s.handleAPIIngress)
	mux.HandleFunc("GET /api/events", s.handleAPIEvents)
	mux.HandleFunc("GET /api/status", s.handleAPIStatus)

	// CrossOriginProtection rejects unsafe cross-origin browser requests via
	// Sec-Fetch-Site / Origin, which covers CSRF without per-form tokens.
	csrf := http.NewCrossOriginProtection()
	return logRequests(s.log, securityHeaders(limitRequestBody(csrf.Handler(mux))))
}

func (s *Server) layout(r *http.Request, title string) web.Layout {
	return web.Layout{
		Title:        title,
		ActivePath:   activePath(r.URL.Path),
		AssetVersion: s.assetVersion,
		Version:      version.Get().Version,
	}
}

// activePath collapses tunnel detail pages onto the dashboard nav entry.
func activePath(path string) string {
	if strings.HasPrefix(path, "/tunnels/") {
		return "/"
	}
	return path
}

func (s *Server) pollStatus() web.PollStatus {
	status := s.collector.Status()
	return web.PollStatus{
		LastRun:       status.LastRun,
		LastSuccess:   status.LastSuccess,
		DurationMS:    status.DurationMS,
		Error:         status.LastError,
		RateKnown:     status.RateLimit.Known(),
		RateRemaining: status.RateLimit.Remaining,
		RateQuota:     status.RateLimit.Quota,
		RateResetAt:   status.RateLimit.ResetAt,
		ProbeEnabled:  s.cfg.ProbeEnabled,
		ProbeToken:    s.cfg.ProbeToken,
	}
}

// assetVersion busts caches per build, falling back to the process start time
// for development builds where the commit is unknown.
func assetVersion() string {
	if commit := version.Get().Commit; commit != "" && commit != "unknown" {
		return commit
	}
	return strconv.FormatInt(time.Now().Unix(), 36)
}

// securityHeaders applies a restrictive policy set; the CSP intentionally keeps
// script-src at 'self' because all JavaScript is vendored.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"font-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// logRequests logs mutating and page requests, skipping the polling traffic
// that would otherwise dominate the log buffer.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)

		path := r.URL.Path
		if path == "/healthz" || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/partials/") {
			return
		}
		log.Info("request",
			"method", sanitizeLogValue(r.Method),
			"path", sanitizeLogValue(path),
			"duration", time.Since(started).String())
	})
}

// sanitizeLogValue strips control characters so a crafted request path cannot
// forge log lines.
var logSanitizer = strings.NewReplacer("\n", "", "\r", "", "\t", " ")

func sanitizeLogValue(s string) string {
	return logSanitizer.Replace(s)
}
