package server

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/version"
	"github.com/daknoblo/CFTM/internal/web"
)

// Budgets for the manually triggered rounds, which outlive the request.
const (
	refreshTimeout = 2 * time.Minute
	probeTimeout   = 5 * time.Minute
)

// manualInterval is the shortest spacing between two manually triggered rounds.
// Neither endpoint is authenticated, and one collection cycle costs a Cloudflare
// request per tunnel out of a quota shared with every other consumer.
const manualInterval = 15 * time.Second

// throttle spaces out the rounds a client can trigger by hand.
type throttle struct {
	mu   sync.Mutex
	last time.Time
}

// reserve claims the next slot, or reports how long the caller has to wait.
func (t *throttle) reserve(now time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := manualInterval - now.Sub(t.last); !t.last.IsZero() && wait > 0 {
		return wait, false
	}
	t.last = now
	return 0, true
}

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
	if wait, ok := s.refreshLimit.reserve(time.Now()); !ok {
		tooSoon(w, wait)
		return
	}

	ctx, cancel := detach(r.Context(), refreshTimeout)
	defer cancel()
	s.collector.PollOnce(ctx)

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
	if wait, ok := s.probeLimit.reserve(time.Now()); !ok {
		tooSoon(w, wait)
		return
	}

	ctx, cancel := detach(r.Context(), probeTimeout)
	defer cancel()
	if err := s.collector.ProbeOnce(ctx, s.prober); err != nil {
		s.serverError(w, r, "running probes", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tooSoon(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	http.Error(w, "a round was triggered moments ago", http.StatusTooManyRequests)
}

// detach keeps a manually triggered round running when the browser navigates
// away, so an aborted request cannot record a spurious failure half way through.
func detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
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
