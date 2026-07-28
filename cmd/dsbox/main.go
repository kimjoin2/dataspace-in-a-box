// Command dsbox runs a dataspace connector.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/dsp"
	"github.com/kimjoin2/dataspace-in-a-box/internal/mgmt"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	data, err := os.ReadFile(*configPath)
	if err != nil {
		slog.Error("read configuration", "path", *configPath, "error", err)
		os.Exit(1)
	}
	cfg, err := config.Load(data, os.Getenv)
	if err != nil {
		slog.Error("load configuration", "path", *configPath, "error", err)
		os.Exit(1)
	}

	dspSrv := &http.Server{Addr: cfg.DSPAddr, Handler: dsp.NewRouter()}
	mgmtSrv := &http.Server{Addr: cfg.MgmtAddr, Handler: mgmt.NewRouter()}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	failed := make(chan error, 2)
	go serve(dspSrv, "dsp", failed)
	go serve(mgmtSrv, "management", failed)

	slog.Info("connector started",
		"public_url", cfg.PublicURL,
		"dsp_addr", cfg.DSPAddr,
		"mgmt_addr", cfg.MgmtAddr,
		"dev_mode", cfg.DevMode,
	)

	exit := 0
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-failed:
		slog.Error("listener failed", "error", err)
		exit = 1
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dspSrv.Shutdown(shutdownCtx)
	mgmtSrv.Shutdown(shutdownCtx)
	os.Exit(exit)
}

func serve(s *http.Server, name string, failed chan<- error) {
	slog.Info("listening", "listener", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		failed <- err
	}
}
