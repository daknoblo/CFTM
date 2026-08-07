package cloudflare

import (
	"encoding/json"
	"strings"
	"time"
)

// Timestamp tolerates the three shapes Cloudflare uses for optional times:
// an RFC 3339 string, JSON null, and an empty string.
type Timestamp struct {
	time.Time
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

// MarshalJSON implements json.Marshaler, emitting null for the zero value.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time)
}

// Tunnel status values reported by the Cloudflare API.
const (
	StatusHealthy  = "healthy"
	StatusDegraded = "degraded"
	StatusDown     = "down"
	StatusInactive = "inactive"
)

// Tunnel is a Cloudflare Tunnel as returned by the cfd_tunnel endpoints.
type Tunnel struct {
	ID              string       `json:"id"`
	AccountTag      string       `json:"account_tag"`
	Name            string       `json:"name"`
	TunType         string       `json:"tun_type"`
	ConfigSrc       string       `json:"config_src"`
	RemoteConfig    bool         `json:"remote_config"`
	Status          string       `json:"status"`
	CreatedAt       Timestamp    `json:"created_at"`
	DeletedAt       Timestamp    `json:"deleted_at"`
	ConnsActiveAt   Timestamp    `json:"conns_active_at"`
	ConnsInactiveAt Timestamp    `json:"conns_inactive_at"`
	Connections     []Connection `json:"connections"`
}

// Connection is a single QUIC connection between a connector and a Cloudflare
// data center. A healthy tunnel maintains four of them.
type Connection struct {
	ID            string    `json:"id"`
	ClientID      string    `json:"client_id"`
	ClientVersion string    `json:"client_version"`
	ColoName      string    `json:"colo_name"`
	OpenedAt      Timestamp `json:"opened_at"`
	OriginIP      string    `json:"origin_ip"`
	UUID          string    `json:"uuid"`
}

// Connector is a running cloudflared instance. The API calls this a "Client".
type Connector struct {
	ID            string       `json:"id"`
	Arch          string       `json:"arch"`
	ConfigVersion int          `json:"config_version"`
	Conns         []Connection `json:"conns"`
	Features      []string     `json:"features"`
	RunAt         Timestamp    `json:"run_at"`
	Version       string       `json:"version"`
}

// TunnelConfiguration is the remotely managed configuration of a tunnel.
type TunnelConfiguration struct {
	AccountID string       `json:"account_id"`
	TunnelID  string       `json:"tunnel_id"`
	Version   int          `json:"version"`
	Source    string       `json:"source"`
	CreatedAt Timestamp    `json:"created_at"`
	Config    TunnelConfig `json:"config"`
}

// TunnelConfig holds the ingress rules and shared origin settings.
type TunnelConfig struct {
	Ingress       []IngressRule  `json:"ingress"`
	OriginRequest *OriginRequest `json:"originRequest"`
	WARPRouting   *WARPRouting   `json:"warp-routing"`
}

// WARPRouting reports whether private network routing is enabled.
type WARPRouting struct {
	Enabled bool `json:"enabled"`
}

// IngressRule maps a public hostname to an origin service.
type IngressRule struct {
	Hostname      string         `json:"hostname"`
	Path          string         `json:"path"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest"`
}

// OriginRequest are the per-hostname connection settings between cloudflared
// and the origin server.
type OriginRequest struct {
	Access                 *OriginAccess `json:"access"`
	CAPool                 string        `json:"caPool"`
	ConnectTimeout         *int          `json:"connectTimeout"`
	DisableChunkedEncoding *bool         `json:"disableChunkedEncoding"`
	HTTP2Origin            *bool         `json:"http2Origin"`
	HTTPHostHeader         string        `json:"httpHostHeader"`
	KeepAliveConnections   *int          `json:"keepAliveConnections"`
	KeepAliveTimeout       *int          `json:"keepAliveTimeout"`
	MatchSNIToHost         *bool         `json:"matchSNItoHost"`
	NoHappyEyeballs        *bool         `json:"noHappyEyeballs"`
	NoTLSVerify            *bool         `json:"noTLSVerify"`
	OriginServerName       string        `json:"originServerName"`
	ProxyType              string        `json:"proxyType"`
	TCPKeepAlive           *int          `json:"tcpKeepAlive"`
	TLSTimeout             *int          `json:"tlsTimeout"`
}

// OriginAccess is the JWT validation cloudflared performs for a hostname.
type OriginAccess struct {
	AudTag   []string `json:"audTag"`
	TeamName string   `json:"teamName"`
	Required bool     `json:"required"`
}

// AccessApp is a Cloudflare Access application.
type AccessApp struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Domain            string    `json:"domain"`
	Type              string    `json:"type"`
	AUD               string    `json:"aud"`
	SelfHostedDomains []string  `json:"self_hosted_domains"`
	CreatedAt         Timestamp `json:"created_at"`
	UpdatedAt         Timestamp `json:"updated_at"`
}

// AccessPolicy is one rule attached to an Access application. The include,
// exclude and require rule bodies are free-form, so they stay untyped.
type AccessPolicy struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Decision   string           `json:"decision"`
	Precedence int              `json:"precedence"`
	Include    []map[string]any `json:"include"`
	Exclude    []map[string]any `json:"exclude"`
	Require    []map[string]any `json:"require"`
}

// ServiceToken is a non-identity credential used for machine access.
type ServiceToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ClientID  string    `json:"client_id"`
	Duration  string    `json:"duration"`
	CreatedAt Timestamp `json:"created_at"`
	UpdatedAt Timestamp `json:"updated_at"`
	ExpiresAt Timestamp `json:"expires_at"`
}
