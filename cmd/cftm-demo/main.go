// Command cftm-demo renders CFTM against fabricated data, either as a local
// server for screenshots or as a static site for GitHub Pages.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/daknoblo/CFTM/internal/demo"
	"github.com/daknoblo/CFTM/internal/logbuf"
	"github.com/daknoblo/CFTM/internal/server"
	"github.com/daknoblo/CFTM/internal/store"
)

func main() {
	var (
		out  = flag.String("out", "", "write a static site to this directory")
		base = flag.String("base", "", "path prefix the static site is served under, e.g. /CFTM")
		addr = flag.String("addr", "", "serve on this address instead of exporting, e.g. 127.0.0.1:8099")
	)
	flag.Parse()

	if err := run(*out, *base, *addr); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(out, base, addr string) error {
	if out == "" && addr == "" {
		return fmt.Errorf("either -out or -addr is required")
	}

	logBuf := logbuf.New(200)
	logger := slog.New(logbuf.NewHandler(slog.NewTextHandler(os.Stdout, nil), logBuf))

	dir, err := os.MkdirTemp("", "cftm-demo")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	st, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	coll, stop, err := demo.Seed(ctx, st, logger)
	if err != nil {
		return err
	}
	defer stop()

	srv, err := server.New(st, coll, nil, logBuf, server.Config{
		AccountID:      demo.AccountID,
		PollInterval:   30 * time.Second,
		RetentionDays:  90,
		ProbeEnabled:   true,
		ProbeToken:     true,
		ProbeClientID:  demo.DemoClientID,
		AuditEnabled:   true,
		ReleaseCheck:   true,
		NotifyEnabled:  true,
		NotifySeverity: "warning",
		NotifyCooldown: time.Hour,
	}, logger)
	if err != nil {
		return err
	}

	if addr != "" {
		logger.Info("serving demo", "addr", addr)
		httpServer := &http.Server{
			Addr:              addr,
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		return httpServer.ListenAndServe()
	}

	tunnels, err := st.Tunnels(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		ids = append(ids, t.ID)
	}

	if err := demo.Export(srv.Handler(), ids, out, base); err != nil {
		return err
	}
	logger.Info("exported demo site", "dir", out, "pages", len(ids)+6)
	return nil
}
