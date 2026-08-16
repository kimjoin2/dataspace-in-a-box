// Command dsbox runs a dataspace connector.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
	"github.com/kimjoin2/dataspace-in-a-box/internal/dsp"
	"github.com/kimjoin2/dataspace-in-a-box/internal/mgmt"
	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("connector stopped", "error", err)
		os.Exit(1)
	}
}

// run wires up the connector and blocks until it stops, either on a shutdown
// signal or a listener failure. Returning an error (instead of calling
// os.Exit inline) lets every deferred cleanup in this function actually run.
func run() error {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read configuration %q: %w", *configPath, err)
	}
	cfg, err := config.Load(data, os.Getenv)
	if err != nil {
		return fmt.Errorf("load configuration %q: %w", *configPath, err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data_dir %q: %w", cfg.DataDir, err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "dsbox.db"))
	if err != nil {
		return fmt.Errorf("open database in %q: %w", cfg.DataDir, err)
	}
	defer st.Close()

	// These timeouts bound how long a connection can sit idle at each phase,
	// so a client that dribbles headers (or never sends any) cannot hold a
	// slot open indefinitely. WriteTimeout will need revisiting once transfer
	// streaming lands: a streaming response can legitimately run longer than
	// 30 seconds and would be cut off mid-stream.
	dspSrv := &http.Server{
		Addr:              cfg.DSPAddr,
		Handler:           dsp.NewRouter(cfg, st),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	mgmtSrv := &http.Server{
		Addr:              cfg.MgmtAddr,
		Handler:           mgmt.NewRouter(cfg, st),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err = <-failed:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Shut both servers down concurrently so each gets the full timeout,
	// instead of one server's shutdown eating into the other's grace period.
	var wg sync.WaitGroup
	for name, srv := range map[string]*http.Server{"dsp": dspSrv, "management": mgmtSrv} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := srv.Shutdown(shutdownCtx); e != nil {
				slog.Error("forced shutdown", "listener", name, "error", e)
			}
		}()
	}
	wg.Wait()
	return err
}

func serve(s *http.Server, name string, failed chan<- error) {
	slog.Info("listening", "listener", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		failed <- err
	}
}
