package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// setRequired sets the two mandatory variables so individual cases only have to
// declare what they actually exercise.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acc123")
	t.Setenv("CLOUDFLARE_API_TOKEN", "token123")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want \":8080\"", c.Addr)
	}
	if c.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", c.PollInterval)
	}
	if c.ConfigRefreshEvery != 10 {
		t.Errorf("ConfigRefreshEvery = %d, want 10", c.ConfigRefreshEvery)
	}
	if c.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", c.RetentionDays)
	}
	if !c.AccessAuditEnabled || !c.ReleaseCheckEnabled {
		t.Errorf("audit=%v release=%v, want both true", c.AccessAuditEnabled, c.ReleaseCheckEnabled)
	}
	if c.ProbeEnabled {
		t.Error("ProbeEnabled = true, want false by default")
	}
	if got, want := c.DBPath(), "/appdata/cftm.db"; got != want {
		t.Errorf("DBPath() = %q, want %q", got, want)
	}
}

func TestLoadRequiredMissing(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	for _, want := range []string{"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"poll interval below floor", "CFTM_POLL_INTERVAL", "1s", "at least 10s"},
		{"poll interval unparsable", "CFTM_POLL_INTERVAL", "soon", "invalid duration"},
		{"negative duration", "CFTM_PROBE_TIMEOUT", "-5s", "must be positive"},
		{"concurrency below floor", "CFTM_PROBE_CONCURRENCY", "0", "at least 1"},
		{"retention not a number", "CFTM_RETENTION_DAYS", "many", "invalid integer"},
		{"bool not parsable", "CFTM_PROBE_ENABLED", "yes please", "invalid boolean"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.val)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want an error for %s=%q", tc.key, tc.val)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSlogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"INFO":     slog.LevelInfo,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := (Config{LogLevel: in}).SlogLevel(); got != want {
			t.Errorf("SlogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExpectedPublicList(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want []string
	}{
		{"unset", "", nil},
		{"single", "public-app.example.com", []string{"public-app.example.com"}},
		{"lowercased and trimmed", " Public-App.example.com , feed.example.com ", []string{"public-app.example.com", "feed.example.com"}},
		{"empty entries dropped", "a.example.com,,", []string{"a.example.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("CFTM_EXPECTED_PUBLIC", tc.val)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(c.ExpectedPublic) != len(tc.want) {
				t.Fatalf("ExpectedPublic = %v, want %v", c.ExpectedPublic, tc.want)
			}
			for i := range tc.want {
				if c.ExpectedPublic[i] != tc.want[i] {
					t.Errorf("ExpectedPublic[%d] = %q, want %q", i, c.ExpectedPublic[i], tc.want[i])
				}
			}
		})
	}
}

func TestHasAccessServiceToken(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		secret string
		want   bool
	}{
		{"both set", "id", "secret", true},
		{"id only", "id", "", false},
		{"secret only", "", "secret", false},
		{"neither", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{AccessClientID: tc.id, AccessClientSecret: tc.secret}
			if got := c.HasAccessServiceToken(); got != tc.want {
				t.Errorf("HasAccessServiceToken() = %v, want %v", got, tc.want)
			}
		})
	}
}
