package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/version"
	"github.com/daknoblo/CFTM/internal/web"
)

// Budgets for the manually triggered rounds, which outlive the request.
const (
	refreshTimeout    = 2 * time.Minute
	refreshAllTimeout = 5 * time.Minute
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

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	page, err := s.NotificationsPage(r.Context())
	if err != nil {
		s.serverError(w, r, "building the notification outbox", err)
		return
	}
	s.render(w, r, web.NotificationsPage(s.layout(r, "Notifications"), page))
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
		Permissions:  s.permissions(r.Context()),
	}
	s.render(w, r, web.AboutPageView(s.layout(r, "About"), page))
}

// permissionAreas describes every part of the Cloudflare API CFTM reads, in the
// order they matter.
var permissionAreas = []struct {
	Key      string
	Name     string
	Optional bool
	Lost     string
}{
	{collector.CapTunnels, "Tunnels, connectors and ingress", false,
		"Nothing works without this"},
	{collector.CapAccessApps, "Access applications and policies", true,
		"The audit page cannot tell which hostnames are protected"},
	{collector.CapServiceTokens, "Access service tokens", true,
		"Expiring service tokens go unnoticed"},
	{collector.CapNotifications, "Notification policies", true,
		"CFTM cannot tell whether Cloudflare would alert you about a tunnel"},
	{collector.CapAccessLogins, "Access authentication log", true,
		"No login or denial figures per application"},
	{collector.CapZones, "Zone list", true,
		"Request origins cannot be mapped onto a zone"},
	{collector.CapZoneAnalytics, "Zone traffic analytics", true,
		"No origin countries for traffic an Access bypass policy waves through"},
	{collector.CapLoginAnalytics, "Access login analytics", true,
		"No origin countries for login attempts"},
}

// permissions reports what the token was actually allowed to read. The states
// come from the responses the API gave, so a missing permission is observed
// rather than guessed.
func (s *Server) permissions(ctx context.Context) []web.Permission {
	known := s.collector.Capabilities(ctx)

	out := make([]web.Permission, 0, len(permissionAreas))
	for _, area := range permissionAreas {
		p := web.Permission{
			Name:     area.Name,
			State:    "unknown",
			Required: collector.RequiredPermission(area.Key),
			Optional: area.Optional,
			Detail:   "Not called yet",
		}

		// "You switched it off" and "the token said no" look the same in the
		// cache, so the configuration decides first.
		if reason, off := s.switchedOff(area.Key); off {
			p.State, p.Detail = collector.CapDisabled, reason
			out = append(out, p)
			continue
		}

		if capability, ok := known[area.Key]; ok {
			p.State = capability.State
			p.CheckedAt = capability.CheckedAt
			switch capability.State {
			case collector.CapOK:
				p.Detail = "Readable"
			case collector.CapForbidden:
				p.Detail = area.Lost
			default:
				p.Detail = capability.Detail
			}
		}
		out = append(out, p)
	}
	return out
}

// switchedOff reports areas CFTM was told not to call, so a missing permission
// is not blamed for a deliberate choice.
func (s *Server) switchedOff(key string) (string, bool) {
	switch key {
	case collector.CapAccessApps, collector.CapServiceTokens:
		if !s.cfg.AuditEnabled {
			return "CFTM_ACCESS_AUDIT_ENABLED is false", true
		}
	case collector.CapAccessLogins:
		if !s.cfg.AccessLoginsEnabled {
			return "CFTM_ACCESS_LOGINS_ENABLED is false", true
		}
	case collector.CapZones, collector.CapZoneAnalytics, collector.CapLoginAnalytics:
		if !s.cfg.OriginsEnabled {
			return "CFTM_ORIGINS_ENABLED is false", true
		}
	}
	return "", false
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

	notifications := web.FeatureState{Name: "Notifications", State: "off", Detail: "CFTM_NOTIFY_ENABLED is false"}
	if s.cfg.NotifyEnabled {
		notifications.State = "unconfigured"
		notifications.Detail = fmt.Sprintf("Recording %s and above to the outbox; no delivery channel is wired up yet", s.cfg.NotifySeverity)
		if s.cfg.NotifyTransport != "" && s.cfg.NotifyTransport != "none" {
			notifications.State = "on"
			notifications.Detail = fmt.Sprintf("Delivering %s and above via %s", s.cfg.NotifySeverity, s.cfg.NotifyTransport)
		}
	}

	logins := web.FeatureState{Name: "Access authentication log", State: "off", Detail: "CFTM_ACCESS_LOGINS_ENABLED is false"}
	if s.cfg.AccessLoginsEnabled {
		logins.State, logins.Detail = "on", "Login and denial figures per application on the audit page"
	}

	origins := web.FeatureState{Name: "Request origins", State: "off", Detail: "CFTM_ORIGINS_ENABLED is false"}
	if s.cfg.OriginsEnabled {
		origins.State, origins.Detail = "on", "Requests summarized by country on the dashboard and the audit page"
	}

	return []web.FeatureState{
		{Name: "Tunnel monitoring", State: "on", Detail: "Always on; uptime comes from the Cloudflare tunnel status"},
		audit,
		probe,
		release,
		public,
		logins,
		origins,
		notifications,
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
	// The Access inventory has its own hourly loop. Without this, a policy the
	// operator just fixed keeps raising findings with no way to force a recheck.
	if s.cfg.AuditEnabled {
		if err := s.collector.AuditAccess(ctx); err != nil {
			s.log.Warn("access audit during refresh failed", "err", err)
		}
	}

	d, err := s.Dashboard(r.Context())
	if err != nil {
		s.serverError(w, r, "building tunnel cards", err)
		return
	}
	s.render(w, r, web.TunnelCards(d.Tunnels))
}

// handleRefreshAll runs every collection the daemon runs at start-up, so the
// button is equivalent to restarting the container, then reloads the page.
func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	if wait, ok := s.refreshAllLimit.reserve(time.Now()); !ok {
		tooSoon(w, wait)
		return
	}

	ctx, cancel := detach(r.Context(), refreshAllTimeout)
	defer cancel()
	s.refreshAll(ctx)

	// htmx reloads the document, so every panel shows the new data at once.
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

// refreshAll mirrors start-up: each step is independent, so one unavailable
// area must not stop the rest.
func (s *Server) refreshAll(ctx context.Context) {
	s.collector.PollOnce(ctx)

	if s.cfg.AuditEnabled {
		if err := s.collector.AuditAccess(ctx); err != nil {
			s.log.Warn("access audit during refresh failed", "err", err)
		}
	}
	if s.cfg.AccessLoginsEnabled {
		if err := s.collector.CollectAccessLogins(ctx, s.cfg.AccessLoginsWindow, time.Now()); err != nil {
			s.log.Warn("access logins during refresh failed", "err", err)
		}
	}
	if s.release != nil {
		if err := s.collector.CheckRelease(ctx, s.release); err != nil {
			s.log.Warn("release check during refresh failed", "err", err)
		}
	}
	if s.prober != nil {
		if err := s.collector.ProbeOnce(ctx, s.prober); err != nil {
			s.log.Warn("probe round during refresh failed", "err", err)
		}
	}
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
