// Package config loads the runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// minPollInterval guards the shared Cloudflare API budget: the global limit is
// 1200 requests per five minutes and is consumed cumulatively by the dashboard
// and every other token of the same user.
const minPollInterval = 10 * time.Second

// Config holds every runtime setting. Secrets are only ever read from the
// environment and must never be echoed back by the API or the UI.
type Config struct {
	Addr     string
	DataDir  string
	LogLevel string

	AccountID string
	APIToken  string

	PollInterval       time.Duration
	ConfigRefreshEvery int
	RetentionDays      int

	AccessAuditEnabled  bool
	AccessAuditInterval time.Duration

	ReleaseCheckEnabled  bool
	ReleaseCheckInterval time.Duration

	ProbeEnabled       bool
	ProbeInterval      time.Duration
	ProbeTimeout       time.Duration
	ProbeConcurrency   int
	AccessClientID     string
	AccessClientSecret string

	// ExpectedPublic lists hostnames that are published without an Access
	// application on purpose.
	ExpectedPublic []string
}

// Load reads the configuration from the environment and validates it.
func Load() (Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	c := Config{
		Addr:     envString("CFTM_ADDR", ":8080"),
		DataDir:  envString("CFTM_DATA_DIR", "/appdata"),
		LogLevel: envString("CFTM_LOG_LEVEL", "info"),

		AccountID: strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")),
		APIToken:  strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")),

		AccessClientID:     strings.TrimSpace(os.Getenv("CFTM_ACCESS_CLIENT_ID")),
		AccessClientSecret: strings.TrimSpace(os.Getenv("CFTM_ACCESS_CLIENT_SECRET")),

		ExpectedPublic: envList("CFTM_EXPECTED_PUBLIC"),
	}

	var err error
	if c.PollInterval, err = envDuration("CFTM_POLL_INTERVAL", 30*time.Second); err != nil {
		fail("%w", err)
	} else if c.PollInterval < minPollInterval {
		fail("CFTM_POLL_INTERVAL must be at least %s, got %s", minPollInterval, c.PollInterval)
	}
	if c.AccessAuditInterval, err = envDuration("CFTM_ACCESS_AUDIT_INTERVAL", time.Hour); err != nil {
		fail("%w", err)
	}
	if c.ReleaseCheckInterval, err = envDuration("CFTM_RELEASE_CHECK_INTERVAL", 12*time.Hour); err != nil {
		fail("%w", err)
	}
	if c.ProbeInterval, err = envDuration("CFTM_PROBE_INTERVAL", 5*time.Minute); err != nil {
		fail("%w", err)
	}
	if c.ProbeTimeout, err = envDuration("CFTM_PROBE_TIMEOUT", 10*time.Second); err != nil {
		fail("%w", err)
	}

	if c.ConfigRefreshEvery, err = envInt("CFTM_CONFIG_REFRESH_EVERY", 10, 1); err != nil {
		fail("%w", err)
	}
	if c.RetentionDays, err = envInt("CFTM_RETENTION_DAYS", 90, 1); err != nil {
		fail("%w", err)
	}
	if c.ProbeConcurrency, err = envInt("CFTM_PROBE_CONCURRENCY", 4, 1); err != nil {
		fail("%w", err)
	}

	if c.AccessAuditEnabled, err = envBool("CFTM_ACCESS_AUDIT_ENABLED", true); err != nil {
		fail("%w", err)
	}
	if c.ReleaseCheckEnabled, err = envBool("CFTM_RELEASE_CHECK_ENABLED", true); err != nil {
		fail("%w", err)
	}
	if c.ProbeEnabled, err = envBool("CFTM_PROBE_ENABLED", false); err != nil {
		fail("%w", err)
	}

	if c.AccountID == "" {
		fail("CLOUDFLARE_ACCOUNT_ID is required")
	}
	if c.APIToken == "" {
		fail("CLOUDFLARE_API_TOKEN is required")
	}

	return c, errors.Join(errs...)
}

// DBPath returns the location of the SQLite database file.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "cftm.db")
}

// SlogLevel maps the configured level name onto an slog.Level, defaulting to info.
func (c Config) SlogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// HasAccessServiceToken reports whether end-to-end probing can authenticate
// against Cloudflare Access. Without it, probes only reach the Cloudflare edge.
func (c Config) HasAccessServiceToken() bool {
	return c.AccessClientID != "" && c.AccessClientSecret != ""
}

func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envList reads a comma-separated list, lowercased so hostname comparisons are
// case insensitive.
func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if v := strings.ToLower(strings.TrimSpace(item)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return def, fmt.Errorf("%s: invalid duration %q", key, raw)
	}
	if v <= 0 {
		return def, fmt.Errorf("%s must be positive, got %s", key, v)
	}
	return v, nil
}

func envInt(key string, def, minValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def, fmt.Errorf("%s: invalid integer %q", key, raw)
	}
	if v < minValue {
		return def, fmt.Errorf("%s must be at least %d, got %d", key, minValue, v)
	}
	return v, nil
}

func envBool(key string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def, fmt.Errorf("%s: invalid boolean %q", key, raw)
	}
	return v, nil
}
