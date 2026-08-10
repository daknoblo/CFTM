// Package web contains the server-rendered UI components and their view models.
package web

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/daknoblo/CFTM/internal/collector"
)

// Layout is the chrome shared by every page.
type Layout struct {
	Title        string
	ActivePath   string
	AssetVersion string
	Version      string
}

// Finding is one derived health observation, ready for display.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Hostname string `json:"hostname,omitempty"`
	// MutedAt is set only on the muted list of a tunnel page.
	MutedAt time.Time `json:"mutedAt,omitzero"`
}

// Heartbeat is one bucket of the uptime bar.
type Heartbeat struct {
	Status string `json:"status"`
	Label  string `json:"label"`
}

// Uptime is a rendered availability figure.
type Uptime struct {
	Label    string        `json:"label"`
	Percent  float64       `json:"percent"`
	Observed bool          `json:"observed"`
	Window   time.Duration `json:"window"`
	// Nominal is the requested window; a shorter Window means the tunnel has
	// not been monitored for that long yet.
	Nominal time.Duration `json:"nominal"`
}

// Connector is a running cloudflared instance.
type Connector struct {
	ID            string    `json:"id"`
	ShortID       string    `json:"shortId"`
	Version       string    `json:"version"`
	Arch          string    `json:"arch"`
	ConfigVersion int       `json:"configVersion"`
	Features      []string  `json:"features"`
	RunAt         time.Time `json:"runAt"`
	Outdated      bool      `json:"outdated"`
}

// Connection is one QUIC connection to a Cloudflare data center.
type Connection struct {
	UUID     string    `json:"uuid"`
	Colo     string    `json:"colo"`
	OriginIP string    `json:"originIp"`
	OpenedAt time.Time `json:"openedAt"`
}

// Ingress is one hostname-to-origin mapping.
type Ingress struct {
	TunnelID    string      `json:"tunnelId"`
	TunnelName  string      `json:"tunnelName"`
	Hostname    string      `json:"hostname"`
	Path        string      `json:"path,omitempty"`
	Service     string      `json:"service"`
	Kind        string      `json:"kind"`
	Probeable   bool        `json:"probeable"`
	NoTLSVerify bool        `json:"noTlsVerify"`
	Access      AccessState `json:"access"`
	Probe       Probe       `json:"probe"`
}

// AccessState summarizes the Access protection of a hostname.
type AccessState struct {
	Known     bool   `json:"known"`
	Protected bool   `json:"protected"`
	AppName   string `json:"appName,omitempty"`
	HasBypass bool   `json:"hasBypass"`
	HasToken  bool   `json:"hasToken"`
}

// Probe is the latest probe outcome for a hostname.
type Probe struct {
	Known      bool      `json:"known"`
	Class      string    `json:"class,omitempty"`
	StatusCode int       `json:"statusCode,omitempty"`
	LatencyMS  int       `json:"latencyMs,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checkedAt,omitzero"`
}

// Probes summarizes the latest probe outcome across a tunnel's hostnames.
type Probes struct {
	// Total counts the probeable hostnames, Checked the ones with a result.
	Total   int    `json:"total"`
	Checked int    `json:"checked"`
	OK      int    `json:"ok"`
	Worst   string `json:"worst,omitempty"`
}

// TunnelCard is the dashboard summary of one tunnel.
type TunnelCard struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Status       string      `json:"status"`
	Severity     string      `json:"severity,omitempty"`
	Connectors   int         `json:"connectors"`
	Connections  int         `json:"connections"`
	Colos        []string    `json:"colos"`
	Versions     []string    `json:"versions"`
	IngressCount int         `json:"ingressCount"`
	Probes       Probes      `json:"probes"`
	Findings     []Finding   `json:"findings"`
	Uptime24h    Uptime      `json:"uptime24h"`
	Heartbeats   []Heartbeat `json:"-"`
	LastSeen     time.Time   `json:"lastSeen"`
	// ConnectedFor is how long the current connections have been up, as reported
	// by Cloudflare. Unlike Uptime24h it needs no history of our own, so it says
	// something useful from the very first poll.
	ConnectedFor string `json:"connectedFor,omitempty"`
}

// TunnelDetail is the full view of a single tunnel.
type TunnelDetail struct {
	Card TunnelCard `json:"card"`
	// Ignored are the findings muted for this tunnel, offered back for undo.
	Ignored     []Finding    `json:"ignored"`
	TunType     string       `json:"tunType"`
	ConfigSrc   string       `json:"configSrc"`
	Remote      bool         `json:"remoteConfig"`
	CreatedAt   time.Time    `json:"createdAt"`
	ActiveAt    time.Time    `json:"connsActiveAt"`
	InactiveAt  time.Time    `json:"connsInactiveAt"`
	ConfigVer   int          `json:"configVersion"`
	Connectors  []Connector  `json:"connectors"`
	Connections []Connection `json:"connections"`
	Ingress     []Ingress    `json:"ingress"`
	Uptimes     []Uptime     `json:"uptimes"`
	Events      []Event      `json:"events"`
}

// Event is one entry of the event log.
type Event struct {
	Timestamp  time.Time `json:"timestamp"`
	Kind       string    `json:"kind"`
	KindLabel  string    `json:"kindLabel"`
	TunnelID   string    `json:"tunnelId,omitempty"`
	TunnelName string    `json:"tunnelName,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	Message    string    `json:"message"`
	Severity   string    `json:"severity"`
}

// PollStatus is the collector health shown in the header. It carries no
// credential material, only whether one is configured.
type PollStatus struct {
	LastRun       time.Time `json:"lastRun"`
	LastSuccess   time.Time `json:"lastSuccess"`
	DurationMS    int       `json:"durationMs"`
	Error         string    `json:"error,omitempty"`
	RateKnown     bool      `json:"rateKnown"`
	RateRemaining int       `json:"rateRemaining"`
	RateQuota     int       `json:"rateQuota"`
	RateResetAt   time.Time `json:"rateResetAt,omitzero"`
	ProbeEnabled  bool      `json:"probeEnabled"`
	ProbeToken    bool      `json:"probeTokenConfigured"`
}

// Dashboard is the model of the index page.
type Dashboard struct {
	Tunnels []TunnelCard `json:"tunnels"`
	Poll    PollStatus   `json:"poll"`
	Totals  Totals       `json:"totals"`
}

// Totals are the account-wide counters shown above the cards.
type Totals struct {
	Tunnels     int `json:"tunnels"`
	Healthy     int `json:"healthy"`
	Connectors  int `json:"connectors"`
	Connections int `json:"connections"`
	Hostnames   int `json:"hostnames"`
	Findings    int `json:"findings"`
	Unprotected int `json:"unprotected"`
}

// IngressPage is the model of the ingress inventory page.
type IngressPage struct {
	Rules        []Ingress
	ProbeEnabled bool
	ProbeToken   bool
}

// AuditPage is the model of the Access audit page.
type AuditPage struct {
	AuditedAt time.Time
	Available bool
	Apps      []AccessApp
	Tokens    []ServiceToken
	// Unprotected are hostnames without an Access application that were not
	// declared as intentional.
	Unprotected []Ingress
	// IntentionallyPublic are the declared exceptions, monitored like any other
	// hostname but not counted as a gap.
	IntentionallyPublic []Ingress
	Findings            []Finding
	// Logins summarizes the Access authentication log, when it is collected.
	Logins        []AccessLogin
	LoginsAt      time.Time
	LoginsEnabled bool
	LoginTotals   LoginTotals
	// ProbeTokenKnown is false when a service token is configured that this
	// account does not have, which is the usual result of rotating it wrong.
	ProbeTokenConfigured bool
	ProbeTokenKnown      bool
}

// AccessLogin is the authentication summary of one application.
type AccessLogin struct {
	AppDomain string
	Allowed   int
	Denied    int
	Users     int
	LastAt    time.Time
}

// LoginTotals aggregates the summary across every application.
type LoginTotals struct {
	Allowed int
	Denied  int
	Apps    int
}

// AccessApp is a Cloudflare Access application with its policy summary.
type AccessApp struct {
	Name        string
	Domains     []string
	Type        string
	PolicyCount int
	HasBypass   bool
	HasToken    bool
	Matched     bool
}

// ServiceToken is Access service token metadata.
type ServiceToken struct {
	Name      string
	ClientID  string
	ExpiresAt time.Time
	DaysLeft  int
	Severity  string
	// InUse marks the token probing is configured with.
	InUse bool
}

// AboutPage lists build and runtime information.
type AboutPage struct {
	Version      string
	Commit       string
	BuiltAt      time.Time
	GoVersion    string
	AccountID    string
	PollInterval time.Duration
	Retention    int
	Features     []FeatureState
	Permissions  []Permission
}

// FeatureState is one optional capability and why it is or is not running.
type FeatureState struct {
	Name string
	// State is one of "on", "off" or "unconfigured".
	State  string
	Detail string
}

// Permission is one area of the Cloudflare API and whether the token may read
// it. It is filled from what the calls actually returned, not from guesswork.
type Permission struct {
	Name string
	// State is one of "ok", "forbidden", "error", "disabled" or "unknown".
	State string
	// Required is the Cloudflare token permission this area needs.
	Required string
	// Detail explains a failure, or what is lost without the permission.
	Detail string
	// Optional marks an area CFTM runs without.
	Optional  bool
	CheckedAt time.Time
}

// NotificationsPageView is the model of the notification outbox page.
type NotificationsPageView struct {
	Enabled     bool
	Transport   string
	MinSeverity string
	Cooldown    string
	Items       []NotificationItem
}

// NotificationItem is one recorded notification.
type NotificationItem struct {
	CreatedAt time.Time
	Severity  string
	Title     string
	Body      string
	Transport string
	Status    string
	Error     string
}

// LogPage renders the in-memory log buffer.
type LogPage struct {
	Lines []LogLine
}

// LogLine is one buffered log record.
type LogLine struct {
	Time    time.Time
	Level   string
	Message string
}

// StatusDotClass maps a tunnel status onto its indicator class.
func StatusDotClass(status string) string {
	switch status {
	case "healthy":
		return "status-dot status-dot-healthy"
	case "degraded":
		return "status-dot status-dot-degraded"
	case "down":
		return "status-dot status-dot-down"
	case "inactive":
		return "status-dot status-dot-inactive"
	default:
		return "status-dot"
	}
}

// BeatClass maps a heartbeat bucket onto its bar class.
func BeatClass(status string) string {
	switch status {
	case "healthy":
		return "beat beat-healthy"
	case "degraded":
		return "beat beat-degraded"
	case "down":
		return "beat beat-down"
	case "inactive":
		return "beat beat-inactive"
	default:
		return "beat"
	}
}

// BadgeClass maps a severity onto its badge class.
func BadgeClass(severity string) string {
	switch severity {
	case string(collector.SeverityCritical):
		return "badge badge-error"
	case string(collector.SeverityWarning):
		return "badge badge-warn"
	case string(collector.SeverityInfo):
		return "badge badge-muted"
	default:
		return "badge badge-ok"
	}
}

// ProbeBadgeClass maps a probe class onto its badge class.
func ProbeBadgeClass(class string) string {
	switch class {
	case "ok":
		return "badge badge-ok"
	case "access_challenge", "access_denied":
		return "badge badge-muted"
	case "":
		return "badge"
	default:
		return "badge badge-error"
	}
}

// StatusBadgeClass maps a tunnel status onto its badge class.
func StatusBadgeClass(status string) string {
	switch status {
	case "healthy":
		return "badge badge-ok"
	case "degraded":
		return "badge badge-warn"
	case "down":
		return "badge badge-error"
	default:
		return "badge badge-muted"
	}
}

// linkableHostname accepts ordinary DNS names only. Wildcard ingress entries
// and the catch-all have nothing a browser could open.
var linkableHostname = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)

// HostnameHref returns the public URL of a hostname, or an empty string when it
// is not one. Everything published through a tunnel is reachable over HTTPS at
// the Cloudflare edge regardless of the origin scheme.
func HostnameHref(host string) string {
	if !linkableHostname.MatchString(host) {
		return ""
	}
	return "https://" + host
}

// ProbeSummaryLabel renders the probe counter, calling out hostnames that are
// probeable but have no result yet.
func ProbeSummaryLabel(p Probes) string {
	if p.Checked == 0 {
		return "not checked yet"
	}
	if p.Checked < p.Total {
		return fmt.Sprintf("%d / %d ok, %d pending", p.OK, p.Checked, p.Total-p.Checked)
	}
	return fmt.Sprintf("%d / %d ok", p.OK, p.Total)
}

// FormatDuration renders how long ago t was in the coarse form Cloudflare uses
// for tunnel uptime, for example "5 days".
func FormatDuration(t time.Time, now time.Time) string {
	if t.IsZero() || t.After(now) {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// FormatPercent renders an availability figure, or a dash when unobserved.
func FormatPercent(u Uptime) string {
	if !u.Observed {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", u.Percent)
}

// UptimeNote explains a figure that covers less than its window, which is the
// normal state for the first day after a fresh start.
func UptimeNote(u Uptime) string {
	if !u.Observed {
		return "no status recorded yet"
	}
	if u.Nominal > 0 && u.Window > 0 && u.Window < u.Nominal-time.Minute {
		return "observed " + FormatWindow(u.Window) + " so far"
	}
	return ""
}

// ShortVersion trims a development version down to something that fits the
// header; release builds are already short.
func ShortVersion(v string) string {
	if len(v) <= 24 {
		return v
	}
	return v[:21] + "…"
}

// FormatTime renders a timestamp in RFC 3339 so the browser can localize it.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// FormatRelative renders a coarse "x ago" label.
func FormatRelative(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// FormatWindow renders an uptime window as a compact label.
func FormatWindow(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// Join renders a string slice as a comma-separated list.
func Join(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}

// FilterKey builds the lowercase haystack used by the client-side table filter.
func FilterKey(parts ...string) string {
	return strings.ToLower(strings.Join(parts, " "))
}
