package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/daknoblo/CFTM/internal/store"
)

func hasFinding(findings []Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func healthyInput() HealthInput {
	return HealthInput{
		Tunnel: store.Tunnel{ID: "t1", Name: "edge.example.com", Status: "healthy"},
		Connectors: []store.Connector{
			{ID: "conn1", Version: "2026.4.0", ConfigVersion: 3},
		},
		Connections: []store.Connection{
			{UUID: "1", ColoName: "FRA"},
			{UUID: "2", ColoName: "FRA"},
			{UUID: "3", ColoName: "AMS"},
			{UUID: "4", ColoName: "AMS"},
		},
		Ingress: []store.IngressRule{
			{Hostname: "app.example.com", Service: "http://127.0.0.1:8091", ConfigVersion: 3},
		},
		LatestRelease: "2026.4.0",
		Now:           time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
}

func TestEvaluateHealthyTunnelHasNoFindings(t *testing.T) {
	if findings := Evaluate(healthyInput()); len(findings) != 0 {
		t.Errorf("findings = %+v, want none for a fully healthy tunnel", findings)
	}
}

func TestEvaluateStatusFindings(t *testing.T) {
	cases := map[string]struct {
		status string
		code   string
		sev    Severity
	}{
		"down":     {"down", FindingTunnelDown, SeverityCritical},
		"degraded": {"degraded", FindingTunnelDegraded, SeverityWarning},
		"inactive": {"inactive", FindingTunnelInactive, SeverityInfo},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := healthyInput()
			in.Tunnel.Status = tc.status
			findings := Evaluate(in)
			if !hasFinding(findings, tc.code) {
				t.Fatalf("findings = %+v, want %s", findings, tc.code)
			}
			if got := WorstSeverity(findings); got.Rank() < tc.sev.Rank() {
				t.Errorf("WorstSeverity() = %q, want at least %q", got, tc.sev)
			}
		})
	}
}

func TestEvaluateInactiveTunnelSkipsConnectivityNoise(t *testing.T) {
	in := healthyInput()
	in.Tunnel.Status = "inactive"
	in.Connectors = nil
	in.Connections = nil

	findings := Evaluate(in)
	if hasFinding(findings, FindingNoConnectors) || hasFinding(findings, FindingLowConnections) {
		t.Errorf("findings = %+v, want no connectivity findings for a tunnel that never ran", findings)
	}
}

func TestEvaluateConnectivityFindings(t *testing.T) {
	t.Run("no connectors", func(t *testing.T) {
		in := healthyInput()
		in.Connectors = nil
		if findings := Evaluate(in); !hasFinding(findings, FindingNoConnectors) {
			t.Errorf("findings = %+v, want %s", findings, FindingNoConnectors)
		}
	})

	t.Run("fewer than four connections", func(t *testing.T) {
		in := healthyInput()
		in.Connections = in.Connections[:2]
		if findings := Evaluate(in); !hasFinding(findings, FindingLowConnections) {
			t.Errorf("findings = %+v, want %s", findings, FindingLowConnections)
		}
	})

	t.Run("single data center", func(t *testing.T) {
		in := healthyInput()
		for i := range in.Connections {
			in.Connections[i].ColoName = "FRA"
		}
		if findings := Evaluate(in); !hasFinding(findings, FindingSingleColo) {
			t.Errorf("findings = %+v, want %s", findings, FindingSingleColo)
		}
	})
}

func TestEvaluateVersionAndConfigDrift(t *testing.T) {
	t.Run("outdated cloudflared", func(t *testing.T) {
		in := healthyInput()
		in.Connectors[0].Version = "2026.1.0"
		if findings := Evaluate(in); !hasFinding(findings, FindingVersionDrift) {
			t.Errorf("findings = %+v, want %s", findings, FindingVersionDrift)
		}
	})

	t.Run("unknown latest release stays quiet", func(t *testing.T) {
		in := healthyInput()
		in.Connectors[0].Version = "2026.1.0"
		in.LatestRelease = ""
		if findings := Evaluate(in); hasFinding(findings, FindingVersionDrift) {
			t.Errorf("findings = %+v, want no drift finding without a known release", findings)
		}
	})

	t.Run("stale configuration version", func(t *testing.T) {
		in := healthyInput()
		in.Connectors[0].ConfigVersion = 2
		if findings := Evaluate(in); !hasFinding(findings, FindingConfigDrift) {
			t.Errorf("findings = %+v, want %s", findings, FindingConfigDrift)
		}
	})
}

func TestEvaluateFlapping(t *testing.T) {
	in := healthyInput()
	in.RecentChanges = FlapThreshold + 1
	if findings := Evaluate(in); !hasFinding(findings, FindingFlapping) {
		t.Errorf("findings = %+v, want %s", findings, FindingFlapping)
	}
}

func TestEvaluateIngressFindings(t *testing.T) {
	in := healthyInput()
	in.Ingress = []store.IngressRule{
		{Hostname: "camera.example.com", Service: "https://127.0.0.1:8971",
			OriginRequest: `{"noTLSVerify":true}`, ConfigVersion: 3},
		{Hostname: "ssh.example.com", Service: "ssh://localhost:22", ConfigVersion: 3},
		{Hostname: "legacy.example.com", Service: "http://localhost:9000", ConfigVersion: 3},
	}

	findings := Evaluate(in)
	if !hasFinding(findings, FindingNoTLSVerify) {
		t.Errorf("findings = %+v, want %s", findings, FindingNoTLSVerify)
	}
	if !hasFinding(findings, FindingLocalhostOrigin) {
		t.Errorf("findings = %+v, want %s", findings, FindingLocalhostOrigin)
	}
	// The SSH rule is not an HTTP service and must not raise the localhost warning.
	for _, f := range findings {
		if f.Code == FindingLocalhostOrigin && f.Hostname == "ssh.example.com" {
			t.Error("ssh:// rules should be exempt from the localhost warning")
		}
	}
}

func TestEvaluateAccessCoverage(t *testing.T) {
	in := healthyInput()
	in.Ingress = []store.IngressRule{
		{Hostname: "app.example.com", Service: "http://127.0.0.1:8091", ConfigVersion: 3},
		{Hostname: "public-app.example.com", Service: "http://127.0.0.1:8096", ConfigVersion: 3},
	}
	in.AccessApps = []store.AccessApp{{ID: "a1", Domains: []string{"app.example.com"}}}

	t.Run("audit unavailable stays quiet", func(t *testing.T) {
		if findings := Evaluate(in); hasFinding(findings, FindingAccessUnprotected) {
			t.Errorf("findings = %+v, want no coverage findings before the audit ran", findings)
		}
	})

	t.Run("audit available reports the gap", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true
		findings := Evaluate(in)

		var unprotected []string
		for _, f := range findings {
			if f.Code == FindingAccessUnprotected {
				unprotected = append(unprotected, f.Hostname)
			}
		}
		if len(unprotected) != 1 || unprotected[0] != "public-app.example.com" {
			t.Errorf("unprotected = %v, want [public-app.example.com]", unprotected)
		}
	})

	t.Run("declared public hostnames are not a gap", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true
		in.ExpectedPublic = map[string]bool{"public-app.example.com": true}

		if findings := Evaluate(in); hasFinding(findings, FindingAccessUnprotected) {
			t.Errorf("findings = %+v, want no gap for a declared public hostname", findings)
		}
	})

	t.Run("declaring one hostname does not hide another", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true
		in.ExpectedPublic = map[string]bool{"other.example.com": true}

		if findings := Evaluate(in); !hasFinding(findings, FindingAccessUnprotected) {
			t.Errorf("findings = %+v, want public-app.example.com still reported", findings)
		}
	})
}

func TestEvaluateAccessBypass(t *testing.T) {
	in := healthyInput()
	in.Ingress = []store.IngressRule{
		{Hostname: "app.example.com", Service: "http://127.0.0.1:8091", ConfigVersion: 3},
		{Hostname: "metrics.example.com", Service: "http://127.0.0.1:8096", ConfigVersion: 3},
	}
	in.AccessApps = []store.AccessApp{
		{
			ID: "a1", Name: "App",
			Domains:    []string{"app.example.com"},
			RawDomains: []string{"app.example.com"},
		},
		{
			ID: "a2", Name: "Beszel",
			Domains:        []string{"metrics.example.com"},
			RawDomains:     []string{"metrics.example.com/api/beszel"},
			HasBypass:      true,
			BypassPolicies: []string{"DE only"},
		},
	}

	t.Run("audit unavailable stays quiet", func(t *testing.T) {
		if findings := Evaluate(in); hasFinding(findings, FindingAccessBypass) {
			t.Errorf("findings = %+v, want no bypass findings before the audit ran", findings)
		}
	})

	t.Run("only the bypassing application is reported", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true

		var hosts []string
		for _, f := range Evaluate(in) {
			if f.Code == FindingAccessBypass {
				hosts = append(hosts, f.Hostname)
			}
		}
		if len(hosts) != 1 || hosts[0] != "metrics.example.com" {
			t.Errorf("bypassed = %v, want [metrics.example.com]", hosts)
		}
	})

	t.Run("message keeps the path scope and names the policy", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true

		var msg string
		for _, f := range Evaluate(in) {
			if f.Code == FindingAccessBypass {
				msg = f.Message
			}
		}
		if !strings.Contains(msg, "metrics.example.com/api/beszel") {
			t.Errorf("message = %q, want the path-scoped domain", msg)
		}
		if !strings.Contains(msg, `"DE only"`) {
			t.Errorf("message = %q, want the policy name", msg)
		}
	})

	t.Run("a hostname served by several rules is reported once", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true
		in.Ingress = append(in.Ingress, store.IngressRule{
			Hostname: "metrics.example.com", Path: "/other",
			Service: "http://127.0.0.1:8097", ConfigVersion: 3,
		})

		var n int
		for _, f := range Evaluate(in) {
			if f.Code == FindingAccessBypass {
				n++
			}
		}
		if n != 1 {
			t.Errorf("bypass findings = %d, want 1", n)
		}
	})

	t.Run("a declared public hostname is still reported", func(t *testing.T) {
		in := in
		in.AccessAuditAvailable = true
		in.ExpectedPublic = map[string]bool{"metrics.example.com": true}

		if findings := Evaluate(in); !hasFinding(findings, FindingAccessBypass) {
			t.Errorf("findings = %+v, want bypass reported despite the public exception", findings)
		}
	})
}

func TestEvaluateServiceTokenExpiry(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		expiresAt time.Time
		wantCode  bool
		wantSev   Severity
	}{
		{"far in the future", now.Add(200 * 24 * time.Hour), false, ""},
		{"within the warning window", now.Add(10 * 24 * time.Hour), true, SeverityWarning},
		{"already expired", now.Add(-time.Hour), true, SeverityCritical},
		{"no expiry", time.Time{}, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthyInput()
			in.Now = now
			in.ServiceTokens = []store.ServiceToken{{ID: "tok", Name: "cftm-monitor", ExpiresAt: tc.expiresAt}}

			findings := Evaluate(in)
			got := hasFinding(findings, FindingTokenExpiring)
			if got != tc.wantCode {
				t.Fatalf("hasFinding(%s) = %v, want %v", FindingTokenExpiring, got, tc.wantCode)
			}
			if tc.wantCode && WorstSeverity(findings) != tc.wantSev {
				t.Errorf("WorstSeverity() = %q, want %q", WorstSeverity(findings), tc.wantSev)
			}
		})
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityCritical.Rank() <= SeverityWarning.Rank() {
		t.Error("critical should outrank warning")
	}
	if SeverityWarning.Rank() <= SeverityInfo.Rank() {
		t.Error("warning should outrank info")
	}
	if Severity("").Rank() != 0 {
		t.Error("unknown severity should rank zero")
	}
}

func TestNoCloudflareAlertIsOnlyReportedWhenPoliciesAreReadable(t *testing.T) {
	base := HealthInput{
		Tunnel: store.Tunnel{ID: "t1", Name: "edge", Status: "healthy"},
		Now:    time.Now(),
	}

	// The token could not list policies: silence is not evidence of a gap.
	unknown := base
	unknown.AlertCoverage = AlertCoverage{Readable: false}
	if hasFinding(Evaluate(unknown), FindingNoCloudflareAlert) {
		t.Error("an unreadable policy list must not raise the finding")
	}

	uncovered := base
	uncovered.AlertCoverage = AlertCoverage{Readable: true, Covered: []string{"other"}}
	if !hasFinding(Evaluate(uncovered), FindingNoCloudflareAlert) {
		t.Error("a tunnel no policy covers should raise the finding")
	}

	covered := base
	covered.AlertCoverage = AlertCoverage{Readable: true, Covered: []string{"t1"}}
	if hasFinding(Evaluate(covered), FindingNoCloudflareAlert) {
		t.Error("a covered tunnel must not raise the finding")
	}
}
