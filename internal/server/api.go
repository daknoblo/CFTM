package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
)

// render writes a templ component, reporting failures without leaking details.
func (s *Server) render(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		// The status line is already sent, so only log the failure.
		s.log.Error("rendering failed", "path", sanitizeLogValue(r.URL.Path), "err", err)
	}
}

// serverError logs the cause and returns a generic message to the client.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.log.Error(what+" failed", "path", sanitizeLogValue(r.URL.Path), "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		s.log.Error("encoding response failed", "path", sanitizeLogValue(r.URL.Path), "err", err)
	}
}

func (s *Server) handleAPITunnels(w http.ResponseWriter, r *http.Request) {
	d, err := s.Dashboard(r.Context())
	if err != nil {
		s.serverError(w, r, "building dashboard", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"tunnels": d.Tunnels, "totals": d.Totals})
}

func (s *Server) handleAPITunnel(w http.ResponseWriter, r *http.Request) {
	detail, found, err := s.tunnelDetail(r.Context(), r.PathValue("id"), r.URL.Query().Get("hostname"))
	if errors.Is(err, errQualityHostname) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, r, "building tunnel detail", err)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, r, detail)
}

func (s *Server) handleAPIIngress(w http.ResponseWriter, r *http.Request) {
	page, err := s.IngressPage(r.Context())
	if err != nil {
		s.serverError(w, r, "building ingress page", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"rules": page.Rules})
}

func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Events(r.Context(), queryLimit(r, 100, 1000))
	if err != nil {
		s.serverError(w, r, "building event log", err)
		return
	}
	s.writeJSON(w, r, map[string]any{"events": events})
}

// handleAPIStatus reports collector health. It deliberately exposes no secrets.
func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, map[string]any{
		"poll":  s.pollStatus(),
		"audit": s.auditTimestamp(r.Context()),
	})
}

func (s *Server) auditTimestamp(ctx context.Context) string {
	at := s.collector.AccessAuditAt(ctx)
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format("2006-01-02T15:04:05Z07:00")
}

func queryLimit(r *http.Request, def, maxValue int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return min(n, maxValue)
}
