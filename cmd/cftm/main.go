// Command cftm serves the Cloudflare Tunnel monitoring dashboard.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "time/tzdata" // makes TZ work on distroless images

	"github.com/daknoblo/CFTM/internal/cloudflare"
	"github.com/daknoblo/CFTM/internal/collector"
	"github.com/daknoblo/CFTM/internal/config"
	"github.com/daknoblo/CFTM/internal/logbuf"
	"github.com/daknoblo/CFTM/internal/notify"
	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/release"
	"github.com/daknoblo/CFTM/internal/server"
	"github.com/daknoblo/CFTM/internal/store"
	"github.com/daknoblo/CFTM/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-healthcheck" || os.Args[1] == "healthcheck") {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logBuf := logbuf.New(500)
	levelVar := new(slog.LevelVar)
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})
	logger := slog.New(logbuf.NewHandler(base, logBuf))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	levelVar.Set(cfg.SlogLevel())

	logger.Info("starting cftm",
		"version", version.Get().String(),
		"addr", cfg.Addr,
		"pollInterval", cfg.PollInterval.String())

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("closing store failed", "err", err)
		}
	}()

	cf := cloudflare.New(cfg.AccountID, cfg.APIToken, cloudflare.WithLogger(logger))
	coll := collector.New(cf, st, collector.Config{
		PollInterval:       cfg.PollInterval,
		ConfigRefreshEvery: cfg.ConfigRefreshEvery,
		RetentionDays:      cfg.RetentionDays,
	}, logger)

	// No transport is wired up yet: notifications are evaluated, deduplicated
	// and recorded in the outbox so the stream can be reviewed before a delivery
	// channel is chosen.
	dispatcher := notify.New(st, nil, notify.Config{
		Enabled:     cfg.NotifyEnabled,
		MinSeverity: collector.Severity(cfg.NotifyMinSeverity),
		Cooldown:    cfg.NotifyCooldown,
	}, logger)
	coll.WithEventSink(dispatcher)

	var activeProber collector.Prober
	if cfg.ProbeEnabled {
		activeProber = prober.New(prober.Config{
			Timeout:      cfg.ProbeTimeout,
			Concurrency:  cfg.ProbeConcurrency,
			ClientID:     cfg.AccessClientID,
			ClientSecret: cfg.AccessClientSecret,
		}, logger)
	}

	srv, err := server.New(st, coll, activeProber, logBuf, server.Config{
		AccountID:           cfg.AccountID,
		PollInterval:        cfg.PollInterval,
		RetentionDays:       cfg.RetentionDays,
		ProbeEnabled:        cfg.ProbeEnabled,
		ProbeToken:          cfg.HasAccessServiceToken(),
		ProbeClientID:       cfg.AccessClientID,
		AuditEnabled:        cfg.AccessAuditEnabled,
		ReleaseCheck:        cfg.ReleaseCheckEnabled,
		NotifyEnabled:       cfg.NotifyEnabled,
		NotifyTransport:     dispatcher.Transport(),
		NotifySeverity:      cfg.NotifyMinSeverity,
		NotifyCooldown:      cfg.NotifyCooldown,
		AccessLoginsEnabled: cfg.AccessLoginsEnabled,
		ExpectedPublic:      toSet(cfg.ExpectedPublic),
	}, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	spawn := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	spawn(func() { coll.Run(ctx) })
	if cfg.ReleaseCheckEnabled {
		spawn(func() { coll.RunReleaseChecks(ctx, release.New(), cfg.ReleaseCheckInterval) })
	}
	if cfg.AccessAuditEnabled {
		spawn(func() { coll.RunAccessAudit(ctx, cfg.AccessAuditInterval) })
	}
	if cfg.AccessLoginsEnabled {
		spawn(func() { coll.RunAccessLogins(ctx, cfg.AccessLoginsInterval, cfg.AccessLoginsWindow) })
	}
	if activeProber != nil {
		spawn(func() { coll.RunProbes(ctx, activeProber, cfg.ProbeInterval) })
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case runErr = <-errCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", "err", err)
	}

	stop()
	wg.Wait()

	return runErr
}

// healthcheck backs the container HEALTHCHECK; distroless has no curl.
func healthcheck() int {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + healthcheckAddr() + "/healthz") //nolint:gosec // G704: the address is derived from our own listen address
	if err != nil {
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// healthcheckAddr turns the configured listen address into a loopback target.
func healthcheckAddr() string {
	addr := strings.TrimSpace(os.Getenv("CFTM_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}
