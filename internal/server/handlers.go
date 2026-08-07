package server

import (
	"net/http"

	"github.com/daknoblo/CFTM/internal/version"
	"github.com/daknoblo/CFTM/internal/web"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.Dashboard(r.Context())
	if err != nil {
		s.serverError(w, r, "building dashboard", err)
		return
	}
	s.render(w, r, web.DashboardPage(s.layout(r, "Tunnels"), d))
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	detail, found, err := s.TunnelDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		s.serverError(w, r, "building tunnel detail", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, web.TunnelPage(s.layout(r, detail.Card.Name), detail))
}

func (s *Server) handleIngress(w http.ResponseWriter, r *http.Request) {
	page, err := s.IngressPage(r.Context())
	if err != nil {
		s.serverError(w, r, "building ingress page", err)
		return
	}
	s.render(w, r, web.IngressPageView(s.layout(r, "Ingress"), page))
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	page, err := s.AuditPage(r.Context())
	if err != nil {
		s.serverError(w, r, "building audit page", err)
		return
	}
	s.render(w, r, web.AuditPageView(s.layout(r, "Access audit"), page))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Events(r.Context(), 200)
	if err != nil {
		s.serverError(w, r, "building event log", err)
		return
	}
	s.render(w, r, web.EventsPage(s.layout(r, "Events"), events))
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, web.LogsPage(s.layout(r, "Logs"), s.logPage()))
}

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	info := version.Get()
	page := web.AboutPage{
		Version:      info.Version,
		Commit:       info.Commit,
		Date:         info.Date,
		GoVersion:    info.GoVer,
		AccountID:    s.cfg.AccountID,
		PollInterval: s.cfg.PollInterval,
		ProbeEnabled: s.cfg.ProbeEnabled,
		ProbeToken:   s.cfg.ProbeToken,
		AuditEnabled: s.cfg.AuditEnabled,
		Retention:    s.cfg.RetentionDays,
	}
	s.render(w, r, web.AboutPageView(s.layout(r, "About"), page))
}

func (s *Server) handlePartialTunnels(w http.ResponseWriter, r *http.Request) {
	d, err := s.Dashboard(r.Context())
	if err != nil {
		s.serverError(w, r, "building tunnel cards", err)
		return
	}
	s.render(w, r, web.TunnelCards(d.Tunnels))
}

func (s *Server) handlePartialPollStatus(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, web.PollStatusBar(s.pollStatus()))
}

func (s *Server) handlePartialEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Events(r.Context(), 100)
	if err != nil {
		s.serverError(w, r, "building event log", err)
		return
	}
	s.render(w, r, web.EventList(events))
}

func (s *Server) handlePartialLog(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, web.LogLines(s.logPage()))
}

// handleRefresh forces a collection cycle and returns the refreshed cards.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.collector.PollOnce(r.Context())

	d, err := s.Dashboard(r.Context())
	if err != nil {
		s.serverError(w, r, "building tunnel cards", err)
		return
	}
	s.render(w, r, web.TunnelCards(d.Tunnels))
}

// handleProbe forces a probe round.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if s.prober == nil {
		http.Error(w, "probing is disabled", http.StatusNotFound)
		return
	}
	if err := s.collector.ProbeOnce(r.Context(), s.prober); err != nil {
		s.serverError(w, r, "running probes", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logPage() web.LogPage {
	entries := s.logs.Entries()
	page := web.LogPage{Lines: make([]web.LogLine, 0, len(entries))}
	// Newest first reads better in a fixed-height panel.
	for i := len(entries) - 1; i >= 0; i-- {
		page.Lines = append(page.Lines, web.LogLine{
			Time:    entries[i].Time,
			Level:   entries[i].Level,
			Message: entries[i].Message,
		})
	}
	return page
}
