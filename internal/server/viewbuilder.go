package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/web"
)

// heartbeatBuckets splits the last 24 hours into half-hour segments.
const heartbeatBuckets = 48

// uptimeWindows are the availability figures shown on a tunnel page.
var uptimeWindows = []struct {
	Label  string
	Window time.Duration
}{
	{"24 hours", 24 * time.Hour},
	{"7 days", 7 * 24 * time.Hour},
	{"30 days", 30 * 24 * time.Hour},
}

// snapshot is everything the read model needs, loaded once per request.
type snapshot struct {
	now            time.Time
	tunnels        []store.Tunnel
	connectors     map[string][]store.Connector
	connections    map[string][]store.Connection
	ingress        map[string][]store.IngressRule
	apps           []store.AccessApp
	tokens         []store.ServiceToken
	probes         map[string]store.ProbeResult
	latestRelease  string
	auditAvailable bool
	alertCoverage  collector.AlertCoverage

	// ignored are the findings the operator muted in the UI.
	ignored map[store.FindingRef]bool
	// expectedPublic merges the configured exceptions with the hostnames whose
	// coverage finding was muted, so both routes reach the same conclusion.
	expectedPublic map[string]bool
}

func (s *Server) loadSnapshot(ctx context.Context) (snapshot, error) {
	now := time.Now()
	snap := snapshot{
		now:         now,
		connectors:  map[string][]store.Connector{},
		connections: map[string][]store.Connection{},
		ingress:     map[string][]store.IngressRule{},
	}

	var err error
	if snap.tunnels, err = s.store.Tunnels(ctx); err != nil {
		return snap, fmt.Errorf("load tunnels: %w", err)
	}

	connectors, err := s.store.Connectors(ctx, "")
	if err != nil {
		return snap, fmt.Errorf("load connectors: %w", err)
	}
	for _, c := range connectors {
		snap.connectors[c.TunnelID] = append(snap.connectors[c.TunnelID], c)
	}

	connections, err := s.store.Connections(ctx, "")
	if err != nil {
		return snap, fmt.Errorf("load connections: %w", err)
	}
	for _, c := range connections {
		snap.connections[c.TunnelID] = append(snap.connections[c.TunnelID], c)
	}

	rules, err := s.store.Ingress(ctx, "")
	if err != nil {
		return snap, fmt.Errorf("load ingress: %w", err)
	}
	for _, r := range rules {
		snap.ingress[r.TunnelID] = append(snap.ingress[r.TunnelID], r)
	}

	if snap.apps, err = s.store.AccessApps(ctx); err != nil {
		return snap, fmt.Errorf("load access apps: %w", err)
	}
	if snap.tokens, err = s.store.ServiceTokens(ctx); err != nil {
		return snap, fmt.Errorf("load service tokens: %w", err)
	}
	if snap.probes, err = s.store.LatestProbeResults(ctx); err != nil {
		return snap, fmt.Errorf("load probe results: %w", err)
	}
	if snap.ignored, err = s.store.IgnoredFindingSet(ctx); err != nil {
		return snap, fmt.Errorf("load ignored findings: %w", err)
	}
	snap.expectedPublic = s.effectivePublic(snap.ignored)

	snap.latestRelease = s.collector.LatestRelease(ctx)
	snap.auditAvailable = !s.collector.AccessAuditAt(ctx).IsZero()
	snap.alertCoverage = s.collector.TunnelAlerts(ctx)

	return snap, nil
}

// effectivePublic treats a muted coverage finding like a configured exception:
// the hostname stays monitored but stops counting as a gap.
func (s *Server) effectivePublic(ignored map[store.FindingRef]bool) map[string]bool {
	out := make(map[string]bool, len(s.cfg.ExpectedPublic)+len(ignored))
	for host := range s.cfg.ExpectedPublic {
		out[host] = true
	}
	for ref := range ignored {
		if ref.Code == collector.FindingAccessUnprotected && ref.Hostname != "" {
			out[ref.Hostname] = true
		}
	}
	return out
}

// findingsFor evaluates the derived health signals of one tunnel.
func (s *Server) findingsFor(ctx context.Context, snap snapshot, t store.Tunnel) []collector.Finding {
	recent, err := s.store.StatusHistory(ctx, t.ID, snap.now.Add(-collector.FlapWindow))
	if err != nil {
		s.log.Warn("reading status history failed", "tunnel", t.ID, "err", err)
	}

	return collector.Evaluate(collector.HealthInput{
		Tunnel:               t,
		Connectors:           snap.connectors[t.ID],
		Connections:          snap.connections[t.ID],
		Ingress:              snap.ingress[t.ID],
		AccessApps:           snap.apps,
		ServiceTokens:        snap.tokens,
		AccessAuditAvailable: snap.auditAvailable,
		ExpectedPublic:       snap.expectedPublic,
		AlertCoverage:        snap.alertCoverage,
		LatestRelease:        snap.latestRelease,
		RecentChanges:        len(recent),
		Now:                  snap.now,
	})
}

// buildCard renders one tunnel with the muted findings left out.
func (s *Server) buildCard(ctx context.Context, snap snapshot, t store.Tunnel) web.TunnelCard {
	visible := visibleFindings(s.findingsFor(ctx, snap, t), t.ID, snap.ignored)

	uptime, err := s.store.UptimeFor(ctx, t.ID, 24*time.Hour, snap.now)
	if err != nil {
		s.log.Warn("computing uptime failed", "tunnel", t.ID, "err", err)
	}

	return web.TunnelCard{
		ID:           t.ID,
		Name:         t.Name,
		Status:       t.Status,
		Severity:     string(collector.WorstSeverity(visible)),
		Connectors:   len(snap.connectors[t.ID]),
		Connections:  len(snap.connections[t.ID]),
		Colos:        colosOf(snap.connections[t.ID]),
		Versions:     versionsOf(snap.connectors[t.ID]),
		IngressCount: countHostnames(snap.ingress[t.ID]),
		Probes:       probeSummary(snap, snap.ingress[t.ID]),
		Findings:     toWebFindings(visible),
		Uptime24h:    toWebUptime("24 hours", 24*time.Hour, uptime),
		Heartbeats:   s.heartbeats(ctx, t.ID, snap.now),
		LastSeen:     t.LastSeen,
		ConnectedFor: web.FormatDuration(t.ConnsActiveAt, snap.now),
	}
}

// probeSummary counts the latest probe outcome across a tunnel's probeable
// hostnames. Ones that were never checked count towards Total but not Checked,
// so a partly probed tunnel cannot look like a failing one.
func probeSummary(snap snapshot, rules []store.IngressRule) web.Probes {
	seen := map[string]bool{}
	var out web.Probes
	for _, r := range rules {
		if r.Hostname == "" || !isHTTPService(r.Service) || seen[r.Hostname] {
			continue
		}
		seen[r.Hostname] = true
		out.Total++

		probe, ok := snap.probes[r.Hostname]
		if !ok {
			continue
		}
		out.Checked++
		if probe.Class == prober.ClassOK {
			out.OK++
			continue
		}
		out.Worst = worseProbeClass(out.Worst, probe.Class)
	}
	return out
}

// probeClassRank orders probe classes so the most serious one wins.
func probeClassRank(class string) int {
	switch class {
	case prober.ClassTunnelDown:
		return 6
	case prober.ClassOriginError:
		return 5
	case prober.ClassTimeout, prober.ClassDNSError, prober.ClassTLSError:
		return 4
	case prober.ClassHTTPError:
		return 3
	case prober.ClassAccessDenied:
		return 2
	case prober.ClassAccessChallenge:
		return 1
	default:
		return 0
	}
}

func worseProbeClass(a, b string) string {
	if probeClassRank(b) > probeClassRank(a) {
		return b
	}
	return a
}

// visibleFindings drops the findings the operator muted.
func visibleFindings(findings []collector.Finding, tunnelID string, ignored map[store.FindingRef]bool) []collector.Finding {
	if len(ignored) == 0 {
		return findings
	}
	out := make([]collector.Finding, 0, len(findings))
	for _, f := range findings {
		ref := store.FindingRef{
			Code:     f.Code,
			TunnelID: tunnelID,
			Hostname: strings.ToLower(f.Hostname),
		}
		if !ignored[ref] {
			out = append(out, f)
		}
	}
	return out
}

// mutedFor lists what is muted for one tunnel. It reads the stored rows rather
// than the live evaluation, so a finding whose cause has gone away can still be
// un-muted.
func (s *Server) mutedFor(ctx context.Context, tunnelID string) []web.Finding {
	ignored, err := s.store.IgnoredFindings(ctx)
	if err != nil {
		s.log.Warn("reading muted findings failed", "tunnel", tunnelID, "err", err)
		return nil
	}

	var out []web.Finding
	for _, f := range ignored {
		if f.TunnelID != tunnelID {
			continue
		}
		out = append(out, web.Finding{
			Code:     f.Code,
			Severity: string(collector.SeverityInfo),
			Message:  f.Message,
			Hostname: f.Hostname,
			MutedAt:  f.CreatedAt,
		})
	}
	return out
}

// Dashboard assembles the index page model.
func (s *Server) Dashboard(ctx context.Context) (web.Dashboard, error) {
	snap, err := s.loadSnapshot(ctx)
	if err != nil {
		return web.Dashboard{}, err
	}

	d := web.Dashboard{Poll: s.pollStatus()}
	for _, t := range snap.tunnels {
		card := s.buildCard(ctx, snap, t)
		d.Tunnels = append(d.Tunnels, card)

		d.Totals.Tunnels++
		if t.Status == "healthy" {
			d.Totals.Healthy++
		}
		d.Totals.Connectors += card.Connectors
		d.Totals.Connections += card.Connections
		d.Totals.Hostnames += card.IngressCount
		d.Totals.Findings += len(card.Findings)
	}

	for _, rule := range s.unprotectedOnly(snap) {
		_ = rule
		d.Totals.Unprotected++
	}

	return d, nil
}

// unprotectedOnly is the genuine-gap half of unprotected.
func (s *Server) unprotectedOnly(snap snapshot) []store.IngressRule {
	unexpected, _ := s.unprotected(snap)
	return unexpected
}

// TunnelDetail assembles the model of a single tunnel page.
func (s *Server) TunnelDetail(ctx context.Context, id string) (web.TunnelDetail, bool, error) {
	snap, err := s.loadSnapshot(ctx)
	if err != nil {
		return web.TunnelDetail{}, false, err
	}

	var tunnel store.Tunnel
	found := false
	for _, t := range snap.tunnels {
		if t.ID == id {
			tunnel, found = t, true
			break
		}
	}
	if !found {
		return web.TunnelDetail{}, false, nil
	}

	card := s.buildCard(ctx, snap, tunnel)
	detail := web.TunnelDetail{
		Card:       card,
		Ignored:    s.mutedFor(ctx, id),
		TunType:    tunnel.TunType,
		ConfigSrc:  tunnel.ConfigSrc,
		Remote:     tunnel.RemoteConfig,
		CreatedAt:  tunnel.CreatedAt,
		ActiveAt:   tunnel.ConnsActiveAt,
		InactiveAt: tunnel.ConnsInactiveAt,
	}

	for _, c := range snap.connectors[id] {
		detail.Connectors = append(detail.Connectors, web.Connector{
			ID:            c.ID,
			ShortID:       shortID(c.ID),
			Version:       c.Version,
			Arch:          c.Arch,
			ConfigVersion: c.ConfigVersion,
			Features:      c.Features,
			RunAt:         c.RunAt,
			Outdated:      snap.latestRelease != "" && c.Version != "" && c.Version != snap.latestRelease,
		})
	}

	for _, c := range snap.connections[id] {
		detail.Connections = append(detail.Connections, web.Connection{
			UUID:     c.UUID,
			Colo:     c.ColoName,
			OriginIP: c.OriginIP,
			OpenedAt: c.OpenedAt,
		})
	}

	names := tunnelNames(snap.tunnels)
	detail.Ingress = s.toWebIngress(snap, snap.ingress[id], names)
	if rules := snap.ingress[id]; len(rules) > 0 {
		detail.ConfigVer = rules[0].ConfigVersion
	}

	for _, w := range uptimeWindows {
		up, err := s.store.UptimeFor(ctx, id, w.Window, snap.now)
		if err != nil {
			s.log.Warn("computing uptime failed", "tunnel", id, "window", w.Label, "err", err)
			continue
		}
		detail.Uptimes = append(detail.Uptimes, toWebUptime(w.Label, w.Window, up))
	}

	events, err := s.store.Events(ctx, 200)
	if err != nil {
		return detail, true, fmt.Errorf("load events: %w", err)
	}
	for _, e := range events {
		if e.TunnelID != id {
			continue
		}
		detail.Events = append(detail.Events, toWebEvent(e, names))
		if len(detail.Events) >= 25 {
			break
		}
	}

	return detail, true, nil
}

// IngressPage assembles the ingress inventory model.
func (s *Server) IngressPage(ctx context.Context) (web.IngressPage, error) {
	snap, err := s.loadSnapshot(ctx)
	if err != nil {
		return web.IngressPage{}, err
	}

	names := tunnelNames(snap.tunnels)
	page := web.IngressPage{ProbeEnabled: s.cfg.ProbeEnabled, ProbeToken: s.cfg.ProbeToken}
	for _, t := range snap.tunnels {
		page.Rules = append(page.Rules, s.toWebIngress(snap, snap.ingress[t.ID], names)...)
	}
	return page, nil
}

// AuditPage assembles the Access audit model.
func (s *Server) AuditPage(ctx context.Context) (web.AuditPage, error) {
	snap, err := s.loadSnapshot(ctx)
	if err != nil {
		return web.AuditPage{}, err
	}

	names := tunnelNames(snap.tunnels)
	unexpected, expected := s.unprotected(snap)
	page := web.AuditPage{
		AuditedAt:            s.collector.AccessAuditAt(ctx),
		Available:            snap.auditAvailable,
		Unprotected:          s.toWebIngress(snap, unexpected, names),
		IntentionallyPublic:  s.toWebIngress(snap, expected, names),
		ProbeTokenConfigured: s.cfg.ProbeToken,
		LoginsEnabled:        s.cfg.AccessLoginsEnabled,
		LoginsAt:             s.collector.AccessLoginsAt(ctx),
	}

	if page.LoginsEnabled {
		logins, err := s.store.AccessLogins(ctx)
		if err != nil {
			return page, fmt.Errorf("load access logins: %w", err)
		}
		for _, l := range logins {
			page.Logins = append(page.Logins, web.AccessLogin{
				AppDomain: l.AppDomain,
				Allowed:   l.Allowed,
				Denied:    l.Denied,
				Users:     l.Users,
				LastAt:    l.LastAt,
			})
			page.LoginTotals.Allowed += l.Allowed
			page.LoginTotals.Denied += l.Denied
		}
		page.LoginTotals.Apps = len(page.Logins)
	}

	hostnames := map[string]bool{}
	for _, rules := range snap.ingress {
		for _, r := range rules {
			if r.Hostname != "" {
				hostnames[strings.ToLower(r.Hostname)] = true
			}
		}
	}

	for _, app := range snap.apps {
		matched := false
		for _, d := range app.Domains {
			if hostnames[strings.ToLower(d)] {
				matched = true
				break
			}
		}
		page.Apps = append(page.Apps, web.AccessApp{
			Name:        app.Name,
			Domains:     app.Domains,
			Type:        app.Type,
			PolicyCount: app.PolicyCount,
			HasBypass:   app.HasBypass,
			HasToken:    app.HasToken,
			Matched:     matched,
		})
	}

	for _, token := range snap.tokens {
		item := web.ServiceToken{
			Name:      token.Name,
			ClientID:  token.ClientID,
			ExpiresAt: token.ExpiresAt,
			InUse:     s.cfg.ProbeClientID != "" && token.ClientID == s.cfg.ProbeClientID,
		}
		if item.InUse {
			page.ProbeTokenKnown = true
		}
		if !token.ExpiresAt.IsZero() {
			remaining := token.ExpiresAt.Sub(snap.now)
			item.DaysLeft = int(remaining.Hours() / 24)
			switch {
			case remaining <= 0:
				item.Severity = string(collector.SeverityCritical)
			case remaining <= collector.TokenExpiryWarning:
				item.Severity = string(collector.SeverityWarning)
			}
		}
		page.Tokens = append(page.Tokens, item)
	}

	return page, nil
}

// NotificationsPage assembles the notification outbox model.
func (s *Server) NotificationsPage(ctx context.Context) (web.NotificationsPageView, error) {
	items, err := s.store.Notifications(ctx, 200)
	if err != nil {
		return web.NotificationsPageView{}, fmt.Errorf("load notifications: %w", err)
	}

	page := web.NotificationsPageView{
		Enabled:     s.cfg.NotifyEnabled,
		Transport:   s.cfg.NotifyTransport,
		MinSeverity: s.cfg.NotifySeverity,
		Cooldown:    s.cfg.NotifyCooldown.String(),
	}
	for _, n := range items {
		page.Items = append(page.Items, web.NotificationItem{
			CreatedAt: n.CreatedAt,
			Severity:  n.Severity,
			Title:     n.Title,
			Body:      n.Body,
			Transport: n.Transport,
			Status:    n.Status,
			Error:     n.Error,
		})
	}
	return page, nil
}

// Events assembles the event log model.
func (s *Server) Events(ctx context.Context, limit int) ([]web.Event, error) {
	tunnels, err := s.store.Tunnels(ctx)
	if err != nil {
		return nil, fmt.Errorf("load tunnels: %w", err)
	}
	events, err := s.store.Events(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}

	names := tunnelNames(tunnels)
	out := make([]web.Event, 0, len(events))
	for _, e := range events {
		out = append(out, toWebEvent(e, names))
	}
	return out, nil
}

// unprotected splits the hostnames without an Access application into the ones
// that are public on purpose and the ones that are a genuine gap.
func (s *Server) unprotected(snap snapshot) (unexpected, expected []store.IngressRule) {
	if !snap.auditAvailable {
		return nil, nil
	}
	protected := map[string]bool{}
	for _, app := range snap.apps {
		for _, d := range app.Domains {
			protected[strings.ToLower(d)] = true
		}
	}

	for _, rules := range snap.ingress {
		for _, r := range rules {
			if r.Hostname == "" || !isHTTPService(r.Service) {
				continue
			}
			host := strings.ToLower(r.Hostname)
			if protected[host] {
				continue
			}
			if snap.expectedPublic[host] {
				expected = append(expected, r)
				continue
			}
			unexpected = append(unexpected, r)
		}
	}
	sort.Slice(unexpected, func(i, j int) bool { return unexpected[i].Hostname < unexpected[j].Hostname })
	sort.Slice(expected, func(i, j int) bool { return expected[i].Hostname < expected[j].Hostname })
	return unexpected, expected
}

func (s *Server) toWebIngress(snap snapshot, rules []store.IngressRule, names map[string]string) []web.Ingress {
	appByDomain := map[string]store.AccessApp{}
	for _, app := range snap.apps {
		for _, d := range app.Domains {
			appByDomain[strings.ToLower(d)] = app
		}
	}

	out := make([]web.Ingress, 0, len(rules))
	for _, r := range rules {
		item := web.Ingress{
			TunnelID:    r.TunnelID,
			TunnelName:  names[r.TunnelID],
			Hostname:    r.Hostname,
			Path:        r.Path,
			Service:     r.Service,
			Kind:        serviceKind(r.Service),
			Probeable:   r.Hostname != "" && isHTTPService(r.Service),
			NoTLSVerify: strings.Contains(r.OriginRequest, `"noTLSVerify":true`),
		}

		if item.Probeable {
			app, ok := appByDomain[strings.ToLower(r.Hostname)]
			item.Access = web.AccessState{
				Known:     snap.auditAvailable,
				Protected: ok,
				AppName:   app.Name,
				HasBypass: app.HasBypass,
				HasToken:  app.HasToken,
			}
			if probe, ok := snap.probes[r.Hostname]; ok {
				item.Probe = web.Probe{
					Known:      true,
					Class:      probe.Class,
					StatusCode: probe.StatusCode,
					LatencyMS:  probe.LatencyMS,
					Error:      probe.Error,
					CheckedAt:  probe.CheckedAt,
				}
			}
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) heartbeats(ctx context.Context, tunnelID string, now time.Time) []web.Heartbeat {
	from := now.Add(-24 * time.Hour)

	initial, _, err := s.store.StatusAt(ctx, tunnelID, from)
	if err != nil {
		s.log.Warn("reading status at window start failed", "tunnel", tunnelID, "err", err)
	}
	changes, err := s.store.StatusHistory(ctx, tunnelID, from)
	if err != nil {
		s.log.Warn("reading status history failed", "tunnel", tunnelID, "err", err)
	}

	return buildHeartbeats(initial, changes, from, now, heartbeatBuckets)
}

// buildHeartbeats renders the worst status seen in each bucket, so a short
// outage stays visible instead of being averaged away.
func buildHeartbeats(initial string, changes []store.StatusChange, from, to time.Time, buckets int) []web.Heartbeat {
	if buckets <= 0 || !to.After(from) {
		return nil
	}

	width := to.Sub(from) / time.Duration(buckets)
	out := make([]web.Heartbeat, 0, buckets)
	current := initial
	next := 0

	for i := range buckets {
		start := from.Add(time.Duration(i) * width)
		end := start.Add(width)

		worst := current
		for next < len(changes) && changes[next].ChangedAt.Before(end) {
			if !changes[next].ChangedAt.Before(start) {
				worst = worseStatus(worst, changes[next].Status)
			}
			current = changes[next].Status
			next++
		}

		label := start.UTC().Format("15:04") + " UTC"
		if worst != "" {
			label += " · " + worst
		} else {
			label += " · no data"
		}
		out = append(out, web.Heartbeat{Status: worst, Label: label})
	}
	return out
}

// statusRank orders statuses so the worst one wins inside a bucket.
func statusRank(status string) int {
	switch status {
	case "down":
		return 4
	case "degraded":
		return 3
	case "inactive":
		return 2
	case "healthy":
		return 1
	default:
		return 0
	}
}

func worseStatus(a, b string) string {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

func toWebFindings(findings []collector.Finding) []web.Finding {
	out := make([]web.Finding, 0, len(findings))
	for _, f := range findings {
		out = append(out, web.Finding{
			Code:     f.Code,
			Severity: string(f.Severity),
			Message:  f.Message,
			Hostname: f.Hostname,
		})
	}
	return out
}

func toWebUptime(label string, nominal time.Duration, u store.Uptime) web.Uptime {
	return web.Uptime{
		Label:    label,
		Percent:  u.Percent(),
		Observed: u.Observed,
		Window:   u.Window,
		Nominal:  nominal,
	}
}

func toWebEvent(e store.Event, names map[string]string) web.Event {
	return web.Event{
		Timestamp:  e.Timestamp,
		Kind:       e.Kind,
		KindLabel:  eventLabel(e.Kind),
		TunnelID:   e.TunnelID,
		TunnelName: names[e.TunnelID],
		Hostname:   e.Hostname,
		From:       e.FromState,
		To:         e.ToState,
		Message:    e.Message,
		Severity:   eventSeverity(e),
	}
}

func eventLabel(kind string) string {
	return collector.EventLabel(kind)
}

func eventSeverity(e store.Event) string {
	return string(collector.EventSeverity(e))
}

func tunnelNames(tunnels []store.Tunnel) map[string]string {
	out := make(map[string]string, len(tunnels))
	for _, t := range tunnels {
		out[t.ID] = t.Name
	}
	return out
}

func colosOf(conns []store.Connection) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range conns {
		if c.ColoName == "" || seen[c.ColoName] {
			continue
		}
		seen[c.ColoName] = true
		out = append(out, c.ColoName)
	}
	sort.Strings(out)
	return out
}

func versionsOf(connectors []store.Connector) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range connectors {
		if c.Version == "" || seen[c.Version] {
			continue
		}
		seen[c.Version] = true
		out = append(out, c.Version)
	}
	sort.Strings(out)
	return out
}

// countHostnames counts routable rules, ignoring the trailing catch-all.
func countHostnames(rules []store.IngressRule) int {
	n := 0
	for _, r := range rules {
		if r.Hostname != "" {
			n++
		}
	}
	return n
}

func serviceKind(service string) string {
	switch {
	case strings.HasPrefix(service, "http://"), strings.HasPrefix(service, "https://"):
		return "http"
	case strings.HasPrefix(service, "ssh://"):
		return "ssh"
	case strings.HasPrefix(service, "rdp://"):
		return "rdp"
	case strings.HasPrefix(service, "tcp://"):
		return "tcp"
	case strings.HasPrefix(service, "http_status:"):
		return "catch-all"
	default:
		return "other"
	}
}

func isHTTPService(service string) bool {
	return strings.HasPrefix(service, "http://") || strings.HasPrefix(service, "https://")
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
