package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/store"
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
	builtAt, _ := time.Parse(time.RFC3339, info.Date)
	page := web.AboutPage{
		Version:      info.Version,
		Commit:       info.Commit,
		BuiltAt:      builtAt,
		GoVersion:    info.GoVer,
		AccountID:    s.cfg.AccountID,
		PollInterval: s.cfg.PollInterval,
		Retention:    s.cfg.RetentionDays,
		Features:     s.features(),
	}
	s.render(w, r, web.AboutPageView(s.layout(r, "About"), page))
}

// features explains which optional capabilities are running, and for the ones
// that are not, whether that is a choice or a missing credential.
func (s *Server) features() []web.FeatureState {
	probe := web.FeatureState{Name: "End-to-end probing", State: "off", Detail: "CFTM_PROBE_ENABLED is false"}
	switch {
	case s.cfg.ProbeEnabled && s.cfg.ProbeToken:
		probe.State = "on"
		probe.Detail = "Requests carry the Access service token " + s.cfg.ProbeClientID
	case s.cfg.ProbeEnabled:
		probe.State, probe.Detail = "unconfigured", "No Access service token, so guarded hostnames only reach the edge"
	}

	audit := web.FeatureState{Name: "Access audit", State: "off", Detail: "CFTM_ACCESS_AUDIT_ENABLED is false"}
	if s.cfg.AuditEnabled {
		audit.State, audit.Detail = "on", "Access applications and service tokens are inventoried"
	}

	release := web.FeatureState{Name: "cloudflared version check", State: "off", Detail: "CFTM_RELEASE_CHECK_ENABLED is false"}
	if s.cfg.ReleaseCheck {
		release.State, release.Detail = "on", "Compares connectors against the latest GitHub release"
	}

	public := web.FeatureState{Name: "Declared public hostnames", State: "off", Detail: "CFTM_EXPECTED_PUBLIC is empty"}
	if n := len(s.cfg.ExpectedPublic); n > 0 {
		public.State = "on"
		public.Detail = fmt.Sprintf("%d hostname(s) exempt from the coverage check", n)
	}

	return []web.FeatureState{
		{Name: "Tunnel monitoring", State: "on", Detail: "Always on; uptime comes from the Cloudflare tunnel status"},
		audit,
		probe,
		release,
		public,
	}
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

// handleIgnoreFinding mutes one finding and re-renders the tunnel page.
func (s *Server) handleIgnoreFinding(w http.ResponseWriter, r *http.Request) {
	ref, ok := findingRef(r)
	if !ok {
		http.Error(w, "a finding code is required", http.StatusBadRequest)
		return
	}
	if err := s.store.IgnoreFinding(r.Context(), ref, r.FormValue("message"), time.Now()); err != nil {
		s.serverError(w, r, "muting finding", err)
		return
	}
	s.redirectToTunnel(w, r, ref.TunnelID)
}

// handleRestoreFinding un-mutes one finding and re-renders the tunnel page.
func (s *Server) handleRestoreFinding(w http.ResponseWriter, r *http.Request) {
	ref, ok := findingRef(r)
	if !ok {
		http.Error(w, "a finding code is required", http.StatusBadRequest)
		return
	}
	if err := s.store.RestoreFinding(r.Context(), ref); err != nil {
		s.serverError(w, r, "restoring finding", err)
		return
	}
	s.redirectToTunnel(w, r, ref.TunnelID)
}

// findingRef reads a finding identity from the submitted form.
func findingRef(r *http.Request) (store.FindingRef, bool) {
	ref := store.FindingRef{
		Code:     r.FormValue("code"),
		TunnelID: r.FormValue("tunnel"),
		Hostname: r.FormValue("hostname"),
	}
	return ref, ref.Valid()
}

// redirectToTunnel sends the browser back to the page the form was posted from.
func (s *Server) redirectToTunnel(w http.ResponseWriter, r *http.Request, tunnelID string) {
	target := "/"
	if tunnelID != "" {
		target = "/tunnels/" + url.PathEscape(tunnelID)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
