# TCK Gate and Metadata Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the official DSP TCK as a CI gate and make the version metadata protocol pass it.

**Architecture:** A single Go binary serves two listeners — public DSP and localhost-only management. Configuration is a pure function over YAML bytes plus an environment lookup, so validation is testable without I/O. The TCK harness runs the connector and the TCK runtime as two Docker Compose services on one network, and a small Go program turns the TCK's stdout into a pass/fail gate scoped to a whitelist of suites.

**Tech Stack:** Go 1.26 (standard library), `gopkg.in/yaml.v3`, Docker Compose, GitHub Actions, `eclipsedataspacetck/dsp-tck-runtime`.

## Global Constraints

Every task's requirements implicitly include this section. Values are copied from the spec and `CLAUDE.md`.

- Go 1.26. Module path `github.com/kimjoin2/dataspace-in-a-box`.
- The only permitted dependency in this milestone is `gopkg.in/yaml.v3`. Everything else is the standard library. Adding any other dependency requires asking first.
- All committed content is English: code, comments, docs, and commit messages.
- No plugin system, no SPI, no inheritance-based extension points.
- Permitted inputs are published, publicly obtainable sources only. Never consult non-public or draft material.
- `public_url` is required configuration. Never infer the external address from the `Host` header.
- The management listener binds to `127.0.0.1` by default.
- The connector speaks plain HTTP. There is no TLS configuration.
- Copyright is b7g. No organizational affiliation anywhere.
- When behavior is unclear, the DSP 2025-1 spec and the TCK decide — not intuition, and not how EDC does it.

## File Structure

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Module definition; the single YAML dependency |
| `internal/config/config.go` | Parse YAML, apply env overrides, validate. Pure — no file I/O |
| `internal/config/config_test.go` | Validation rules and override precedence |
| `internal/dsp/version.go` | Version metadata document types and handler |
| `internal/dsp/version_test.go` | Response body, content type, method handling |
| `internal/dsp/router.go` | Public DSP listener routes; mount point for `/2025-1/` |
| `internal/mgmt/router.go` | Management listener routes; `/health` |
| `internal/mgmt/router_test.go` | Health response |
| `cmd/dsbox/main.go` | Flag parsing, file reading, listener startup, graceful shutdown |
| `cmd/tckgate/main.go` | Parse TCK stdout, enforce the suite whitelist |
| `cmd/tckgate/main_test.go` | Gate decisions against a captured real TCK output fixture |
| `cmd/tckgate/testdata/` | Real TCK output captured in Task 4 |
| `Dockerfile` | Connector image for the harness |
| `Makefile` | `build`, `test`, `tck` |
| `test/tck/compose.yaml` | Two services on one network |
| `test/tck/config.properties` | TCK configuration |
| `test/tck/dsbox.yaml` | Connector configuration for the harness |
| `test/tck/run.sh` | Bring up the harness, capture output, tear down |
| `config.example.yaml` | Documented example configuration |
| `.github/workflows/ci.yml` | Unit tests and TCK gate |
| `.github/workflows/cla.yml` | CLA Assistant |
| `CONTRIBUTING.md` | How to contribute; CLA requirement |

Two boundaries matter and are load-bearing for later work. `internal/config` never touches the filesystem, which is why its rules can be tested exhaustively in microseconds. `internal/dsp` and `internal/mgmt` return `http.Handler` values and hold no package-level state, so a later task can run several connectors in one process (`DECISIONS.md` §19) without refactoring.

**Note on a deliberate spec deviation:** the design document anticipates `dsp.NewRouter(cfg)` taking a `Config`. No route in this milestone reads configuration, so `NewRouter()` takes no argument. An unused parameter added in advance is exactly the speculative structure §4 rules out; the parameter arrives with the first route that needs it.

---

### Task 1: Go module and configuration package

**Files:**
- Create: `go.mod`, `go.sum`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Create: `config.example.yaml`

**Interfaces:**
- Consumes: nothing
- Produces: `config.Config` struct with fields `PublicURL string`, `DevMode bool`, `DSPAddr string`, `MgmtAddr string`; and `config.Load(data []byte, getenv func(string) string) (Config, error)`

- [ ] **Step 1: Initialize the module**

```bash
cd ~/b7g/dataspace-in-a-box
go mod init github.com/kimjoin2/dataspace-in-a-box
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: Write the failing tests**

Create `internal/config/config_test.go`:

```go
package config

import "testing"

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load([]byte("public_url: https://connector.example.org\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSPAddr != "0.0.0.0:8080" {
		t.Errorf("DSPAddr = %q, want 0.0.0.0:8080", cfg.DSPAddr)
	}
	if cfg.MgmtAddr != "127.0.0.1:8081" {
		t.Errorf("MgmtAddr = %q, want 127.0.0.1:8081", cfg.MgmtAddr)
	}
}

func TestLoadRequiresPublicURL(t *testing.T) {
	_, err := Load([]byte("dev_mode: true\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error when public_url is absent")
	}
}

func TestLoadRejectsPlainHTTPOutsideDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://connector.example.org\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for http without dev_mode")
	}
}

func TestLoadAllowsPlainHTTPInDevMode(t *testing.T) {
	_, err := Load([]byte("public_url: http://dsbox:8080\ndev_mode: true\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsRelativePublicURL(t *testing.T) {
	_, err := Load([]byte("public_url: /dsp\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a non-absolute public_url")
	}
}

func TestLoadRejectsTrailingSlash(t *testing.T) {
	_, err := Load([]byte("public_url: https://connector.example.org/\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for a trailing slash in public_url")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	cfg, err := Load(
		[]byte("public_url: https://from-file.example.org\ndsp_addr: 0.0.0.0:9999\n"),
		env(map[string]string{
			"DSBOX_PUBLIC_URL": "https://from-env.example.org",
			"DSBOX_DSP_ADDR":   "0.0.0.0:7777",
		}),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicURL != "https://from-env.example.org" {
		t.Errorf("PublicURL = %q, want the environment value", cfg.PublicURL)
	}
	if cfg.DSPAddr != "0.0.0.0:7777" {
		t.Errorf("DSPAddr = %q, want the environment value", cfg.DSPAddr)
	}
}

func TestInvalidDevModeEnvIsAnError(t *testing.T) {
	_, err := Load(
		[]byte("public_url: https://connector.example.org\n"),
		env(map[string]string{"DSBOX_DEV_MODE": "yes-please"}),
	)
	if err == nil {
		t.Fatal("Load: expected an error for an unparsable DSBOX_DEV_MODE")
	}
}

func TestMalformedYAMLIsAnError(t *testing.T) {
	_, err := Load([]byte("public_url: [unclosed\n"), env(nil))
	if err == nil {
		t.Fatal("Load: expected an error for malformed YAML")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`

- [ ] **Step 4: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config resolves the connector configuration from a YAML document and
// environment overrides.
//
// Load performs no I/O: the caller supplies the document bytes and an
// environment lookup. That keeps every validation rule testable without a
// filesystem, and keeps the precedence between file and environment in one
// readable place.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved connector configuration.
type Config struct {
	// PublicURL is the connector's external address. It is required: behind a
	// reverse proxy the connector cannot determine this itself, and inferring
	// it from the Host header is never acceptable.
	PublicURL string `yaml:"public_url"`

	// DevMode permits a plain http PublicURL. The local demo and the TCK
	// harness run without a proxy; nothing else should set this.
	DevMode bool `yaml:"dev_mode"`

	// DSPAddr is the listen address for public DSP endpoints.
	DSPAddr string `yaml:"dsp_addr"`

	// MgmtAddr is the listen address for the management API. It binds to
	// localhost by default so a firewall mistake cannot expose it.
	MgmtAddr string `yaml:"mgmt_addr"`
}

const (
	defaultDSPAddr  = "0.0.0.0:8080"
	defaultMgmtAddr = "127.0.0.1:8081"
)

// Load parses the YAML document, applies environment overrides, and validates
// the result. Environment values take precedence over the file.
func Load(data []byte, getenv func(string) string) (Config, error) {
	cfg := Config{DSPAddr: defaultDSPAddr, MgmtAddr: defaultMgmtAddr}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if v := getenv("DSBOX_PUBLIC_URL"); v != "" {
		cfg.PublicURL = v
	}
	if v := getenv("DSBOX_DSP_ADDR"); v != "" {
		cfg.DSPAddr = v
	}
	if v := getenv("DSBOX_MGMT_ADDR"); v != "" {
		cfg.MgmtAddr = v
	}
	if v := getenv("DSBOX_DEV_MODE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("DSBOX_DEV_MODE: %w", err)
		}
		cfg.DevMode = b
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.PublicURL == "" {
		return fmt.Errorf("public_url is required: behind a proxy the connector cannot infer its own external address")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return fmt.Errorf("public_url: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("public_url must be absolute, got %q", c.PublicURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !c.DevMode {
			return fmt.Errorf("public_url must use https unless dev_mode is true, got %q", c.PublicURL)
		}
	default:
		return fmt.Errorf("public_url scheme must be http or https, got %q", u.Scheme)
	}
	if strings.HasSuffix(c.PublicURL, "/") {
		return fmt.Errorf("public_url must not end with a slash, got %q", c.PublicURL)
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, all nine tests

- [ ] **Step 6: Write the example configuration**

Create `config.example.yaml`:

```yaml
# The connector's external address, as seen by other participants.
# Required. Behind a reverse proxy the connector cannot determine this itself,
# so it is never inferred from request headers. No trailing slash.
public_url: https://connector.example.org

# Permit a plain http public_url. Only for local demos and the TCK harness,
# which run without a reverse proxy.
dev_mode: false

# Listen address for public DSP endpoints.
dsp_addr: 0.0.0.0:8080

# Listen address for the management API. Localhost by default: exposing it is
# a deliberate act, not an accident.
mgmt_addr: 127.0.0.1:8081
```

Every value above can be overridden by `DSBOX_PUBLIC_URL`, `DSBOX_DEV_MODE`, `DSBOX_DSP_ADDR`, and `DSBOX_MGMT_ADDR`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/config config.example.yaml
git commit -m "feat: configuration loading with environment overrides

public_url is required and validated: https unless dev_mode, absolute, no
trailing slash. Load takes bytes and an environment lookup rather than a path,
so the rules are testable without a filesystem."
```

---

### Task 2: DSP version metadata endpoint

**Files:**
- Create: `internal/dsp/version.go`
- Create: `internal/dsp/router.go`
- Test: `internal/dsp/version_test.go`

**Interfaces:**
- Consumes: nothing from Task 1
- Produces: `dsp.NewRouter() http.Handler`, serving `GET /.well-known/dspace-version`

The response values below are read from DSP 2025-1 and are provisional. Task 5 corrects them from TCK output if the TCK disagrees. Do not "fix" them on intuition before then.

- [ ] **Step 1: Write the failing tests**

Create `internal/dsp/version_test.go`:

```go
package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionEndpointReturnsProtocolVersions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body VersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Context) != 1 || body.Context[0] != ContextURL {
		t.Errorf("@context = %v, want [%s]", body.Context, ContextURL)
	}
	if len(body.ProtocolVersions) != 1 {
		t.Fatalf("protocolVersions has %d entries, want 1", len(body.ProtocolVersions))
	}
	v := body.ProtocolVersions[0]
	if v.Version != Version || v.Path != VersionPath || v.Binding != Binding {
		t.Errorf("protocolVersions[0] = %+v, want {%s %s %s}", v, Version, VersionPath, Binding)
	}
}

func TestVersionEndpointRejectsPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/.well-known/dspace-version", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/2025-1/catalog/request", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 while the catalog protocol is unimplemented", rec.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -v`
Expected: FAIL — `undefined: NewRouter`, `undefined: VersionResponse`

- [ ] **Step 3: Write the version document**

Create `internal/dsp/version.go`:

```go
// Package dsp implements the Dataspace Protocol 2025-1 HTTPS binding.
package dsp

import (
	"encoding/json"
	"net/http"
)

// Values of the version metadata document.
//
// These are read from DSP 2025-1. Where the official TCK disagrees with any of
// them, the TCK is authoritative and these change — that rule is why this
// project can claim compliance at all.
const (
	ContextURL  = "https://w3id.org/dspace/2025/1/context.jsonld"
	Version     = "2025-1"
	VersionPath = "/2025-1"
	Binding     = "HTTPS"
)

// ProtocolVersion is one entry of the version metadata document. Path is
// relative to the base path hosting the version metadata endpoint, so the
// routes for this version live under {base}{Path}.
type ProtocolVersion struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Binding string `json:"binding"`
}

// VersionResponse is the version metadata document in the fixed compact form.
// DSP 2025-1 fixes the serialization, so this is ordinary structured JSON and
// no JSON-LD or RDF processing is involved.
type VersionResponse struct {
	Context          []string          `json:"@context"`
	ProtocolVersions []ProtocolVersion `json:"protocolVersions"`
}

func versionDocument() VersionResponse {
	return VersionResponse{
		Context: []string{ContextURL},
		ProtocolVersions: []ProtocolVersion{
			{Version: Version, Path: VersionPath, Binding: Binding},
		},
	}
}

// handleVersion serves the version metadata endpoint. It is unauthenticated:
// discovering which protocol versions a connector speaks precedes any trust
// relationship with it.
func handleVersion(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(versionDocument()) // a static document cannot fail to marshal
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
```

- [ ] **Step 4: Write the router**

Create `internal/dsp/router.go`:

```go
package dsp

import "net/http"

// NewRouter returns the handler for the public DSP listener.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/dspace-version", handleVersion)

	// Protocol routes mount under VersionPath and are added one protocol at a
	// time, in TCK order: catalog, contract negotiation, transfer process.
	// Until then, requests below that path are correctly 404.

	return mux
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dsp/ -v`
Expected: PASS, all three tests. The 405 test passes because Go 1.22+ pattern routing returns `405 Method Not Allowed` when a path matches under a different method.

- [ ] **Step 6: Commit**

```bash
git add internal/dsp
git commit -m "feat: DSP version metadata endpoint

Serves the fixed compact-form document from a struct; no JSON-LD library. The
context URL, binding, and path are provisional until the TCK confirms them."
```

---

### Task 3: Management listener and process wiring

**Files:**
- Create: `internal/mgmt/router.go`
- Test: `internal/mgmt/router_test.go`
- Create: `cmd/dsbox/main.go`
- Create: `Makefile`

**Interfaces:**
- Consumes: `config.Load` (Task 1), `dsp.NewRouter` (Task 2)
- Produces: `mgmt.NewRouter() http.Handler` serving `GET /health`; a runnable `dsbox` binary taking `-config <path>`

- [ ] **Step 1: Write the failing test**

Create `internal/mgmt/router_test.go`:

```go
package mgmt

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReturnsOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/mgmt/ -v`
Expected: FAIL — `undefined: NewRouter`

- [ ] **Step 3: Write the management router**

Create `internal/mgmt/router.go`:

```go
// Package mgmt serves the management API. It listens on a separate port from
// the DSP endpoints and binds to localhost by default, so exposing it is a
// deliberate configuration choice rather than a firewall accident.
package mgmt

import "net/http"

// NewRouter returns the handler for the management listener.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/mgmt/ -v`
Expected: PASS

- [ ] **Step 5: Write the entry point**

Create `cmd/dsbox/main.go`:

```go
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
```

- [ ] **Step 6: Write the Makefile**

Create `Makefile`:

```make
GO ?= go

.PHONY: build test tck

build:
	$(GO) build -o dsbox ./cmd/dsbox

test:
	$(GO) test ./...
```

- [ ] **Step 7: Verify the binary end to end by hand**

```bash
make build
printf 'public_url: http://localhost:8080\ndev_mode: true\n' > /tmp/dsbox-manual.yaml
./dsbox -config /tmp/dsbox-manual.yaml &
sleep 1
curl -sS -i http://localhost:8080/.well-known/dspace-version
curl -sS http://localhost:8081/health
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/health
kill %1
```

Expected: the first returns `200` with the version document, the second returns `{"status":"ok"}`, and the third returns `404` — proving the management route is not reachable on the public listener.

- [ ] **Step 8: Add build output to .gitignore and commit**

`.gitignore` already ignores `/dsbox`. Verify with `git status --short` that no binary is staged.

```bash
git add internal/mgmt cmd/dsbox Makefile
git commit -m "feat: management listener and process wiring

Two listeners, graceful shutdown, JSON structured logs. The management listener
binds to localhost, so a firewall mistake cannot expose it."
```

---

### Task 4: Docker image and TCK harness, first run

The deliverable is a real TCK run and its captured output. **The MET suite is expected to fail here.** Do not modify the connector to chase a green result in this task; Task 5 does that, informed by what this one observes.

**Files:**
- Create: `Dockerfile`
- Create: `test/tck/dsbox.yaml`
- Create: `test/tck/config.properties`
- Create: `test/tck/compose.yaml`
- Create: `test/tck/run.sh`
- Modify: `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: the `dsbox` binary and its `-config` flag (Task 3)
- Produces: `make tck` writes the TCK's stdout to `tck-output.txt` at the repository root

- [ ] **Step 1: Write the Dockerfile**

Create `Dockerfile`:

```dockerfile
# The connector image exists to run the TCK harness. Native execution is the
# documented way to run dsbox; see DECISIONS.md section 17.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/dsbox ./cmd/dsbox

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dsbox /dsbox
EXPOSE 8080 8081
ENTRYPOINT ["/dsbox"]
CMD ["-config", "/etc/dsbox/config.yaml"]
```

- [ ] **Step 2: Write the connector configuration for the harness**

Create `test/tck/dsbox.yaml`:

```yaml
# Connector configuration for the TCK harness only.
#
# public_url uses the Compose service name because the TCK addresses the
# connector by that name on the shared network. dev_mode permits plain http,
# which is the relaxation DECISIONS.md section 13 carves out for running
# without a reverse proxy.
public_url: http://dsbox:8080
dev_mode: true
dsp_addr: 0.0.0.0:8080

# Bound to all interfaces so the harness can poll readiness from the host.
# This is a test-only relaxation of the localhost default.
mgmt_addr: 0.0.0.0:8081
```

- [ ] **Step 3: Write the TCK configuration**

Create `test/tck/config.properties`:

```properties
# Configuration for the Eclipse DSP TCK running against dsbox.
# Property names follow config/tck/sample.tck.properties upstream.

dataspacetck.debug=true
dataspacetck.dsp.local.connector=false

# The TCK's own callback server. The connector reaches it by service name.
dataspacetck.port=8083
dataspacetck.callback.address=http://tck:8083

# The connector under test.
dataspacetck.dsp.connector.agent.id=urn:connector:dsbox-test
dataspacetck.dsp.connector.http.base.url=http://dsbox:8080
dataspacetck.dsp.connector.http.url=http://dsbox:8080/2025-1

# How long to wait for the connector to respond to a DSP message.
dataspacetck.dsp.default.wait=10000
```

The consumer-side webhook properties (`negotiation.initiate.url`, `transfer.initiate.url`) and the dataset, offer, and agreement identifiers are deliberately absent. They belong to protocols this milestone does not implement, and adding them now would imply a capability that does not exist.

- [ ] **Step 4: Pin the TCK image by digest**

```bash
docker pull eclipsedataspacetck/dsp-tck-runtime:latest
docker inspect --format='{{index .RepoDigests 0}}' eclipsedataspacetck/dsp-tck-runtime:latest
```

Copy the printed `repository@sha256:...` value into the compose file in the next step. A moving `latest` tag would let an upstream change silently alter a compliance result, which is the one thing this gate must not do.

- [ ] **Step 5: Write the Compose harness**

Create `test/tck/compose.yaml`, substituting the digest from Step 4:

```yaml
# The TCK and the connector talk to each other in both directions, so both run
# as services on one Compose network and address each other by service name.
# Running the connector on the host instead would behave differently on macOS
# and Linux.
services:
  dsbox:
    build:
      context: ../..
      dockerfile: Dockerfile
    volumes:
      - ./dsbox.yaml:/etc/dsbox/config.yaml:ro
    ports:
      # Management port, published for the readiness probe only.
      - "127.0.0.1:8081:8081"

  tck:
    image: eclipsedataspacetck/dsp-tck-runtime@sha256:PASTE_DIGEST_FROM_STEP_4
    depends_on:
      - dsbox
    volumes:
      - ./config.properties:/etc/tck/config.properties:ro
```

- [ ] **Step 6: Write the harness runner**

Create `test/tck/run.sh` and make it executable with `chmod +x test/tck/run.sh`:

```sh
#!/bin/sh
# Brings up the connector, runs the TCK against it, captures stdout, tears down.
# Always exits 0 when the TCK ran to completion: judging the output is the
# gate's job, not this script's.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/../.." && pwd)
compose="docker compose -f $dir/compose.yaml"
output="$root/tck-output.txt"

cleanup() {
	$compose down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

$compose up -d --build dsbox

printf 'waiting for the connector'
i=0
until curl -sf http://127.0.0.1:8081/health >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		echo
		echo "connector did not become ready" >&2
		$compose logs dsbox >&2
		exit 1
	fi
	printf '.'
	sleep 1
done
echo ' ready'

$compose run --rm tck >"$output" 2>&1 || true
echo "TCK output written to $output"
```

- [ ] **Step 7: Wire it into the Makefile**

Replace the `Makefile` with:

```make
GO ?= go

.PHONY: build test tck

build:
	$(GO) build -o dsbox ./cmd/dsbox

test:
	$(GO) test ./...

tck:
	./test/tck/run.sh
```

- [ ] **Step 8: Ignore the captured output**

Add to `.gitignore`:

```
# Captured TCK output; regenerate with `make tck`
/tck-output.txt
```

- [ ] **Step 9: Run the TCK and read the output**

Run: `make tck`
Then: `less tck-output.txt`

Record three things before continuing — they decide Task 5 and Task 6:

1. Does the output contain `test run complete`? If not, the harness itself is broken; fix the harness before anything else.
2. What does a single test result line look like, and what identifiers appear for the metadata suite (expected to start with `MET`)? Copy two real lines — one passing, one failing — into the commit message.
3. For each failing `MET` test, what does the TCK say it expected? This is the input to Task 5.

- [ ] **Step 10: Commit**

```bash
git add Dockerfile test/tck Makefile .gitignore
git commit -m "test: TCK harness running against the connector

Two Compose services on one network, image pinned by digest. The MET suite does
not pass yet; the captured output is what Task 5 works from.

Observed result lines:
  <paste one passing line>
  <paste one failing line>"
```

---

### Task 5: Make the MET suite pass

**Files:**
- Modify: `internal/dsp/version.go`
- Modify: `internal/dsp/version_test.go`
- Modify: `internal/dsp/router.go` (only if the TCK requires a path this milestone does not serve)
- Modify: `docs/superpowers/specs/2026-07-29-tck-gate-metadata-design.md` (only if an observed value differs from the design)

**Interfaces:**
- Consumes: `tck-output.txt` from Task 4
- Produces: a connector where every `MET` test passes

- [ ] **Step 1: Turn each MET failure into a Go test**

For every failing `MET` test, add a case to `internal/dsp/version_test.go` asserting the behavior the TCK says it expects. Write the assertion from the TCK's own message, not from a guess about what it meant. For example, if the TCK reports that it expected the context value `https://w3id.org/dspace/2025/1/context.json`, the test becomes:

```go
func TestContextMatchesTCKExpectation(t *testing.T) {
	if ContextURL != "https://w3id.org/dspace/2025/1/context.json" {
		t.Errorf("ContextURL = %q, want the value the TCK requires", ContextURL)
	}
}
```

Delete this scaffold test once the constant is corrected and the behavioral tests in Task 2 cover the value; a test that only restates a constant earns its place for one commit, not forever.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -v`
Expected: FAIL on each newly added case

- [ ] **Step 3: Correct the constants or handler**

Change the values in `internal/dsp/version.go` — or the routes in `router.go` — to what the TCK requires. Update the existing Task 2 assertions to match. Do not add configurability to satisfy the TCK: it is describing one correct answer, not a range.

- [ ] **Step 4: Run the unit tests**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Re-run the TCK**

Run: `make tck` then `grep -i 'MET' tck-output.txt`
Expected: every `MET` test passes. If any still fail, return to Step 1 with the new message. Do not proceed with a failing MET test.

- [ ] **Step 6: Amend the design document if reality differed**

If any observed value differs from what the spec's "Version metadata response" section predicted, correct that section and add one sentence recording what the TCK actually required. The spec says the TCK wins; this step is where that promise is kept.

- [ ] **Step 7: Commit**

```bash
git add internal/dsp docs/superpowers/specs
git commit -m "fix: satisfy the MET suite

Corrected the version metadata document to what the TCK requires rather than
what the specification reading implied. Design document amended to match."
```

---

### Task 6: TCK gate and CI

**Files:**
- Create: `cmd/tckgate/main.go`
- Create: `cmd/tckgate/main_test.go`
- Create: `cmd/tckgate/testdata/passing.txt`, `cmd/tckgate/testdata/failing.txt`
- Create: `.github/workflows/ci.yml`
- Modify: `Makefile`, `README.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: `tck-output.txt` produced by `test/tck/run.sh` (Task 4)
- Produces: `go run ./cmd/tckgate <file>` exiting 0 only when every whitelisted suite passed

The TCK reports through stdout; upstream's own TestContainers example keys on the phrases `test run complete` and `there were failing tests`, so those are the markers this gate uses too. The per-test line shape comes from the fixture, not from a guess.

- [ ] **Step 1: Capture real fixtures**

```bash
make tck
cp tck-output.txt cmd/tckgate/testdata/passing.txt
```

Then produce a failing fixture by hand: copy `passing.txt` to `failing.txt` and edit one `MET` line so it reads as a failure, using the exact failure wording observed in Task 4. Both fixtures are real TCK output, which is the point — the gate is tested against the format that actually exists.

- [ ] **Step 2: Write the failing tests**

Create `cmd/tckgate/main_test.go`:

```go
package main

import (
	"os"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestPassingOutputSatisfiesTheGate(t *testing.T) {
	report, err := evaluate(read(t, "testdata/passing.txt"), []string{"MET"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("gate rejected a passing run: %s", report)
	}
	if report.Required == 0 {
		t.Error("no MET tests were recognized; the result line pattern is wrong")
	}
}

func TestFailingMETTestFailsTheGate(t *testing.T) {
	report, err := evaluate(read(t, "testdata/failing.txt"), []string{"MET"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if report.OK() {
		t.Error("gate accepted a run with a failing MET test")
	}
}

func TestFailureOutsideTheWhitelistIsIgnored(t *testing.T) {
	report, err := evaluate(read(t, "testdata/passing.txt"), []string{"MET"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !report.OK() {
		t.Errorf("unimplemented suites must not fail the gate: %s", report)
	}
	if report.Skipped == 0 {
		t.Error("no non-MET results were seen, so this fixture cannot prove they are ignored")
	}
}

func TestTruncatedOutputIsAnError(t *testing.T) {
	_, err := evaluate("starting tests\n", []string{"MET"})
	if err == nil {
		t.Error("expected an error when the run did not complete")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/tckgate/ -v`
Expected: FAIL — `undefined: evaluate`

- [ ] **Step 4: Write the gate**

Create `cmd/tckgate/main.go`. Set `resultLine` to a pattern matching the real result lines observed in Task 4; the two capture groups must be the test identifier and its outcome.

```go
// Command tckgate decides whether a TCK run passes the compliance gate.
//
// Only suites whose protocol is implemented are required to pass. Adding a
// prefix to the whitelist is how a protocol is declared done, which keeps the
// README's claims and the build in agreement.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// whitelist holds the test identifier prefixes that must pass. Add a prefix
// only when its protocol is implemented.
var whitelist = []string{"MET"}

// resultLine matches one test result in the TCK's stdout. Group 1 is the test
// identifier, group 2 is the outcome. Derived from real output; see
// testdata/passing.txt.
var resultLine = regexp.MustCompile(`(?i)\b(MET|CAT|CN_C|CN|TP_C|TP)[_:][\w:._-]*\b.*\b(successful|passed|failed)\b`)

const completionMarker = "test run complete"

// Report is the gate's verdict over one TCK run.
type Report struct {
	Required int      // whitelisted tests seen
	Failed   []string // whitelisted tests that did not pass
	Skipped  int      // results outside the whitelist, reported but not gating
}

// OK reports whether the run satisfies the gate.
func (r Report) OK() bool { return r.Required > 0 && len(r.Failed) == 0 }

func (r Report) String() string {
	if r.OK() {
		return fmt.Sprintf("%d required tests passed, %d results outside the gate", r.Required, r.Skipped)
	}
	if r.Required == 0 {
		return "no required tests were recognized in the output"
	}
	sort.Strings(r.Failed)
	return fmt.Sprintf("%d of %d required tests failed: %s",
		len(r.Failed), r.Required, strings.Join(r.Failed, ", "))
}

// evaluate reads a complete TCK run and reports the gate's verdict. It errors
// when the run did not finish, because an incomplete run proves nothing and
// must never be mistaken for a pass.
func evaluate(output string, prefixes []string) (Report, error) {
	if !strings.Contains(strings.ToLower(output), completionMarker) {
		return Report{}, fmt.Errorf("the TCK run did not complete: %q not found in the output", completionMarker)
	}

	var report Report
	for _, line := range strings.Split(output, "\n") {
		m := resultLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, outcome := strings.ToUpper(m[1]), strings.ToLower(m[2])
		if !matchesAny(id, prefixes) {
			report.Skipped++
			continue
		}
		report.Required++
		if outcome == "failed" {
			report.Failed = append(report.Failed, strings.TrimSpace(line))
		}
	}
	return report, nil
}

func matchesAny(id string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tckgate <tck-output-file>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", os.Args[1], err)
		os.Exit(2)
	}
	report, err := evaluate(string(data), whitelist)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(report)
	if !report.OK() {
		os.Exit(1)
	}
}
```

Note the redundancy between `inWhitelist` and `matchesAny`: delete `inWhitelist` and keep `matchesAny`, which takes the prefixes as a parameter and is therefore testable. If you kept both, the tests still pass and the code is worse.

- [ ] **Step 5: Run the tests, adjusting the pattern until they pass**

Run: `go test ./cmd/tckgate/ -v`
Expected: PASS. If `TestPassingOutputSatisfiesTheGate` reports that no MET tests were recognized, the `resultLine` pattern does not match the real output — fix the pattern, not the test.

- [ ] **Step 6: Wire the gate into `make tck`**

Replace the `tck` target in the `Makefile`:

```make
tck:
	./test/tck/run.sh
	$(GO) run ./cmd/tckgate tck-output.txt
```

Run: `make tck`
Expected: the gate prints a pass summary and exits 0.

- [ ] **Step 7: Write the CI workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go vet ./...
      - run: go test ./...

  tck:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - name: Run the DSP TCK
        run: make tck
      - name: Upload TCK output
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: tck-output
          path: tck-output.txt
```

The output is uploaded even on failure: a compliance gate that hides its evidence is worth very little.

- [ ] **Step 8: Update the documented commands**

In `CLAUDE.md`, replace the Commands section body with:

```markdown
- Test: `go test ./...`
- TCK: `make tck` (runs the harness, then the gate)
- Build: `make build`
```

In `README.md`, change the Version metadata row of the status table from `in progress` to `gated in CI` and add one line below the table stating that `MET` is the only suite in the gate's whitelist.

- [ ] **Step 9: Commit and push**

```bash
git add cmd/tckgate Makefile .github/workflows/ci.yml README.md CLAUDE.md
git commit -m "ci: gate the build on the MET suite

The gate reads the TCK's stdout and requires every whitelisted test to pass.
Unimplemented suites are counted and reported but do not fail the build, so
adding a prefix to the whitelist is what declares a protocol done."
git push
```

Then confirm the run is green at https://github.com/kimjoin2/dataspace-in-a-box/actions before continuing.

---

### Task 7: Contribution material and CLA

**Files:**
- Create: `CONTRIBUTING.md`
- Create: `.github/workflows/cla.yml`

**Interfaces:**
- Consumes: nothing
- Produces: a repository that can accept an external pull request without a licensing problem

`DECISIONS.md` §5 makes the CLA non-optional: without assigned rights, the two-year Apache 2.0 conversion cannot be granted for contributed code.

- [ ] **Step 1: Write the contribution guide**

Create `CONTRIBUTING.md`:

```markdown
# Contributing

Thank you for considering a contribution.

## Before you write code

Read [`DECISIONS.md`](DECISIONS.md). It records what this project deliberately
does not do and why. A pull request that adds a plugin system, a general policy
engine, or TLS handling will be declined on those grounds no matter how well it
is written — please open an issue first if you want to argue a decision should
change.

There is no extension API by design. Extension happens by fork or by pull
request.

## Ground rules

- The default answer to "which dependency?" is the standard library. Adding one
  needs its own discussion in an issue first.
- Compliance is owed to the DSP 2025-1 specification and verified by the
  official TCK. When behavior is unclear, the spec and the TCK decide.
- English for code, comments, documentation, and commit messages.
- Every change needs a test. `go test ./...` and `make tck` must both pass.

## Contributor License Agreement

Every contributor must sign the CLA before a pull request can be merged. A bot
will comment on your first pull request with a link. This is required so that
each release can carry the Apache 2.0 conversion grant described in
[`LICENSE.md`](LICENSE.md).

## Getting started

```bash
make build   # build the binary
make test    # unit tests
make tck     # run the official DSP TCK and the compliance gate
```

`make tck` needs Docker.
```

- [ ] **Step 2: Create the CLA signature store**

CLA Assistant records signatures in a file in a repository. Create an empty file to hold them:

```bash
mkdir -p .github
printf '{"signedContributors":[]}\n' > .github/cla-signatures.json
git add .github/cla-signatures.json
```

- [ ] **Step 3: Write the CLA workflow**

Create `.github/workflows/cla.yml`:

```yaml
name: cla

on:
  issue_comment:
    types: [created]
  pull_request_target:
    types: [opened, closed, synchronize]

permissions:
  actions: write
  contents: write
  pull-requests: write
  statuses: write

jobs:
  cla:
    runs-on: ubuntu-latest
    steps:
      - name: CLA Assistant
        if: (github.event.comment.body == 'recheck' || github.event.comment.body == 'I have read the CLA Document and I hereby sign the CLA') || github.event_name == 'pull_request_target'
        uses: contributor-assistant/github-action@v2.6.1
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        with:
          path-to-signatures: ".github/cla-signatures.json"
          path-to-document: "https://github.com/kimjoin2/dataspace-in-a-box/blob/main/CONTRIBUTING.md#contributor-license-agreement"
          branch: "main"
          allowlist: "kimjoin2"
```

- [ ] **Step 4: Verify the workflow is syntactically valid**

Run: `gh workflow list` after pushing, and confirm `cla` appears.

The workflow cannot be fully exercised without an external pull request, and manufacturing one to test it is not worth it. Confirming it is registered and that `ci` is green is the check available here; note in the commit message that the end-to-end path is untested.

- [ ] **Step 5: Commit and push**

```bash
git add CONTRIBUTING.md .github/workflows/cla.yml .github/cla-signatures.json
git commit -m "chore: contribution guide and CLA automation

The CLA is required so releases can carry the Apache 2.0 conversion grant for
contributed code. The signing path itself is untested until a first external
pull request arrives."
git push
```

---

## Milestone verification

After Task 7, confirm every done criterion from the spec:

```bash
go build ./cmd/dsbox                 # 1. builds
make test                            # 6. unit tests pass
make tck                             # 3. MET passes, gate exits 0
gh run list --limit 3                # 4. CI green on main
```

Then read `README.md` and confirm criterion 5: the status table says `MET` is gated and the other three protocols are not implemented. If the table claims more than `make tck` proves, fix the table.

## What this unlocks

The catalog protocol is next. It inherits a working gate, so its done criterion is that `CAT` joins the whitelist in `cmd/tckgate/main.go`. It is also the first protocol needing persistence and dataset seeding, because the TCK expects specific dataset identifiers — `CAT_01_01_DATASETID` and friends — to already exist in the connector.
