package cloudflare

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestAccessAppDomains(t *testing.T) {
	app := AccessApp{
		Domain:            "https://App.example.com/path",
		SelfHostedDomains: []string{"app.example.com", "alt.example.com", ""},
	}
	got := app.Domains()
	want := []string{"app.example.com", "alt.example.com"}
	if len(got) != len(want) {
		t.Fatalf("Domains() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Domains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAccessAppRawDomainsKeepThePath(t *testing.T) {
	app := AccessApp{
		Domain:            "https://App.example.com/api/beszel",
		SelfHostedDomains: []string{"app.example.com/api/beszel", "alt.example.com", ""},
	}
	got := app.RawDomains()
	want := []string{"app.example.com/api/beszel", "alt.example.com"}
	if len(got) != len(want) {
		t.Fatalf("RawDomains() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RawDomains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAccessPolicyHelpers(t *testing.T) {
	cases := []struct {
		name       string
		policy     AccessPolicy
		wantToken  bool
		wantBypass bool
	}{
		{
			name:      "named service token",
			policy:    AccessPolicy{Include: []map[string]any{{"service_token": map[string]any{"token_id": "x"}}}},
			wantToken: true,
		},
		{
			name:      "any valid service token",
			policy:    AccessPolicy{Include: []map[string]any{{"any_valid_service_token": map[string]any{}}}},
			wantToken: true,
		},
		{
			name:   "email only",
			policy: AccessPolicy{Include: []map[string]any{{"email": map[string]any{"email": "a@b.c"}}}},
		},
		{
			name:       "bypass",
			policy:     AccessPolicy{Decision: "bypass", Include: []map[string]any{{"everyone": map[string]any{}}}},
			wantBypass: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.HasServiceTokenRule(); got != tc.wantToken {
				t.Errorf("HasServiceTokenRule() = %v, want %v", got, tc.wantToken)
			}
			if got := tc.policy.IsBypass(); got != tc.wantBypass {
				t.Errorf("IsBypass() = %v, want %v", got, tc.wantBypass)
			}
		})
	}
}

func TestListServiceTokens(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/accounts/acct/access/service_tokens"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		w.Write([]byte(`{"success":true,"result":[
		  {"id":"tok1","name":"cftm-monitor","client_id":"abc.access","expires_at":"2027-08-06T00:00:00Z"}
		]}`))
	})

	tokens, err := c.ListServiceTokens(t.Context())
	if err != nil {
		t.Fatalf("ListServiceTokens() error = %v, want nil", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(tokens))
	}
	if tokens[0].Name != "cftm-monitor" {
		t.Errorf("Name = %q, want \"cftm-monitor\"", tokens[0].Name)
	}
	if tokens[0].ExpiresAt.Year() != 2027 {
		t.Errorf("ExpiresAt = %v, want a 2027 date", tokens[0].ExpiresAt)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	cases := []struct {
		in       string
		wantZero bool
	}{
		{`"2021-01-25T18:22:34.317854Z"`, false},
		{`"2009-11-10T23:00:00Z"`, false},
		{`null`, true},
		{`""`, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var ts Timestamp
			if err := json.Unmarshal([]byte(tc.in), &ts); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.in, err)
			}
			if got := ts.IsZero(); got != tc.wantZero {
				t.Errorf("IsZero() = %v, want %v", got, tc.wantZero)
			}

			out, err := json.Marshal(ts)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if tc.wantZero && string(out) != "null" {
				t.Errorf("Marshal() = %s, want null", out)
			}
		})
	}
}

func TestTimestampInvalidValue(t *testing.T) {
	var ts Timestamp
	if err := json.Unmarshal([]byte(`"25.01.2021"`), &ts); err == nil {
		t.Error("Unmarshal of a non-RFC3339 string should fail")
	}
}

func TestRateLimitKnown(t *testing.T) {
	if (RateLimit{}).Known() {
		t.Error("zero RateLimit should not be Known")
	}
	if !(RateLimit{ObservedAt: time.Now()}).Known() {
		t.Error("observed RateLimit should be Known")
	}
}
