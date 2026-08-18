package collector

import (
	"fmt"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/store"
)

// Thresholds for the derived health signals.
const (
	// A healthy cloudflared keeps four QUIC connections to the edge.
	ExpectedConnections = 4
	// More transitions than this within the flap window means the tunnel is unstable.
	FlapThreshold = 4
	FlapWindow    = time.Hour
	// Service tokens expire after a year; warn while there is still time to rotate.
	TokenExpiryWarning = 30 * 24 * time.Hour
)

// Severity ranks a finding for display and sorting.
type Severity string

// Severity levels, ordered from least to most urgent.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Rank returns a sortable weight, highest severity first.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

// Finding codes.
const (
	FindingTunnelDown        = "tunnel_down"
	FindingTunnelDegraded    = "tunnel_degraded"
	FindingTunnelInactive    = "tunnel_inactive"
	FindingNoConnectors      = "no_connectors"
	FindingLowConnections    = "low_ha_connections"
	FindingSingleColo        = "single_colo"
	FindingVersionDrift      = "version_drift"
	FindingConfigDrift       = "config_drift"
	FindingFlapping          = "flapping"
	FindingNoTLSVerify       = "ingress_no_tls_verify"
	FindingLocalhostOrigin   = "ingress_localhost_origin"
	FindingAccessUnprotected = "access_unprotected"
	FindingAccessBypass      = "access_bypass"
	FindingTokenExpiring     = "service_token_expiring"
	FindingNoCloudflareAlert = "no_cloudflare_alert"
)

// Finding is one derived health observation about a tunnel.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Hostname string   `json:"hostname,omitempty"`
}

// HealthInput is everything Evaluate needs about a single tunnel.
type HealthInput struct {
	Tunnel        store.Tunnel
	Connectors    []store.Connector
	Connections   []store.Connection
	Ingress       []store.IngressRule
	AccessApps    []store.AccessApp
	ServiceTokens []store.ServiceToken
	// AccessAuditAvailable guards the coverage check: without a successful
	// audit an empty app list would mark every hostname as unprotected.
	AccessAuditAvailable bool
	// ExpectedPublic names hostnames that are published without Access on
	// purpose, so a permanent warning does not drown out a real gap.
	ExpectedPublic map[string]bool
	// AlertCoverage says whether Cloudflare would notify anyone about this
	// tunnel. It is only consulted when the token could read the policies.
	AlertCoverage AlertCoverage
	LatestRelease string
	RecentChanges int
	Now           time.Time
}

// Evaluate derives the health findings for one tunnel. It is pure so both the
// collector and the UI can call it on the same data.
func Evaluate(in HealthInput) []Finding {
	var findings []Finding

	switch in.Tunnel.Status {
	case cloudflare.StatusDown:
		findings = append(findings, Finding{
			Code: FindingTunnelDown, Severity: SeverityCritical,
			Message: "Tunnel has no connection to the Cloudflare edge",
		})
	case cloudflare.StatusDegraded:
		findings = append(findings, Finding{
			Code: FindingTunnelDegraded, Severity: SeverityWarning,
			Message: "Tunnel is serving traffic in a degraded state",
		})
	case cloudflare.StatusInactive:
		findings = append(findings, Finding{
			Code: FindingTunnelInactive, Severity: SeverityInfo,
			Message: "Tunnel has never been run",
		})
	}

	if in.Tunnel.Status != cloudflare.StatusInactive {
		findings = append(findings, evaluateConnectivity(in)...)
	}

	if in.RecentChanges > FlapThreshold {
		findings = append(findings, Finding{
			Code: FindingFlapping, Severity: SeverityWarning,
			Message: fmt.Sprintf("%d status changes in the last %s", in.RecentChanges, FlapWindow),
		})
	}

	findings = append(findings, evaluateIngress(in)...)
	findings = append(findings, evaluateAccessCoverage(in)...)
	findings = append(findings, evaluateAccessBypass(in)...)
	findings = append(findings, evaluateServiceTokens(in)...)
	findings = append(findings, evaluateAlertCoverage(in)...)

	return findings
}

// evaluateAlertCoverage reports a tunnel Cloudflare would stay quiet about.
// CFTM watching a tunnel is not the same as being told when it breaks, and the
// gap is invisible until the outage nobody hears about.
func evaluateAlertCoverage(in HealthInput) []Finding {
	if !in.AlertCoverage.Readable || in.AlertCoverage.CoversTunnel(in.Tunnel.ID) {
		return nil
	}
	return []Finding{{
		Code:     FindingNoCloudflareAlert,
		Severity: SeverityInfo,
		Message:  "No Cloudflare notification policy would fire if this tunnel goes down",
	}}
}

func evaluateConnectivity(in HealthInput) []Finding {
	if len(in.Connectors) == 0 {
		return []Finding{{
			Code: FindingNoConnectors, Severity: SeverityCritical,
			Message: "No cloudflared connector is registered",
		}}
	}

	var findings []Finding

	if n := len(in.Connections); n < ExpectedConnections {
		findings = append(findings, Finding{
			Code: FindingLowConnections, Severity: SeverityWarning,
			Message: fmt.Sprintf("%d of %d edge connections established", n, ExpectedConnections),
		})
	}

	if colos := distinctColos(in.Connections); len(colos) == 1 {
		findings = append(findings, Finding{
			Code: FindingSingleColo, Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"All edge connections terminate in %s, so a data center outage has no failover", colos[0]),
		})
	}

	if in.LatestRelease != "" {
		for _, c := range in.Connectors {
			if c.Version != "" && c.Version != in.LatestRelease {
				findings = append(findings, Finding{
					Code: FindingVersionDrift, Severity: SeverityWarning,
					Message: fmt.Sprintf("Connector %s runs cloudflared %s, latest is %s",
						shortID(c.ID), c.Version, in.LatestRelease),
				})
			}
		}
	}

	if want := ingressConfigVersion(in.Ingress); want > 0 {
		for _, c := range in.Connectors {
			if c.ConfigVersion > 0 && c.ConfigVersion != want {
				findings = append(findings, Finding{
					Code: FindingConfigDrift, Severity: SeverityWarning,
					Message: fmt.Sprintf("Connector %s applied configuration version %d, current is %d",
						shortID(c.ID), c.ConfigVersion, want),
				})
			}
		}
	}

	return findings
}

func evaluateIngress(in HealthInput) []Finding {
	var findings []Finding
	for _, rule := range in.Ingress {
		if rule.Hostname == "" || !isHTTPService(rule.Service) {
			continue
		}
		if strings.Contains(rule.OriginRequest, `"noTLSVerify":true`) {
			findings = append(findings, Finding{
				Code:     FindingNoTLSVerify,
				Severity: SeverityInfo,
				Hostname: rule.Hostname,
				Message:  "Origin certificate is not verified (noTLSVerify)",
			})
		}
		if strings.Contains(rule.Service, "//localhost") {
			findings = append(findings, Finding{
				Code:     FindingLocalhostOrigin,
				Severity: SeverityWarning,
				Hostname: rule.Hostname,
				Message:  "Origin uses localhost; cloudflared tries ::1 first, so prefer 127.0.0.1",
			})
		}
	}
	return findings
}

func evaluateAccessCoverage(in HealthInput) []Finding {
	if !in.AccessAuditAvailable {
		return nil
	}
	protected := map[string]bool{}
	for _, app := range in.AccessApps {
		for _, d := range app.Domains {
			protected[strings.ToLower(d)] = true
		}
	}

	var findings []Finding
	for _, rule := range in.Ingress {
		if rule.Hostname == "" || !isHTTPService(rule.Service) {
			continue
		}
		host := strings.ToLower(rule.Hostname)
		if protected[host] || in.ExpectedPublic[host] {
			continue
		}
		findings = append(findings, Finding{
			Code:     FindingAccessUnprotected,
			Severity: SeverityWarning,
			Hostname: rule.Hostname,
			Message:  "Hostname is publicly reachable without a Cloudflare Access application",
		})
	}
	return findings
}

// evaluateAccessBypass reports hostnames an Access application waives. Bypass
// disables every Access control and stops the authentication log, so the
// hostname looks protected while nothing is enforced or recorded.
func evaluateAccessBypass(in HealthInput) []Finding {
	if !in.AccessAuditAvailable {
		return nil
	}
	served := map[string]string{}
	for _, rule := range in.Ingress {
		if rule.Hostname == "" || !isHTTPService(rule.Service) {
			continue
		}
		host := strings.ToLower(rule.Hostname)
		if _, ok := served[host]; !ok {
			served[host] = rule.Hostname
		}
	}

	var findings []Finding
	for _, app := range in.AccessApps {
		if !app.HasBypass {
			continue
		}
		for _, d := range app.Domains {
			hostname, ok := served[strings.ToLower(d)]
			if !ok {
				continue
			}
			findings = append(findings, Finding{
				Code:     FindingAccessBypass,
				Severity: SeverityWarning,
				Hostname: hostname,
				Message:  bypassMessage(app, hostname),
			})
		}
	}
	return findings
}

func bypassMessage(app store.AccessApp, hostname string) string {
	// The normalized domain hides the path, so a path-scoped application would
	// otherwise read as if it waived the whole host.
	scope := hostname
	host := strings.ToLower(hostname)
	var scoped []string
	for _, raw := range app.RawDomains {
		if h, _, _ := strings.Cut(raw, "/"); h == host {
			scoped = append(scoped, raw)
		}
	}
	if len(scoped) > 0 {
		scope = strings.Join(scoped, ", ")
	}

	via := ""
	switch n := len(app.BypassPolicies); {
	case n == 1:
		via = fmt.Sprintf(" via policy %q", app.BypassPolicies[0])
	case n > 1:
		via = fmt.Sprintf(" via policies %s", strings.Join(app.BypassPolicies, ", "))
	}

	return fmt.Sprintf(
		"Access application %q bypasses %s%s, so those requests are not logged and no Access controls apply",
		app.Name, scope, via)
}

func evaluateServiceTokens(in HealthInput) []Finding {
	var findings []Finding
	for _, token := range in.ServiceTokens {
		if token.ExpiresAt.IsZero() {
			continue
		}
		remaining := token.ExpiresAt.Sub(in.Now)
		switch {
		case remaining <= 0:
			findings = append(findings, Finding{
				Code:     FindingTokenExpiring,
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("Access service token %q has expired", token.Name),
			})
		case remaining <= TokenExpiryWarning:
			findings = append(findings, Finding{
				Code:     FindingTokenExpiring,
				Severity: SeverityWarning,
				Message: fmt.Sprintf("Access service token %q expires in %d days",
					token.Name, int(remaining.Hours()/24)),
			})
		}
	}
	return findings
}

// WorstSeverity returns the highest severity among findings.
func WorstSeverity(findings []Finding) Severity {
	worst := Severity("")
	for _, f := range findings {
		if f.Severity.Rank() > worst.Rank() {
			worst = f.Severity
		}
	}
	return worst
}

func distinctColos(conns []store.Connection) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range conns {
		if c.ColoName == "" || seen[c.ColoName] {
			continue
		}
		seen[c.ColoName] = true
		out = append(out, c.ColoName)
	}
	return out
}

// ingressConfigVersion returns the configuration version the ingress rules were
// captured at.
func ingressConfigVersion(rules []store.IngressRule) int {
	for _, r := range rules {
		if r.ConfigVersion > 0 {
			return r.ConfigVersion
		}
	}
	return 0
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
