package store

import "time"

// Tunnel is the persisted view of a Cloudflare Tunnel.
type Tunnel struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	TunType         string    `json:"tunType"`
	ConfigSrc       string    `json:"configSrc"`
	RemoteConfig    bool      `json:"remoteConfig"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	ConnsActiveAt   time.Time `json:"connsActiveAt"`
	ConnsInactiveAt time.Time `json:"connsInactiveAt"`
	FirstSeen       time.Time `json:"firstSeen"`
	LastSeen        time.Time `json:"lastSeen"`
}

// Connector is a running cloudflared instance.
type Connector struct {
	ID            string    `json:"id"`
	TunnelID      string    `json:"tunnelId"`
	Version       string    `json:"version"`
	Arch          string    `json:"arch"`
	ConfigVersion int       `json:"configVersion"`
	Features      []string  `json:"features"`
	RunAt         time.Time `json:"runAt"`
	LastSeen      time.Time `json:"lastSeen"`
}

// Connection is one QUIC connection to a Cloudflare data center.
type Connection struct {
	UUID        string    `json:"uuid"`
	ConnectorID string    `json:"connectorId"`
	TunnelID    string    `json:"tunnelId"`
	ColoName    string    `json:"coloName"`
	OriginIP    string    `json:"originIp"`
	OpenedAt    time.Time `json:"openedAt"`
	LastSeen    time.Time `json:"lastSeen"`
}

// IngressRule is a single hostname-to-origin mapping of a tunnel.
type IngressRule struct {
	TunnelID      string    `json:"tunnelId"`
	Index         int       `json:"index"`
	Hostname      string    `json:"hostname"`
	Path          string    `json:"path"`
	Service       string    `json:"service"`
	OriginRequest string    `json:"originRequest"`
	ConfigVersion int       `json:"configVersion"`
	SeenAt        time.Time `json:"seenAt"`
}

// AccessApp is a Cloudflare Access application together with a flattened
// summary of its policies.
type AccessApp struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Domains     []string  `json:"domains"`
	Type        string    `json:"type"`
	HasBypass   bool      `json:"hasBypass"`
	HasToken    bool      `json:"hasToken"`
	PolicyCount int       `json:"policyCount"`
	LastSeen    time.Time `json:"lastSeen"`
}

// ServiceToken is Access service token metadata.
type ServiceToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ClientID  string    `json:"clientId"`
	ExpiresAt time.Time `json:"expiresAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

// StatusChange is one transition in a tunnel's status history.
type StatusChange struct {
	TunnelID   string    `json:"tunnelId"`
	FromStatus string    `json:"fromStatus"`
	Status     string    `json:"status"`
	ChangedAt  time.Time `json:"changedAt"`
}

// Event kinds recorded in the event log.
const (
	EventTunnelStatus      = "tunnel_status"
	EventTunnelAdded       = "tunnel_added"
	EventTunnelRemoved     = "tunnel_removed"
	EventConnectorAdded    = "connector_added"
	EventConnectorRemoved  = "connector_removed"
	EventConfigChanged     = "config_changed"
	EventVersionDrift      = "version_drift"
	EventProbe             = "probe"
	EventAccessCoverage    = "access_coverage"
	EventServiceTokenAging = "service_token_aging"
	EventPollFailed        = "poll_failed"
)

// Event is a single entry of the UI event log.
type Event struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	TunnelID  string    `json:"tunnelId"`
	Hostname  string    `json:"hostname"`
	FromState string    `json:"fromState"`
	ToState   string    `json:"toState"`
	Message   string    `json:"message"`
}

// ProbeResult is the outcome of one origin probe.
type ProbeResult struct {
	Hostname   string    `json:"hostname"`
	CheckedAt  time.Time `json:"checkedAt"`
	StatusCode int       `json:"statusCode"`
	Class      string    `json:"class"`
	LatencyMS  int       `json:"latencyMs"`
	Error      string    `json:"error"`
}

// PollRun records the outcome of one collector cycle.
type PollRun struct {
	StartedAt  time.Time `json:"startedAt"`
	DurationMS int       `json:"durationMs"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error"`
}

// TunnelDiff reports what changed between two tunnel snapshots.
type TunnelDiff struct {
	Changes []StatusChange
	Added   []Tunnel
	Removed []Tunnel
}

// ConnectorChange reports a connector that stayed registered but changed its
// cloudflared version or the configuration version it has applied.
type ConnectorChange struct {
	ID                string
	FromVersion       string
	ToVersion         string
	FromConfigVersion int
	ToConfigVersion   int
}

// ConnectorDiff reports which connectors appeared, vanished or changed.
type ConnectorDiff struct {
	Added   []Connector
	Removed []Connector
	Changed []ConnectorChange
}

// Uptime summarizes availability over a time window.
type Uptime struct {
	Window       time.Duration `json:"window"`
	HealthyRatio float64       `json:"healthyRatio"`
	Changes      int           `json:"changes"`
	Observed     bool          `json:"observed"`
}

// Percent renders the healthy ratio as a percentage.
func (u Uptime) Percent() float64 { return u.HealthyRatio * 100 }
