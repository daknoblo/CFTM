// Package demo builds a browsable CFTM instance from fabricated data and
// exports it as a static site, so the README can link to something real.
package demo

import (
	"encoding/json"
	"net/http"
	"time"
)

// AccountID is the fictional account the fixtures belong to.
const AccountID = "0123456789abcdef0123456789abcdef"

// tunnel identifiers, kept short so the demo URLs stay readable.
const (
	tunnelEdge = "3f1c9b2a-0e64-4a71-9d3c-5b8e1a2f4c6d"
	tunnelHome = "8c5d0e71-2b93-4f18-a6e2-7d4b9c3a1e50"
	tunnelLab  = "b27a6f04-91d5-4c3e-8f17-2a6c5e0b9d84"
)

// Handler serves the subset of the Cloudflare API that the collector uses. The
// demo runs the real collector against it, so the fixtures can never drift from
// what the production code path expects.
func Handler(now time.Time) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /accounts/"+AccountID+"/cfd_tunnel", func(w http.ResponseWriter, _ *http.Request) {
		writeResult(w, tunnels(now))
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/cfd_tunnel/{id}/connections", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, connectors(r.PathValue("id"), now))
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/cfd_tunnel/{id}/configurations", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, configuration(r.PathValue("id")))
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/access/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeResult(w, accessApps())
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/access/apps/{id}/policies", func(w http.ResponseWriter, r *http.Request) {
		writeResult(w, policies(r.PathValue("id")))
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/access/service_tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeResult(w, serviceTokens(now))
	})
	mux.HandleFunc("GET /accounts/"+AccountID+"/alerting/v3/policies", func(w http.ResponseWriter, _ *http.Request) {
		writeResult(w, alertPolicies())
	})

	return mux
}

// alertPolicies covers only the flagship tunnel, so the other two demonstrate
// the finding for a tunnel Cloudflare would stay quiet about.
func alertPolicies() []map[string]any {
	return []map[string]any{{
		"id": "pol-tunnel-health", "name": "Edge tunnel health",
		"alert_type": "tunnel_health_event", "enabled": true,
		"filters":    map[string]any{"tunnel_id": []string{tunnelEdge}},
		"mechanisms": map[string]any{"email": []map[string]any{{"id": "ops@example.com"}}},
	}}
}

func writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	// Mirrors the quota headers the client records for the status bar.
	w.Header().Set("Ratelimit-Limit", "1200")
	w.Header().Set("Ratelimit-Remaining", "1160")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tunnels(now time.Time) []map[string]any {
	conn := func(uuid, colo, origin string, opened time.Time) map[string]any {
		return map[string]any{
			"uuid": uuid, "client_id": "cn-" + uuid[:4], "colo_name": colo,
			"origin_ip": origin, "opened_at": rfc3339(opened),
		}
	}

	return []map[string]any{
		{
			"id": tunnelEdge, "name": "edge-frankfurt", "status": "healthy",
			"tun_type": "cfd_tunnel", "config_src": "cloudflare", "remote_config": true,
			"created_at":      rfc3339(now.AddDate(-1, -2, 0)),
			"conns_active_at": rfc3339(now.Add(-19 * 24 * time.Hour)),
			"connections": []map[string]any{
				conn("a1b2c3d4-1111-4a2b-8c3d-000000000001", "FRA", "203.0.113.17", now.Add(-19*24*time.Hour)),
				conn("a1b2c3d4-1111-4a2b-8c3d-000000000002", "FRA", "203.0.113.17", now.Add(-19*24*time.Hour)),
				conn("a1b2c3d4-1111-4a2b-8c3d-000000000003", "AMS", "203.0.113.17", now.Add(-19*24*time.Hour)),
				conn("a1b2c3d4-1111-4a2b-8c3d-000000000004", "AMS", "203.0.113.17", now.Add(-19*24*time.Hour)),
			},
		},
		{
			// Two connections instead of four, and every one of them in the same
			// data center: the low_ha_connections and single_colo findings.
			"id": tunnelHome, "name": "home-lab", "status": "degraded",
			"tun_type": "cfd_tunnel", "config_src": "cloudflare", "remote_config": true,
			"created_at":      rfc3339(now.AddDate(0, -7, 0)),
			"conns_active_at": rfc3339(now.Add(-4 * time.Hour)),
			"connections": []map[string]any{
				conn("a1b2c3d4-2222-4a2b-8c3d-000000000001", "MUC", "198.51.100.42", now.Add(-4*time.Hour)),
				conn("a1b2c3d4-2222-4a2b-8c3d-000000000002", "MUC", "198.51.100.42", now.Add(-4*time.Hour)),
			},
		},
		{
			"id": tunnelLab, "name": "staging", "status": "down",
			"tun_type": "cfd_tunnel", "config_src": "cloudflare", "remote_config": true,
			"created_at":        rfc3339(now.AddDate(0, -2, 0)),
			"conns_inactive_at": rfc3339(now.Add(-95 * time.Minute)),
			"connections":       []map[string]any{},
		},
	}
}

func connectors(tunnelID string, now time.Time) []map[string]any {
	switch tunnelID {
	case tunnelEdge:
		return []map[string]any{{
			"id": "cn-a1b2", "arch": "linux_amd64", "config_version": 14,
			"version": latestCloudflared, "features": []string{"ha-origin", "quic"},
			"run_at": rfc3339(now.Add(-19 * 24 * time.Hour)),
		}}
	case tunnelHome:
		return []map[string]any{{
			// An old cloudflared: the version_drift finding.
			"id": "cn-a1b2-2", "arch": "linux_arm64", "config_version": 6,
			"version": "2026.1.2", "features": []string{"ha-origin"},
			"run_at": rfc3339(now.Add(-4 * time.Hour)),
		}}
	default:
		return []map[string]any{}
	}
}

func configuration(tunnelID string) map[string]any {
	rule := func(host, service string, extra map[string]any) map[string]any {
		out := map[string]any{"hostname": host, "service": service}
		if extra != nil {
			out["originRequest"] = extra
		}
		return out
	}
	catchAll := map[string]any{"service": "http_status:404"}

	switch tunnelID {
	case tunnelEdge:
		return map[string]any{
			"version": 14, "source": "cloudflare", "tunnel_id": tunnelID,
			"config": map[string]any{"ingress": []map[string]any{
				rule("git.example.com", "http://127.0.0.1:3000", nil),
				rule("wiki.example.com", "http://127.0.0.1:8080", nil),
				rule("mail.example.com", "http://127.0.0.1:1080", nil),
				rule("status.example.com", "http://127.0.0.1:3001", nil),
				// Self-signed origin certificate: the ingress_no_tls_verify finding.
				rule("vault.example.com", "https://127.0.0.1:8200", map[string]any{"noTLSVerify": true}),
				rule("feed.example.com", "http://127.0.0.1:8088", nil),
				rule("ssh.example.com", "ssh://localhost:22", nil),
				catchAll,
			}},
		}
	case tunnelHome:
		return map[string]any{
			"version": 6, "source": "cloudflare", "tunnel_id": tunnelID,
			"config": map[string]any{"ingress": []map[string]any{
				rule("media.example.com", "http://127.0.0.1:8096", nil),
				rule("photos.example.com", "http://127.0.0.1:2342", nil),
				rule("books.example.com", "http://127.0.0.1:8083", nil),
				rule("home.example.com", "http://127.0.0.1:8123", nil),
				catchAll,
			}},
		}
	default:
		return map[string]any{
			"version": 2, "source": "cloudflare", "tunnel_id": tunnelID,
			"config": map[string]any{"ingress": []map[string]any{
				rule("preview.example.com", "http://127.0.0.1:4321", nil),
				catchAll,
			}},
		}
	}
}

// latestCloudflared is what the release check would resolve to. Pinning it
// keeps the demo reproducible instead of depending on GitHub.
const latestCloudflared = "2026.7.3"

func accessApps() []map[string]any {
	app := func(id, name string, domains ...string) map[string]any {
		return map[string]any{
			"id": id, "name": name, "domain": domains[0],
			"self_hosted_domains": domains, "type": "self_hosted",
		}
	}
	// media, feed and preview deliberately have no application.
	return []map[string]any{
		app("app-git", "Git", "git.example.com"),
		app("app-wiki", "Wiki", "wiki.example.com"),
		app("app-mail", "Webmail", "mail.example.com"),
		app("app-status", "Status page", "status.example.com"),
		app("app-vault", "Vault", "vault.example.com"),
		app("app-photos", "Photos", "photos.example.com"),
		app("app-books", "Books", "books.example.com"),
		app("app-home", "Home automation", "home.example.com"),
		app("app-orphan", "Retired service", "old.example.com"),
	}
}

func policies(appID string) []map[string]any {
	token := map[string]any{
		"id": "pol-token-" + appID, "name": "Monitoring", "decision": "non_identity",
		"include": []map[string]any{{"any_valid_service_token": map[string]any{}}},
	}
	people := map[string]any{
		"id": "pol-team-" + appID, "name": "Team", "decision": "allow",
		"include": []map[string]any{{"email_domain": map[string]any{"domain": "example.com"}}},
	}
	if appID == "app-status" {
		// A bypass policy makes the application public to everyone.
		return []map[string]any{{
			"id": "pol-bypass", "name": "Public status page", "decision": "bypass",
			"include": []map[string]any{{"everyone": map[string]any{}}},
		}}
	}
	return []map[string]any{people, token}
}

func serviceTokens(now time.Time) []map[string]any {
	return []map[string]any{
		{
			"id": "tok-monitor", "name": "cftm-monitor", "client_id": DemoClientID,
			"created_at": rfc3339(now.AddDate(0, -1, 0)),
			"expires_at": rfc3339(now.AddDate(1, 0, -1)),
		},
		{
			// Inside the 30-day window: the service_token_expiring finding.
			"id": "tok-ci", "name": "ci-pipeline", "client_id": "5f2a9c1e7b3d.access",
			"created_at": rfc3339(now.AddDate(-1, 0, 11)),
			"expires_at": rfc3339(now.Add(11 * 24 * time.Hour)),
		},
	}
}

// DemoClientID is the Access client ID the demo claims to probe with, so the
// audit page can mark it. There is no matching secret anywhere.
const DemoClientID = "d4e9f0a1c2b3.access"
