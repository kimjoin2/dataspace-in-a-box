# Connector authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every DSP endpoint requires a valid Ed25519-signed JWT from a roster participant, every outbound DSP call carries one, and the TCK still reports 64 of 65.

**Architecture:** A hand-written EdDSA-only JWT in `internal/auth`, verified against a roster JSON file loaded at startup. One middleware wraps the DSP mux. Outbound calls mint a fresh token per request. A `dsops` binary generates keys and mints tokens so the harness and the demo need no checked-in fixtures.

**Tech Stack:** Go standard library only — `crypto/ed25519`, `encoding/base64`, `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-08-19-connector-authentication-design.md`

## Global Constraints

- Go standard library only. Ask before adding a dependency; the default answer is no.
- English for all docs, comments, and identifiers. Everything committed here is public.
- Compliance is owed to DSP 2025-1, verified by the official TCK. The suite must stay at 64 of 65 with 0 results outside the gate.
- `401` with `WWW-Authenticate: Bearer` is the only new status code. Everything else keeps the existing rule: structural rejections are `400`, `404` only for an unknown id.
- Never accept a constraint that is not enforced.
- Private keys never enter the repository. Generated key material is gitignored.
- Each task ends green: `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` passing.

---

### Task 1: The token

**Files:**
- Create: `internal/auth/token.go`
- Test: `internal/auth/token_test.go`

**Interfaces:**
- Produces: `auth.Mint(priv ed25519.PrivateKey, iss, aud string, now time.Time, ttl time.Duration) (string, error)`; `auth.Verify(token string, keyFor func(iss string) (ed25519.PublicKey, bool), wantAud string, now time.Time) (iss string, err error)`; sentinel errors `auth.ErrMalformed`, `auth.ErrBadAlgorithm`, `auth.ErrUnknownIssuer`, `auth.ErrBadSignature`, `auth.ErrExpired`, `auth.ErrWrongAudience`.

`keyFor` is a function rather than a roster type so this package never imports the roster and can be tested without one.

- [ ] **Step 1: Write the failing tests**

```go
func TestMintVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	tok, err := Mint(priv, "urn:participant:alice", "urn:participant:bob", now, 5*time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	iss, err := Verify(tok, staticKey("urn:participant:alice", pub), "urn:participant:bob", now.Add(time.Second))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if iss != "urn:participant:alice" {
		t.Errorf("iss = %q", iss)
	}
}

// Each row is a way a token can be wrong. Every one of them must be refused,
// and the sentinel says which — the caller logs it and never echoes it.
func TestVerifyRefusals(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	_ = otherPub
	now := time.Unix(1_700_000_000, 0)
	good := func() string {
		tok, _ := Mint(priv, "alice", "bob", now, 5*time.Minute)
		return tok
	}

	for _, c := range []struct {
		name  string
		token string
		keys  func(string) (ed25519.PublicKey, bool)
		aud   string
		at    time.Time
		want  error
	}{
		{"not three segments", "a.b", staticKey("alice", pub), "bob", now, ErrMalformed},
		{"payload edited after signing", tamperPayload(t, good()), staticKey("alice", pub), "bob", now, ErrBadSignature},
		{"signed by a key the roster does not have", mustMint(t, otherPriv, "alice", "bob", now), staticKey("alice", pub), "bob", now, ErrBadSignature},
		{"issuer not in the roster", good(), noKeys, "bob", now, ErrUnknownIssuer},
		{"expired", good(), staticKey("alice", pub), "bob", now.Add(6 * time.Minute), ErrExpired},
		{"addressed to someone else", good(), staticKey("alice", pub), "carol", now, ErrWrongAudience},
	} {
		_, err := Verify(c.token, c.keys, c.aud, c.at)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// Alg confusion is the failure this hand-written verifier exists to avoid.
// The header must never select the algorithm — it is compared against the one
// value this connector accepts, and everything else is refused before any key
// is consulted.
func TestVerifyRefusesAnyAlgorithmButEdDSA(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_700_000_000, 0)
	for _, alg := range []string{"none", "HS256", "RS256", "eddsa", ""} {
		tok := mintWithHeaderAlg(t, priv, alg, "alice", "bob", now)
		if _, err := Verify(tok, staticKey("alice", pub), "bob", now); !errors.Is(err, ErrBadAlgorithm) {
			t.Errorf("alg %q: err = %v, want ErrBadAlgorithm", alg, err)
		}
	}
}
```

Helpers in the same file: `staticKey(id, pub)` returns a `keyFor` closure that answers for one id; `noKeys` answers for none; `mustMint` mints or fails the test; `tamperPayload` base64-decodes the middle segment, flips a claim, re-encodes, and leaves the signature untouched; `mintWithHeaderAlg` builds a token with an arbitrary `alg` in the header, signing the result with Ed25519 so the *only* thing wrong is the header.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Implement**

Three segments, base64url without padding, `{"alg":"EdDSA","typ":"JWT"}` as the header. Verify in this order, and the order is the design:

1. Split into exactly three segments, decode each. Anything else is `ErrMalformed`.
2. Decode the header and compare `alg` to the constant `"EdDSA"` with a plain string equality. Anything else is `ErrBadAlgorithm`. **Never** use the header's value to pick an algorithm or a key.
3. Read `iss` from the payload *only to look up a key*, and refuse with `ErrUnknownIssuer` if `keyFor` has none. Nothing else in the payload is trusted yet.
4. Verify the signature over `header.payload` with that key. Failure is `ErrBadSignature`.
5. Only now read `exp` and `aud`, and check them against `now` and `wantAud`.

Write step 3's constraint as a comment, because the next reader's instinct will be to parse the payload once at the top.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 5: Mutation check — prove the alg test discriminates**

Change the verifier to take the algorithm from the header instead of pinning it, and re-run.
Expected: `TestVerifyRefusesAnyAlgorithmButEdDSA` fails. If it still passes, the test is not pinning the property and must be fixed before continuing. Restore and confirm `git diff` is empty.

- [ ] **Step 6: Commit**

```bash
git add internal/auth/
git commit -m "feat: an EdDSA-only JWT this connector mints and verifies itself"
```

---

### Task 2: The roster

**Files:**
- Create: `internal/auth/roster.go`
- Test: `internal/auth/roster_test.go`

**Interfaces:**
- Produces: `auth.Roster` with method `KeyFor(id string) (ed25519.PublicKey, bool)`; `auth.LoadRoster(path string) (Roster, error)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadRosterReadsParticipants(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	path := writeRoster(t, `{"participants":[{"id":"alice","public_key":"`+
		base64.RawURLEncoding.EncodeToString(pub)+`"}]}`)

	r, err := LoadRoster(path)
	if err != nil {
		t.Fatalf("LoadRoster: %v", err)
	}
	got, ok := r.KeyFor("alice")
	if !ok || !got.Equal(pub) {
		t.Errorf("KeyFor(alice) = %v, %v", got, ok)
	}
	if _, ok := r.KeyFor("mallory"); ok {
		t.Error("KeyFor answered for an id that is not in the roster")
	}
}

// A connector that starts with no usable roster can verify nobody. Failing at
// startup is louder — and easier to diagnose — than starting fine and then
// refusing every counterparty.
func TestLoadRosterRejectsUnusableFiles(t *testing.T) {
	for name, body := range map[string]string{
		"not json":           `{`,
		"no participants":    `{"participants":[]}`,
		"missing id":         `{"participants":[{"public_key":"AAAA"}]}`,
		"missing key":        `{"participants":[{"id":"alice"}]}`,
		"key is not base64":  `{"participants":[{"id":"alice","public_key":"!!!!"}]}`,
		"key is wrong size":  `{"participants":[{"id":"alice","public_key":"AAAA"}]}`,
		"duplicate id":       duplicateIDRoster(t),
	} {
		if _, err := LoadRoster(writeRoster(t, body)); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	if _, err := LoadRoster(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing file loaded without error")
	}
}
```

`writeRoster` writes the body to a temp file and returns the path.
`duplicateIDRoster` builds a document with the same id twice and two valid keys — ambiguous trust is a configuration error, not a last-one-wins.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/ -run Roster -v`
Expected: FAIL, undefined `LoadRoster`.

- [ ] **Step 3: Implement**

Decode the document, require a non-empty `participants`, and for each entry require a non-empty `id` and a `public_key` that base64url-decodes to exactly `ed25519.PublicKeySize` bytes. Reject a duplicate `id`. Return a `Roster` holding a `map[string]ed25519.PublicKey`; `KeyFor` is a lookup.

Immutable after load: no reload path in this milestone, so nothing needs a lock.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/
git commit -m "feat: load the participant roster that says whose signatures count"
```

---

### Task 3: Configuration and startup

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`
- Modify: `cmd/dsbox/main.go`

**Interfaces:**
- Produces: `config.Config.ParticipantKey`, `config.Config.RosterPath`, `config.Config.RequireAuth *bool` — a pointer so an omitted key is distinguishable from an explicit `false`, defaulted to `true` at load.

- [ ] **Step 1: Write the failing tests**

```go
func TestRequireAuthDefaultsToTrue(t *testing.T) {
	cfg, err := Load(minimal(""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AuthRequired() {
		t.Error("authentication is off by default")
	}
}

// Turning authentication off is a development affordance. An operator who has
// not declared this a development instance may not also declare that anyone
// may talk to it.
func TestRequireAuthFalseNeedsDevMode(t *testing.T) {
	if _, err := Load(minimal("require_auth: false\n"), env(nil)); err == nil {
		t.Error("require_auth: false loaded without dev_mode")
	}
	if _, err := Load(minimal("require_auth: false\ndev_mode: true\n"), env(nil)); err != nil {
		t.Errorf("require_auth: false with dev_mode: true failed to load: %v", err)
	}
}

// With authentication on, the two files it needs are required. Loading
// without them would produce a connector that cannot verify or sign, and the
// first symptom would be every request failing at runtime.
func TestAuthRequiresKeyAndRoster(t *testing.T) {
	if _, err := Load(minimal("participant_key: /k\n"), env(nil)); err == nil {
		t.Error("loaded with a key but no roster")
	}
	if _, err := Load(minimal("roster: /r\n"), env(nil)); err == nil {
		t.Error("loaded with a roster but no key")
	}
}
```

`minimal` already exists and appends to a minimal valid document. Note that `minimal("")` must keep loading, so the new required-ness applies only when `AuthRequired()` is true — and since it defaults true, `minimal` itself needs the two new keys added to its base document. Update `minimal`, and expect the existing config tests to keep passing unchanged.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: FAIL, undefined fields.

- [ ] **Step 3: Implement**

Add the three fields with doc comments carrying the spec's reasoning. `AuthRequired()` returns `true` when `RequireAuth` is nil or `*RequireAuth`. In `validate`: reject `require_auth: false` unless `DevMode`; when `AuthRequired()`, require both `ParticipantKey` and `RosterPath`.

- [ ] **Step 4: Wire startup**

In `cmd/dsbox/main.go`, when `AuthRequired()`: read and parse the Ed25519 private key from `ParticipantKey`, load the roster, and fail startup on either error. When it is false, log a warning naming every route left open — one line per startup, not per request.

- [ ] **Step 5: Run the tests and commit**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/config/ cmd/dsbox/
git commit -m "feat: configure the key and roster authentication needs"
```

---

### Task 4: Enforcement on the DSP listener

**Files:**
- Modify: `internal/dsp/router.go`
- Create: `internal/dsp/auth_middleware.go`
- Test: `internal/dsp/auth_middleware_test.go`

**Interfaces:**
- Consumes: `auth.Verify`, `auth.Roster`.
- Produces: `dsp.RequireParticipant(r auth.Roster, self string, next http.Handler) http.Handler`; the authenticated issuer available to handlers via request context, accessor `dsp.issuerFrom(r *http.Request) string`.

- [ ] **Step 1: Write the failing tests**

```go
// Every DSP route is closed. The list is derived from the mux rather than
// hand-written, so a route added later without auth fails this test instead
// of shipping open.
func TestEveryDSPRouteRefusesAnAnonymousRequest(t *testing.T) {
	for _, rt := range dspRoutesForTest() {
		if rt.path == "/.well-known/dspace-version" {
			continue
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", rt.method, rt.path, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s %s: challenge = %q, want Bearer", rt.method, rt.path, got)
		}
	}
}

// The version endpoint is how a counterparty learns what to speak before it
// has any context, and it discloses only a protocol version.
func TestVersionEndpointStaysOpen(t *testing.T) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/dspace-version", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
}

// The rejection tells the caller that a credential was missing or invalid and
// nothing else. Which of the six ways it was wrong goes to the log, where an
// operator can see it and a prober cannot.
func TestRejectionDoesNotExplainWhy(t *testing.T) {
	for _, tok := range []string{"", "Bearer garbage", "Bearer " + expiredToken(t), "Bearer " + wrongAudienceToken(t)} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader("{}"))
		if tok != "" {
			req.Header.Set("Authorization", tok)
		}
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		for _, leak := range []string{"expired", "audience", "signature", "issuer"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Errorf("%q: body leaks %q: %s", tok, leak, body)
			}
		}
	}
}

func TestValidTokenIsAdmitted(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", VersionPath+"/catalog/request", strings.NewReader(catalogRequestBody))
	req.Header.Set("Authorization", "Bearer "+validToken(t))
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("a valid token was refused: %s", rec.Body)
	}
}
```

`dspRoutesForTest` returns the method and a concrete path for every route the router mounts, with `{id}` filled by any string — the point is reaching the middleware, not the handler.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dsp/ -run 'DSPRoute|VersionEndpointStaysOpen|RejectionDoes|ValidTokenIsAdmitted' -v`
Expected: FAIL — every route answers as it does today, without a `401`.

- [ ] **Step 3: Implement the middleware**

Read `Authorization`, cut the `Bearer ` prefix case-insensitively the way `cutBearerPrefix` already does in `internal/mgmt`, and call `auth.Verify` with the roster's `KeyFor`, this connector's participant id as `wantAud`, and `time.Now()`. On any error: log the sentinel and the remote address, set `WWW-Authenticate: Bearer`, and write `401` with a body that says only that a valid credential is required.

On success, put the issuer in the request context and call through.

- [ ] **Step 4: Wire the router**

`NewRouter` takes the roster and this connector's participant id, and wraps the mux — every route except the version endpoint. Mount the version endpoint on an outer mux so the wrapping is structural rather than a path comparison inside the middleware: a path comparison is a list someone has to remember to update, and this milestone has exactly one exemption.

When `AuthRequired()` is false, `NewRouter` skips the wrap entirely rather than installing a middleware that always passes — a disabled check should be absent, not silently true.

- [ ] **Step 5: Run the tests to verify they pass, then verify the whole suite**

Run: `go test ./internal/dsp/ -v`, then `gofmt -l . && go vet ./... && go test -race ./...`

Existing `dsp` tests call handlers directly rather than through the router, so they should be unaffected. Any that go through `NewRouter` need a token.

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/
git commit -m "feat: refuse an unauthenticated request on every DSP route"
```

---

### Task 5: Outbound tokens

**Files:**
- Modify: `internal/dsp/callback.go`, `internal/dsp/negotiation_client.go`, `internal/dsp/transfer_client.go`
- Modify: `internal/store/store.go` (counterparty participant id on both provider-role tables)
- Modify: the handlers that create provider-role rows
- Test: `internal/dsp/callback_test.go` and the client tests

**Interfaces:**
- Produces: every outbound DSP request carries `Authorization: Bearer <freshly minted token>`; `store.Negotiation.CounterpartyID` and `store.TransferProcess.CounterpartyID`.

- [ ] **Step 1: Write the failing tests**

```go
// Every outbound call carries a token, and the token says who it is for.
// The TCK cannot catch a mistake here — its mock endpoints accept whatever
// arrives without inspecting the header — so this is the only evidence.
func TestOutboundCallsCarryAMintedToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pushCallback(srv.URL+"/x", map[string]string{"@type": "X"})

	raw, ok := strings.CutPrefix(got, "Bearer ")
	if !ok {
		t.Fatalf("Authorization = %q, want a Bearer token", got)
	}
	iss, err := auth.Verify(raw, testRosterKeyFor, "urn:participant:counterparty", time.Now())
	if err != nil {
		t.Fatalf("the token this connector sent does not verify: %v", err)
	}
	if iss != "urn:participant:self" {
		t.Errorf("iss = %q", iss)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dsp/ -run OutboundCallsCarryAMintedToken -v`
Expected: FAIL — `Authorization` is empty.

- [ ] **Step 3: Record who the counterparty is**

Add `counterparty_id` to `negotiations` and `transfer_processes`, written from the authenticated issuer when the row is created — `dsp.issuerFrom(r)` from Task 4. That is the honest source: the row exists because that party made an authenticated request.

Consumer-role rows already know the counterparty from the initiate call's `providerId`; store it in the same column name so the outbound path reads one field regardless of role.

- [ ] **Step 4: Mint on every outbound call**

`pushCallback`, `sendInitialRequest`, `sendTransferRequest`, and every `send*` helper take the audience and attach the header. Mint per call — one Ed25519 signature is cheap, and a cache is a refresh bug waiting to happen.

- [ ] **Step 5: Run the tests to verify they pass, then the whole suite**

Run: `gofmt -l . && go vet ./... && go test -race ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/dsp/ internal/store/
git commit -m "feat: sign every outbound DSP call"
```

---

### Task 6: `dsops`, the harness, and the gate

**Files:**
- Create: `cmd/dsops/main.go`
- Test: `cmd/dsops/main_test.go`
- Modify: `test/tck/run.sh`, `test/tck/compose.yaml`, `test/tck/dsbox.yaml`, `.gitignore`
- Modify: `README.md`

**Interfaces:**
- Produces: `dsops keygen -out <path>` writing a PEM private key and printing the base64url public key; `dsops token -key <path> -iss <id> -aud <id> [-ttl 5m]` printing a signed token.

- [ ] **Step 1: Write the failing test**

```go
// The harness and the demo both depend on these two commands, so their
// contract is a test rather than a README line.
func TestKeygenThenTokenVerifies(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "k.pem")

	pubB64 := runDsops(t, "keygen", "-out", keyPath)
	pub, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil {
		t.Fatalf("keygen printed an unusable public key: %v", err)
	}

	tok := strings.TrimSpace(runDsops(t, "token", "-key", keyPath, "-iss", "alice", "-aud", "bob"))
	if _, err := auth.Verify(tok, staticKey("alice", ed25519.PublicKey(pub)), "bob", time.Now()); err != nil {
		t.Fatalf("the token dsops minted does not verify: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails, then implement**

Two subcommands, `flag` package, no framework. `keygen` refuses to overwrite an existing file — a silently replaced key is a locked-out connector.

- [ ] **Step 3: Provision identities in the harness**

In `run.sh`, after the connector is up and the agreements are seeded — **not** before `docker compose`, because a cold build would spend the token's five minutes before the suite starts:

1. `dsops keygen` for the connector and for the TCK's participant id, into a gitignored directory.
2. Write `roster.json` with both public keys.
3. `dsops token` for the TCK's identity, audience the connector's participant id.
4. Write `config.generated.properties` — the tracked file plus
   `dataspacetck.dsp.connector.http.headers.authorization=Bearer <token>`.

Steps 1 and 2 have to happen before the connector starts, since it loads the roster at startup; only the token must be minted late. Split the provisioning accordingly rather than moving it all early.

`compose.yaml` mounts the generated properties file and the roster and key. `.gitignore` gains the generated directory. No key material is ever committed.

- [ ] **Step 4: Run the TCK**

Run: `make tck`
Expected: `64 required tests passed, 0 results outside the gate, 1 known exemption(s)`.

A failure here is a real defect: the connector log at `tck-connector.txt` will show which sentinel the middleware logged.

- [ ] **Step 5: Prove the harness is actually authenticating**

Blank the authorization line in the generated properties and re-run.
Expected: the suite fails, with `401`s in the connector log. If it still passes, the middleware is not wired to the routes the TCK calls, and Task 4 is incomplete regardless of what its unit tests say.

Restore, re-run, confirm 64 of 65.

- [ ] **Step 6: Update the README and commit**

The status table gains a line saying connector-to-connector authentication is in place, and the honest caveats: no `did:web` resolution, no signed roster, replay possible within the token's five-minute lifetime.

```bash
git add cmd/dsops/ test/tck/ .gitignore README.md
git commit -m "feat: authenticate the TCK harness with a minted token"
```

---

## Done

- Every DSP route except the version endpoint refuses an anonymous request with `401` and a challenge.
- Every outbound DSP call carries a freshly minted token whose `aud` names the counterparty.
- `make tck` reports 64 of 65 with 0 results outside the gate, and fails when the harness's credential is removed.
- No key material is committed; the harness generates its own.
- `gofmt -l .` empty, `go vet ./...` clean, `go test -race -count=2 ./...` green.
