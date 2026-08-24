// Command dsbox runs a dataspace connector.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/auth"
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
	showVersion := flag.Bool("version", false, "print the build identity and exit")
	flag.Parse()

	version := buildVersion(debug.ReadBuildInfo())
	if *showVersion {
		// Straight to stdout, not through slog: this is the answer to a
		// question, and a bug report should be able to paste it.
		fmt.Println(version)
		return nil
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read configuration %q: %w", *configPath, err)
	}
	cfg, err := config.Load(data, os.Getenv)
	if err != nil {
		return fmt.Errorf("load configuration %q: %w", *configPath, err)
	}
	if cfg.MgmtToken == "" {
		slog.Warn("no mgmt_token configured; the management API will reject every authenticated request")
	}

	// Authentication material is read before anything listens. Both failures
	// below are fatal rather than degraded: a connector that cannot verify a
	// counterparty, or cannot sign for itself, has nothing useful to offer,
	// and starting anyway would turn a configuration mistake into a runtime
	// mystery.
	var (
		roster  auth.Roster
		signKey ed25519.PrivateKey
	)
	if cfg.AuthRequired() {
		if signKey, err = auth.LoadPrivateKey(cfg.ParticipantKey); err != nil {
			return fmt.Errorf("load participant_key %q: %w", cfg.ParticipantKey, err)
		}
		signerRaw, err := base64.RawURLEncoding.DecodeString(cfg.RosterSigner)
		if err != nil {
			return fmt.Errorf("roster_signer is not base64url: %w", err)
		}
		if len(signerRaw) != ed25519.PublicKeySize {
			return fmt.Errorf("roster_signer is %d bytes, want %d", len(signerRaw), ed25519.PublicKeySize)
		}
		if roster, err = auth.LoadRoster(cfg.RosterPath, ed25519.PublicKey(signerRaw)); err != nil {
			return err
		}
	} else {
		// One line at startup, not one per request: an operator who chose this
		// needs to see it in the boot log, and a per-request warning would
		// bury the rest of the log under it.
		slog.Warn("connector-to-connector authentication is OFF; every DSP endpoint accepts anonymous requests",
			"dsp_addr", cfg.DSPAddr)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data_dir %q: %w", cfg.DataDir, err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "dsbox.db"))
	if err != nil {
		return fmt.Errorf("open database in %q: %w", cfg.DataDir, err)
	}
	defer st.Close()

	// pulls counts the data pulls the router has in flight, and cancelPulls
	// ends them. Both are used at shutdown, below, in that order: a pull
	// records its outcome on the way out, so it has to be stopped before it
	// can be waited for.
	dspHandler, pulls, cancelPulls := dsp.NewRouter(cfg, st, roster, signKey)

	// These timeouts bound how long a connection can sit idle at each phase,
	// so a client that dribbles headers (or never sends any) cannot hold a
	// slot open indefinitely. WriteTimeout no longer bounds a data transfer:
	// the data endpoint rolls its own write deadline forward as bytes leave
	// (dsp.copyUnderRollingDeadline), so a stream is bounded by time without
	// progress rather than by total elapsed time.
	//
	// It stays at 30 seconds regardless, because it still bounds every other
	// response this server writes. Nothing outside the data endpoint rolls a
	// deadline, so without this a client that stops reading a negotiation or
	// catalog response parks that handler for as long as it likes.
	//
	// Overriding it per-request from a handler is safe, and that is not
	// obvious: net/http clears the connection's write deadline after every
	// request (conn.serve, right after finishRequest) and re-arms it from
	// WriteTimeout while reading the next one, so a deadline the data
	// endpoint set cannot survive into the next request on a keep-alive
	// connection.
	dspSrv := &http.Server{
		Addr:              cfg.DSPAddr,
		Handler:           dspHandler,
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

	// version leads: the first question asked about a log from a connector
	// that misbehaved is which build produced it.
	slog.Info("connector started",
		"version", version,
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

	// In-flight pulls write their outcome to the store when they finish, so
	// they have to finish before the deferred st.Close() above. Cancelling
	// first is what makes the cap below adequate: a cancelled pull stops
	// copying at once and its deferred write lands immediately, where an
	// uncancelled one would copy for as long as the counterparty kept
	// dribbling, run the cap out, and lose the row. DECISIONS.md section
	// 34.3 has the argument, and section 33.6 is where the cap was first set
	// and flagged for exactly this re-examination.
	//
	// Still bounded rather than indefinite. The cap is now a backstop for a
	// pull stuck somewhere cancellation does not reach — a blocking syscall
	// on the file it is writing — not the ordinary path.
	cancelPulls()
	pullsDone := make(chan struct{})
	go func() { pulls.Wait(); close(pullsDone) }()
	select {
	case <-pullsDone:
	case <-time.After(5 * time.Second):
		slog.Warn("shutting down with data pulls still in flight; their outcome will not be recorded")
	}
	return err
}

func serve(s *http.Server, name string, failed chan<- error) {
	slog.Info("listening", "listener", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		failed <- err
	}
}
