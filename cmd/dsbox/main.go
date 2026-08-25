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

	// Authentication material is read before anything listens. The failures
	// below are fatal rather than degraded: a connector that cannot verify a
	// counterparty, or cannot sign for itself, has nothing useful to offer,
	// and starting anyway would turn a configuration mistake into a runtime
	// mystery.
	//
	// The same rule reaches a failure that cannot live in this block. A roster
	// older than one this connector has already run is refused against the
	// store, and the store is not open yet — so that check sits at the call
	// below that opens it, with its own reason for the placement.
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
		if roster, err = auth.LoadRoster(cfg.RosterPath, ed25519.PublicKey(signerRaw), time.Now()); err != nil {
			return err
		}
		// Its own line, at load time, rather than a field on the "connector
		// started" line below: that one is written after both listeners are
		// already serving and past the version check that can refuse to
		// start, so a roster this connector rejects would never be named in
		// the log at all.
		slog.Info("roster loaded",
			"roster_path", cfg.RosterPath,
			"roster_version", roster.Version(),
			"roster_expires_at", roster.ExpiresAt().UTC().Format(time.RFC3339),
		)
		// The harness rosters are dated a day out, so every make tck and make
		// demo run trips this. That is expected, not a defect.
		const rosterExpiryWarning = 30 * 24 * time.Hour
		if remaining := time.Until(roster.ExpiresAt()); remaining < rosterExpiryWarning {
			// Warn and carry on. The roster is still usable, and refusing to
			// start on an approaching expiry would take a working connector
			// down for a deadline it has not reached.
			slog.Warn("the roster expires soon; replace it before it does, or this connector will refuse every counterparty",
				"roster_version", roster.Version(),
				"roster_expires_at", roster.ExpiresAt().UTC().Format(time.RFC3339),
				"remaining", remaining.Round(time.Minute).String(),
			)
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

	// The version ratchet, once the store is open and before dsp.NewRouter
	// below: that call arms what this connector sends, so a roster older than
	// one this connector has already run has to be refused before anything
	// can go out under it. Fatal, like every other failure to establish
	// authentication material — an operator who rolled the roster back gets
	// told which version they handed over and which one is remembered.
	if cfg.AuthRequired() {
		if err := st.RecordRosterVersion(roster.Version()); err != nil {
			return err
		}
	}

	// routers.Pulls counts the data pulls the protocol router has in flight,
	// and routers.CancelPulls ends them. Both are used at shutdown, below, in
	// that order: a pull records its outcome on the way out, so it has to be
	// stopped before it can be waited for.
	//
	// routers.Initiate is unpacked at the mgmt.NewRouter call below rather
	// than passed whole, so internal/mgmt takes http.Handler values and never
	// imports internal/dsp.
	routers := dsp.NewRouter(cfg, st, roster, signKey)

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
		Handler:           routers.Protocol,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	mgmtSrv := &http.Server{
		Addr:              cfg.MgmtAddr,
		Handler:           mgmt.NewRouter(cfg, st, routers.RosterUsable, routers.Initiate.Negotiation, routers.Initiate.Transfer),
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
	// they have to finish before the deferred st.Close() above.
	if !drainPulls(routers.CancelPulls, routers.Pulls, pullDrainBudget) {
		slog.Warn("shutting down with data pulls still in flight; their outcome will not be recorded")
	}
	return err
}

// pullDrainBudget is how long shutdown gives in-flight pulls to stop and
// record what they did. Five seconds, from DECISIONS.md section 33.6, which
// set it against a window where a pull's only store write happened early and
// flagged it for re-examination once the outcome write landed at the end.
// That re-examination is section 34.3: with the cancellation in place the
// budget is no longer sized against a copy the counterparty controls the
// length of, so it stays where it was.
const pullDrainBudget = 5 * time.Second

// drainPulls cancels in-flight data pulls and waits for them, bounded. It
// reports whether they all finished: a pull still running when the budget
// expires is abandoned mid-copy, which loses its outcome row but is better
// than a connector that will not shut down while a counterparty keeps
// dribbling at it.
//
// Cancelling first is what makes the budget adequate rather than a guess. A
// cancelled pull stops copying at once and its deferred outcome write lands
// immediately; an uncancelled one would copy for as long as the counterparty
// kept feeding it, run the budget out, and lose exactly the row the wait
// exists to protect.
//
// It is a function rather than four lines inside run because run cannot be
// tested — it parses flags on the global CommandLine, binds two real
// listeners, and blocks on os/signal — and the cancel call is the single
// line connecting this mechanism to the running connector. Inline, dropping
// it would compile, pass every test, and show up only as the loss it was
// written to prevent.
func drainPulls(cancel context.CancelFunc, pulls *sync.WaitGroup, within time.Duration) bool {
	cancel()
	done := make(chan struct{})
	go func() { pulls.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(within):
		return false
	}
}

func serve(s *http.Server, name string, failed chan<- error) {
	slog.Info("listening", "listener", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		failed <- err
	}
}
