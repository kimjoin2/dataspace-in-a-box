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

Every claim below was re-read against the source adversarially — the aim was
to refute each one, not to confirm it — and what survived carries a file and
line so you can check it rather than trust this. What did not survive is under
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
`demo/run.sh` hardcodes as `urn:dataset:sample#offer` in each of its
negotiate calls. Combined with an authentication profile that DSP does not
specify and this project invented (roster plus self-signed JWT,
`DECISIONS.md` §10), **the only counterparties this connector can transact
with today are another `dsbox` and the TCK.**

Done looks like: a catalog client, and a `README.md` table that names the
role for every protocol. It already names roles for `CN`/`CN_C` and
`TP`/`TP_C` and omits them for `MET` and `CAT` — which is exactly the two rows
where a role is missing in the code, so the table reads as symmetric at the
one place it is not.

### P2. It moves the right bytes, and keeps no record that it did

> **Superseded in part, 2026-08-24.** Seven of this section's findings are no
> longer true of the code, and `README.md` now links here from a paragraph
> that says the opposite — so this notice is the correction rather than a
> reader's own re-run. **Four were closed by the data-path correctness
> milestone (`DECISIONS.md` §33, "Plan A")**: the pull no longer goes through
> `callbackHTTPClient` or inherits its ten-second timeout (it has its own
> client, `dataPullHTTPClient`, bounded by progress rather than elapsed
> time — §33.1, §33.3); an expected size *is* recorded and compared
> (`expected_bytes`, §33.5); the download is `Sync`ed before the rename; and
> something is written to the database on every pull. **Three more were
> closed by the recording-and-exposure milestone (§34)**: the management API
> gained `GET /transfers`, which is exactly the "no way to ask whether data
> arrived" this section names; a failed pull and a successful one are now
> distinguishable in storage
> (`data_completed_at` and `data_error`, §34.1); and `handleData`'s success
> path logs the identity it served (§34.5).
>
> **The `file:line` citations below have rotted with the text.**
> `transfer_consumer_handler.go:334` now lands inside `parseContentRange`,
> and `callback.go:26` still points at a real ten-second timeout that the
> data path no longer uses. Treat every line number in this section as
> unverified. The management API's route count below has rotted the same way,
> twice over — `GET /transfers` (§34.4) and the initiate hooks (§35.5) both
> moved it — and it is left standing for the same reason the rest of this
> prose is: `internal/mgmt/router.go` is the only statement of it that cannot
> go stale.
>
> **What survives, and is still the point of this section:** size has no
> measurement — `make demo` moves kilobytes and the TCK moves none — and
> there is still no operator retry endpoint. The "Done looks like" paragraph
> at the end of this section is a fair statement of what is still missing on
> the retry half. The prose below is left unedited so the measurement can be
> audited against what was actually found.

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
  cheaply. *(2026-08-25: closed, as `DECISIONS.md` §36 — and the fix this
  bullet proposed was half of it. `version` alone is a local anti-rollback
  memory: it stops this connector being handed an older document, and reaches
  no other participant. What actually bounds revocation is the `expires_at`
  that shipped beside it, because past that instant a superseded roster stops
  verifying wherever it is held, including on a connector nobody restarted.
  Both are inside the signature. The bound is real only because
  `maxRosterLifetime` caps how far ahead the expiry may sit — without a cap it
  would be whatever the operator typed, which was then the same shape as a
  token lifetime the issuer picked and the verifier never bounded
  (`auth.Verify` compared `exp` and nothing else; `credentialTTL` lives at the
  minting site, and still does). The comparison holds and the tense no longer
  does: `maxCredentialLifetime` gave the token the same kind of cap on
  2026-08-26, `DECISIONS.md` §37. No dataspace identifier was added; nothing
  needed one.)*
- **Clock skew has zero tolerance.** `Verify` checks `now.Unix() >= c.Exp`
  and nothing else (`internal/auth/token.go`) — no leeway, no `nbf`,
  and `iat` is minted but never checked. Credentials live five minutes, so
  two participants whose clocks differ by more than that fail every request,
  and the caller receives an unexplained 401 because `refuse` deliberately
  hides the reason. No document states an NTP requirement.
  *(2026-08-26: closed, as `DECISIONS.md` §37. `Verify` now compares against
  `now` less a minute of leeway, so a lagging clock costs a minute rather
  than every request, and it separately refuses an `exp` sitting more than an
  hour ahead of its own clock — the half this bullet did not ask for, and the
  one that closes the opposite direction, where a clock running ahead used to
  extend its own credential's life with nothing capping it. `nbf` was
  declined for the reason §36.9 gives. `iat` is still minted and still never
  read, deliberately: §37.2 bounds `exp - now`, because `iat` and `exp` are
  both the issuer's to choose. The line number this bullet cited was accurate
  when it was written and had already gone stale before this milestone
  existed: the expiry check moved off line 136 at `6a2f4c8`, the roster
  milestone's own documentation commit, which lengthened the comment above
  the sentinel block; the constants this milestone added moved it again. It
  is removed rather than corrected, since the next edit above `Verify` would
  stale it a third time. The unexplained 401 is unchanged and is still
  deliberate. The NTP sentence is also still true — §37 records the
  assumption as accepted rather than closed.)*
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
- **"Ten minutes" has never been defined.** "ten minutes" appears once each
  in `README.md`, `CLAUDE.md`, `DECISIONS.md`, and `docs/follow-ups.md`, and
  `DECISIONS.md` reaches for "the ten-minute promise" twice more — always as
  motivation, never as a condition anything tests. Whether
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
- ~~**The binary has no version.**~~ *Closed.* A `-version` flag reports the
  short revision, marked when the tree was dirty, and the boot log carries
  the same value.
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
- **Coordinated disclosure was missing and is now the one P4 item closed.**
  `docs/follow-ups.md` publishes a working exploit in prose on a public
  repository; until `SECURITY.md` landed there was no private channel to
  report anything back. That file, and GitHub private vulnerability reporting
  behind it, were added immediately after this document first named the gap.
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
does not blunt it.**

> **Closed, 2026-08-24, `DECISIONS.md` §35.** This composition was right and
> it was acted on. §35.1 moves both hooks to the management listener, which
> removes the primitive rather than mitigating it: the composition needs an
> untrusted caller to choose the audience, and after the move the only caller
> is the operator. §35.2 additionally refuses a `providerId` the roster does
> not list. Nothing about §28 or about rate limiting changed — what changed is
> that neither is reachable through this any more. **What survives is the
> narrower version §35.2 could not close**: being in the roster is not the
> same as being the participant at `connectorAddress`, so an operator who
> points an initiate call at the wrong connector still hands a signed
> credential to whoever is there. The sequence that demonstrates it now begins
> with the operator being wrong rather than with a request an outsider makes,
> which is why `SECURITY.md` no longer names this as the sharpest item open.
> Closing it means binding an address to a participant in the roster, which is
> ordered item 3 below.
>
> *(2026-08-25: not item 3. That milestone shipped as `DECISIONS.md` §36 and
> declined this on scope — it changed the roster document's lifecycle, while
> an address changes what an entry means and would make every address change
> a re-signed roster and a fleet-wide restart. It moves to ordered item 4,
> discovery, where something actually consumes an address. This residual
> stays open in the meantime.)*

§32.3 already names the vector — "an impersonation primitive against a third
participant" — so the finding is not that it exists. The finding is what it
composes with.

`mintOutboundCredential` produces a token signed by this connector's real key
with `aud` set to a caller-chosen `providerId`, and
`negotiation_client.go:44` writes it as the `Authorization` header on a
request sent to a caller-chosen `connectorAddress`. The caller therefore
receives the token. §28 declined replay defense, and `claims` carries no
`jti`, no nonce, and no binding to method, path, or body — so that token can
be presented, repeatedly, to the victim it names. The victim verifies issuer
against its own roster and audience against its own id, both of which pass.
Nothing rate-limits minting a fresh one.

§32's ownership checks give **zero** mitigation here, and the reason is
structural rather than a flaw in them: they compare a caller against the
verified issuer already stored on a row, and a token that verifies as `dsbox`
is indistinguishable from `dsbox` at every one of them. §32.3 scopes itself to
that comparison and says so.

So three separately-filed items compose: §32.3's unvalidated audience, §28's
declined replay defense, and an absence of rate limiting that is filed
nowhere at all.

**2. Tagging a release is a legal act, and doing it before the above is
fixed is irreversible.** *(2026-08-24: the specific entry this named is gone —
`docs/follow-ups.md` deleted it when §35 closed it. The ordering constraint
below is unaffected, because it is about the act of tagging rather than about
which gap is open at the time.)* §16 records that tags start the FSL clock. A
first tag cut today permanently records, as a released version, whatever
`docs/follow-ups.md` and `SECURITY.md` still carry. When this was first
written nothing in the repository connected the release decision to
the security decision; `SECURITY.md` now does, and names the first tag as the
point where publishing unfixed gaps stops being defensible. The ordering
constraint below follows from that, and it is the reason the release item sits
where it does.

## The order, and why

The sequencing rule in use — "do the step that makes the next one safe,
first" — does not survive contact with P3 and P4, because "safe" says nothing
about a metrics endpoint or a licence. That is why `docs/milestone-sequence.md`
has no entry for any of them.

The better rule is already in that document's own opening paragraph: the
order is driven by **what can still verify the work**. "Safe first" is the
special case of it that holds for security work, promoted to a slogan; the
promotion is what pushed three of the four promises out of the document.

0. **Give the artifact an identity, and stop the documentation drift.**
   *Done.* `SECURITY.md` and private vulnerability reporting; a `-version`
   flag reporting the short revision and carried in the boot log;
   `source_file` and `consumer_transfer_policies` documented in
   `config.example.yaml`; `MET` and `CAT` marked served-only in `README.md`'s
   protocol table, and that section's claim about the transfer size limit
   corrected to name the consumer's ten seconds. Cheap, blocked nothing, and
   everything below can now name a build.

1. **The data path** — split the pull off `callbackHTTPClient`, verify what
   arrives, `Sync()` it, record it, make it queryable, log the served
   identity. *First because nothing can verify it today.* A milestone that
   has to build its own evidence is the one that decays if deferred, and this
   defect breaks the product's central claim for its first real user. The
   authorization work below wants a transfer that actually completes
   underneath its demo evidence.

2. **The initiate hooks — but the question the sequence poses is the wrong
   one.** *Done, 2026-08-24, `DECISIONS.md` §35.*
   `docs/milestone-sequence.md` frames the milestone as "what an
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
   P4's complaint about the size of the management API turns out to be the
   same work seen from the other side.

   **What the milestone did with this item.** It adopted the framing —
   §35.1 moves both hooks to the management listener, and what went away is
   the asymmetry in *authorization weight*: after §35.3 a consumer-role
   counterparty is something an inbound message is compared against, which is
   what a provider-role one already was. The asymmetry in **provenance**
   remains, and is still explained on purpose in `refuseIfNotParty`
   (`internal/dsp/auth_middleware.go`), the `CounterpartyID` doc comments on
   `store.ConsumerNegotiation` and `store.ConsumerTransfer` in
   `internal/store/store.go`, `transferHandler.lookup`'s placement warning
   (`internal/dsp/transfer_handler.go`), and
   `internal/dsp/transfer_consumer_handler.go` — a provider-role
   counterparty is a verified issuer, a consumer-role one is an operator's
   assertion, and §35.5 records that a roster name is not necessarily the
   participant at `connectorAddress`. It declined the conclusion: §35.2
   validates `providerId` anyway, not because the move leaves the hole open
   but because an unverifiable name accepted at initiate time surfaces
   later as blanket refusals in someone else's subsystem. And the reason
   the 30-of-65 constraint dissolved is not the one predicted here — the
   harness's long-lived credential was necessary but not sufficient. What
   removed the cost was correcting the harness's own participant identity
   to the name it already sends (§35.4).

3. **The roster as a versioned, expiring artifact**, plus clock leeway.
   *Before* any onboarding document is written, or the document describes a
   procedure about to be replaced.

   *(2026-08-25: the roster half is done — `DECISIONS.md` §36. The clock work
   is not, and it was never one item with the roster: the two share no code
   and no decision, and their evidence is opposite. Both harnesses exercise
   the roster half simply by coming up; neither can exercise a clock
   difference, because every container shares one host clock. What is still
   owed is leeway on the `exp` comparison and a bound on the credential's
   lifetime — not `nbf`, which would newly refuse pairs that transact fine.
   §36.9 records why it split, and what deferring it costs §36 is that the
   fleet stops across its clock spread rather than at one instant.
   `config.example.yaml` now describes the roster procedure, so the "before
   any onboarding document" instruction was met for this half.)*

   *(2026-08-26: the clock half is done — `DECISIONS.md` §37. The addendum
   above said `exp - iat` until this correction, and that quantity measures
   nothing: `iat` and `exp` are both integers the issuer signs, so an issuer
   wanting a decade sets `iat` a decade ahead and `exp` an hour after it and
   passes any bound on their difference. It was corrected in place rather
   than left with a note beside it, because a sentence saying what is owed
   goes on prescribing what it names. What shipped bounds `exp - now` against
   the verifier's own clock, alongside a minute of leeway on the expiry
   comparison; both constants are in `internal/auth/token.go`. `nbf` stayed
   declined. The evidence prediction held exactly: `go test` is the only gate
   that carries it, and both harnesses stayed green because neither can
   express a clock difference.)*

4. **Discovery** — a catalog client. After 2, because it mints outbound
   credentials whose audience 2 decides; before 5, because a ten-minute
   script that includes "obtain the offer id out of band" is not worth
   measuring.

   *(2026-08-25: this item also inherits binding `connectorAddress` to a
   roster entry, which `DECISIONS.md` §35.5 and item 3 both assigned to the
   roster milestone. §36 declined it on scope: it changed the roster
   document's *lifecycle* — a revision and an expiry, both properties of the
   document as a whole — while an address changes what an *entry* means, and
   would make every address change a re-signed roster and a fleet-wide
   restart. It is not that the field would be unverifiable earlier — both
   initiate handlers already take `providerId` and `connectorAddress` in one
   body and validate both, so a roster address could be compared at exactly
   that point with no discovery client. It lands here because this is where
   something actually consumes an address. Until then the gap §35.5 names
   stays open and `SECURITY.md` carries it.)*

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

- **The first pass miscounted the management API's routes.**
  `GET /.well-known/dspace-version` is a DSP protocol endpoint on the public
  listener, not a management route. *(2026-08-24: the number this bullet
  once carried has been wrong twice — `GET /transfers` moved it once
  (`DECISIONS.md` §34.4) and the initiate hooks moved it again (§35.5). The
  count is removed rather than corrected a third time.
  `internal/mgmt/router.go` is where the routes are, and it is the only thing
  that cannot go stale.)*
- **The 30 second `WriteTimeout` is not a silent limit.** `cmd/dsbox/main.go`
  carries an explicit comment predicting exactly this. The silent one is the
  consumer's ten seconds. *(2026-08-24: the consumer's ten seconds is gone —
  `DECISIONS.md` §33.1 replaced both bounds with one idle timeout. The
  `WriteTimeout` stays, for the narrower reason §33.3 gives.)*
- **"Restart required" is one decision, not several gaps.** Roster (§9),
  token rotation (§11), and dataset changes (§22.1) apply a single recorded
  principle. The roster's real defect is revocation, not the restart.
- **"Protocol: essentially done" was wrong in the optimistic direction.** See
  P1.
- **The initiate hooks have a fourth residual, not three.** An agreement
  concluded while `require_auth` was off has no recoverable owner, and that
  flag is the migration path this repository documents. Fixing the hooks does
  not close it.

Found on a second pass, after this document was committed:

- **The "ten minutes" count was wrong.** It said five occurrences; there are
  four of "ten minutes" and two more of "ten-minute". The point stands and the
  arithmetic did not.
- **It went stale in four minutes.** The P4 bullet saying `SECURITY.md` is
  absent was true when written and false by the next commit, which added it.
  A document that measures a moving repository dates from its commit, not from
  its reasoning.
- **It claimed a role-labeled protocol table was missing.** `README.md` has
  one; it labels `CN`/`CN_C` and `TP`/`TP_C` and omits `MET` and `CAT`. An
  inconsistent table is a different defect from an absent one, and the
  inconsistency happens to fall exactly where the missing code is.
