# Measuring this connector against its own goal

`docs/milestone-sequence.md` argues which milestone goes first.
`docs/follow-ups.md` records small gaps not worth blocking a milestone on.
Neither one measures the whole project against the sentence it promises in
`README.md` and `CLAUDE.md`, and that measurement is what this document is.

It is a measurement, so it goes stale. Re-run it rather than trusting it.

## The goal, as stated

> A minimum **operational** dataspace: clone, run, working in ten minutes, no
> consulting required.

`README.md`'s Scope section declares four things in: the four core DSP
protocols, `did:web` identity with an operator-signed roster, a management
API, and an embedded web UI.

## How this was checked

Every claim below was verified against the repository by four independent
readers instructed to refute rather than confirm. Claims that survived carry a
file and line. Three claims did not survive and are recorded under
"Corrections" rather than deleted, because a gap analysis that quietly drops
its own errors cannot be audited either.

## The four promises

The obvious decomposition — one axis per Scope item — was tried first and
discarded. It fails twice: "declared scope" is a scoreboard over the other
axes rather than an axis of its own, and nothing in it owns the question a
*dataspace* actually turns on, which is whether a second organization can
participate. What follows is organized by promise, and each promise is
written so that it can be declared done.

### P1. It speaks the protocol — verified in one direction only

TCK 65 of 65, every suite gated, none exempt. That result is real and it is
the strongest evidence in the repository.

It is also narrower than the Scope sentence reads. There is no outbound
catalog client: `internal/dsp/` contains `negotiation_client.go` and
`transfer_client.go` and nothing else. `CatalogRequestMessage` appears only in
`catalog_handler.go`, the receiving side. The version document is likewise
served and never requested. So of the four core protocols, two are
implemented in the provider role only, and the TCK cannot see this because it
plays the consumer in those suites.

The practical consequence is larger than the missing code. Without discovery,
a consumer has to learn `datasetId` and `offerId` out of band, and the offer
identifier is derived by a convention private to this implementation —
`offerIDSuffix = "#offer"` (`internal/dsp/catalog.go:41`), which
`demo/run.sh:111` hardcodes as `urn:dataset:sample#offer`. Combined with an
authentication profile that DSP does not specify and this project invented
(roster plus self-signed JWT, `DECISIONS.md` §10), **the only counterparties
this connector can transact with today are another `dsbox` and the TCK.**

Done looks like: a catalog client, and a coverage table in `README.md` that
names the role each protocol is implemented in.

### P2. It moves the right bytes, and keeps no record that it did

`make demo` moves a real file and diffs it, which the TCK never does. That
evidence stands. What it does not cover is size.

**The binding limit is on the consumer and is undocumented.**
`pullTransferData` issues the data fetch through `callbackHTTPClient`
(`internal/dsp/transfer_consumer_handler.go:334`), whose `Timeout` is
`10 * time.Second` (`internal/dsp/callback.go:26`). `http.Client.Timeout`
covers the body read, so any transfer that cannot finish in ten seconds is
severed — roughly 12 MB on a 10 Mbit/s link. That client's doc comment
describes callback pushes only; nothing says a data pull reuses it. The 30
second `WriteTimeout` in `cmd/dsbox/main.go` is the *looser* of the two and
is already flagged in a comment there.

Nothing in the repository can catch this. `make demo` moves files of a few
kilobytes and the TCK moves none, so the product's central claim — that it
moves files — is standing without a measurement, and the means to measure it
does not exist here.

**A completed pull leaves almost no trace.** There is no expected size and no
digest; resumption validates only the starting offset, so a counterparty file
replaced with a *longer* one is appended to rather than refused. The file is
closed and renamed without `Sync()`. Nothing is written to the database, so
`GET /agreements` — three routes are all the management API has — cannot
answer whether data arrived; one `slog.Info` line is the entire record. The
transfer state machine is driven by configured timers and never by the
arrival of bytes, so a failed pull and a successful one are indistinguishable
in storage. And `handleData`'s success path logs nothing at all, so a
provider cannot say who collected its data. §27 went to real trouble to
obtain a verified identity; the success path does not record it.

There is also no operator retry: a doc comment says "an operator can ask
again", and no endpoint exists to ask with. With the ten-second cap above,
a large file is not slow — it is permanently unfinishable.

Done looks like: a transfer of measured size arriving with its integrity
checked, that fact recorded in the database and queryable, and both sides
logging the identity they served or fetched from.

### P3. A second organization can participate — the weakest promise

This is the promise "dataspace" makes, and no milestone owns it.

- **Revocation is not slow; it is undetectable.** `rosterDocument` is
  `{participants, signature}` with no version, issue time, expiry, or
  dataspace identifier (`internal/auth/roster.go`). The signature covers only
  the payload, so a superseded roster verifies forever and a rollback to one
  that still lists a removed participant cannot be detected. §9 recorded this
  as "revocation is only as fast as propagation", which is weaker than what
  the code does. A monotonic `version` inside the signed payload closes it
  cheaply.
- **Clock skew has zero tolerance.** `Verify` checks `now.Unix() >= c.Exp`
  and nothing else (`internal/auth/token.go:136`) — no leeway, no `nbf`,
  and `iat` is minted but never checked. Credentials live five minutes, so
  two participants whose clocks differ by more than that fail every request,
  and the caller receives an unexplained 401 because `refuse` deliberately
  hides the reason. No document states an NTP requirement.
- **The bootstrap ends in an admitted unsolved step.** `config.example.yaml`
  says it plainly: distributing `roster_signer`'s public half "is still an
  out-of-band problem". `make demo` does not disprove this — `demo/run.sh`
  generates both participants' keys *and* the roster signing key on one
  machine, which is the step being punted.
- **The first thing a reader tries fails.** Two `dsbox` instances on
  `127.0.0.1` cannot negotiate: `dev_mode` relaxes the `https` requirement
  but does not reach `isDisallowedCallbackIP`, so initiate returns 400 with
  no hint. `docs/follow-ups.md:119` already records this *and already names
  the ten-minute promise* — filed in the document reserved for gaps not worth
  blocking a milestone on.
- **The example config omits the field that makes the product work.**
  `source_file` is what causes a dataset to be served, and it appears zero
  times in `config.example.yaml` (`internal/config/config.go:156` defines
  it). `consumer_transfer_policies` is missing there too. The example config
  is the only onboarding document that exists, and it does not describe how
  to turn the product on.
- **"Ten minutes" has never been defined.** The phrase appears five times
  across `README.md`, `CLAUDE.md`, `DECISIONS.md`, and `docs/follow-ups.md`,
  always as motivation, never as a condition anything tests. Whether
  "working" means the process boots or two independent participants exchanged
  a file is not written down, so the claim cannot be true or false.

Done looks like: two operators who have never met, working from the
documentation alone, bootstrap, revoke, and discover.

### P4. A stranger can run it and adopt it — decided but not built

- No metrics, no pprof, no expvar, no `/metrics` route anywhere.
- Log level is fixed: `slog.NewJSONHandler(os.Stdout, nil)` is the only
  handler constructed, and no config field or environment variable reaches it.
- No rate limiting on the public listener, which performs an Ed25519
  verification per request.
- No backup or `VACUUM` story in code or documentation, against a schema with
  no delete path anywhere by design (§25.3) and an uncleaned `.partial-*`
  directory. Monotonic growth with no remediation.
- **The binary has no version.** No `-version` flag, no `ReadBuildInfo`
  stamp, no version field in the boot log. An operator cannot say what is
  running and a bug report cannot name a build.
- **The DSP version is a compile-time constant.** `VersionPath = "/2025-1"`
  is baked into every route and into callback addresses. The version metadata
  endpoint exists to advertise several versions; this connector cannot serve
  two. No decision records that assumption.
- **Migration is forward-only with no downgrade detection**, so an older
  binary opens a newer database and dies on first query. §23.1's trade-off
  covers a forgotten check step, not a rollback.
- **§19 declares four test layers and two do not exist.** Playwright depends
  on the absent UI. In-process multi-connector integration has no test at all
  and cannot be written as the code stands: `mintOutboundCredential` is a
  package-level variable (`internal/dsp/callback.go:208`), so two routers in
  one process share one identity. §19's accepted trade-off is a trade-off
  about a layer that was never built.
- **The web UI does not exist and the gap is larger than Scope suggests.**
  Zero `.html`, `.css`, or `.js` files; zero `go:embed` directives. Yet §7
  settles the architecture — "The Svelte UI is built to static assets and
  embedded via `go:embed`" — and uses it inside the one-binary argument, and
  §19 hangs a test layer on it. This is documentation asserting a fact about
  the code that is not true, not merely an unbuilt feature.
- **`SECURITY.md` is absent while `docs/follow-ups.md` publishes a working
  exploit** in prose, on a public repository, with no coordinated disclosure
  channel. This costs something every day it stands.
- **The FSL clock has not started.** `LICENSE.md` converts each version two
  years after it is made available and §16 says tags carry licensing meaning;
  there are no tags and no version in the binary, so `README.md`'s conversion
  promise has no subject. An adopter's counsel cannot answer the question.
  This is a licensing gap, not an ops gap.
- No `NOTICE`/third-party attribution for the statically linked dependencies,
  and the supply-chain posture is inconsistent: the TCK image is pinned by
  digest while `Dockerfile` uses floating tags, with no `govulncheck` or SBOM.

Done looks like: a versioned artifact, a failure a stranger can diagnose, and
a licensing question counsel can answer.

## Two compositions that no single entry records

Both are made of parts already written down separately. Neither composition
is written down anywhere, which is the point: this repository's per-milestone
honesty does not compose by itself.

**1. The initiate hooks are a cross-connector authentication bypass, and §32
does not blunt it.** §32.3 already names the vector — "an impersonation
primitive against a third participant" — so the finding is not that it exists.
The finding is what it composes with.

`mintOutboundCredential` produces a token signed by this connector's real key
with `aud` set to a caller-chosen `providerId`, and
`negotiation_client.go:44` writes it as the `Authorization` header on a
request sent to a caller-chosen `connectorAddress`. The caller therefore
receives the token. §28 declined replay defense, and `claims` carries no
`jti`, no nonce, and no binding to method, path, or body — so that token can
be presented, repeatedly, to the victim it names. The victim verifies issuer
against its own roster and audience against its own id, both of which pass.
Nothing rate-limits minting a fresh one.

§32's ownership checks give **zero** mitigation here. They ask whether a
caller is the verified issuer stored on an existing row; an attacker holding
a valid `dsbox` token verifies identically to `dsbox`, and a *new* exchange
has no prior row to check. `handleContractRequest` records
`CounterpartyID: issuerFrom(r)` honestly — as `dsbox` — and every later
check on that row then passes for the attacker.

So three separately-filed items compose: §32.3's unvalidated audience, §28's
declined replay defense, and an absence of rate limiting that is filed
nowhere at all.

**2. Tagging a release is a legal act, and doing it before the above is
fixed is irreversible.** §16 records that tags start the FSL clock. A first
tag cut today permanently records, as a released version, the item
`docs/follow-ups.md` calls the highest-severity entry in the file. Nothing in
the repository connects the release decision to the security decision. It
should.

## The order, and why

The sequencing rule in use — "do the step that makes the next one safe,
first" — does not survive contact with P3 and P4, because "safe" says nothing
about a metrics endpoint or a licence. That is why `docs/milestone-sequence.md`
has no entry for any of them.

The better rule is already in that document's own opening paragraph: the
order is driven by **what can still verify the work**. "Safe first" is the
special case of it that holds for security work, promoted to a slogan; the
promotion is what pushed three of the four promises out of the document.

0. **Give the artifact an identity, and stop the documentation drift.** A
   `-version` flag and a build stamp, `SECURITY.md`, `source_file` and
   `consumer_transfer_policies` in `config.example.yaml`, role columns in
   `README.md`'s protocol table. Cheap, blocks nothing, and everything below
   needs to name a build. `SECURITY.md` is charging rent today.

1. **The data path** — split the pull off `callbackHTTPClient`, verify what
   arrives, `Sync()` it, record it, make it queryable, log the served
   identity. *First because nothing can verify it today.* A milestone that
   has to build its own evidence is the one that decays if deferred, and this
   defect breaks the product's central claim for its first real user. The
   authorization work below wants a transfer that actually completes
   underneath its demo evidence.

2. **The initiate hooks — but the question the sequence poses is the wrong
   one.** `docs/milestone-sequence.md` frames the milestone as "what an
   initiate call may name when the roster does not list it", and calls the
   TCK "worse than neutral" because a naive roster check loses 30 of 65
   results. That constraint dissolves: the harness reaches these hooks
   through configurable URL properties, `dsops` already takes `-ttl`
   (`cmd/dsops/main.go:91`) so a long-lived static credential is available,
   and `demo/run.sh` already calls them with a token the operator minted for
   themselves. The prior question is **who may call them at all** — that is,
   which listener they belong on. Moving them to the management listener
   makes validating `providerId` unnecessary rather than difficult, deletes
   the `counterparty_id` asymmetry this repository says it must explain in
   seven places, and closes composition 1 structurally. It is also where
   P4's "the management API has only three routes" turns out to be the same
   work seen from the other side.

3. **The roster as a versioned, expiring artifact**, plus clock leeway.
   *Before* any onboarding document is written, or the document describes a
   procedure about to be replaced.

4. **Discovery** — a catalog client. After 2, because it mints outbound
   credentials whose audience 2 decides; before 5, because a ten-minute
   script that includes "obtain the offer id out of band" is not worth
   measuring.

5. **Define and measure "ten minutes"** in CI. After 1–4, which change the
   steps being counted.

6. **Release and tags.** Not before 2. See composition 2.

7. **Observability, rate limiting, and the UI.** Last because nothing depends
   on them, and because rate limiting gets cheaper once 2 removes the
   amplifier.

## What this does not decide

It does not decide whether the UI stays in scope. §7 and §19 both lean on it,
so removing it is an edit to two decisions, not a deletion from a list —
and either finishing it or dropping it is better than the present state,
where the documentation asserts something about the code that is false.

It does not reopen the decisions whose trade-offs are already recorded and
still hold: restart-on-roster-change (§9), native-execution-first (§17), the
deliberately narrow management surface (§25.3), or the absence of a general
policy engine (§14). An omission with a recorded trade-off is a decision, not
a gap, and this document counts it as one only where the trade-off's own
terms have since stopped holding.

## Corrections to this analysis's first pass

- **The management API has three routes, not four.**
  `GET /.well-known/dspace-version` is a DSP protocol endpoint on the public
  listener, not a management route.
- **The 30 second `WriteTimeout` is not a silent limit.** `cmd/dsbox/main.go`
  carries an explicit comment predicting exactly this. The silent one is the
  consumer's ten seconds.
- **"Restart required" is one decision, not several gaps.** Roster (§9),
  token rotation (§11), and dataset changes (§22.1) apply a single recorded
  principle. The roster's real defect is revocation, not the restart.
- **"Protocol: essentially done" was wrong in the optimistic direction.** See
  P1.
- **The initiate hooks have a fourth residual, not three.** An agreement
  concluded while `require_auth` was off has no recoverable owner, and that
  flag is the migration path this repository documents. Fixing the hooks does
  not close it.
