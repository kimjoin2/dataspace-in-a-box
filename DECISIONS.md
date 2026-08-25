# Design Decisions

This document records **why** things are the way they are.
The decisions themselves are visible in the code; the reasoning is not.

**Read this before proposing an architectural change.** If a proposal
contradicts something here, the rationale and the accepted trade-off are the
starting point of the discussion — not the conclusion. If a decision turns out
to be wrong, amend the entry rather than deleting it, and keep the original
reasoning visible.

Status: concept design closed. Implementation in progress.
Not loaded into `CLAUDE.md` — read on demand.

---

## 0. Mission and non-goals

**Mission.** A minimum *operational* dataspace. Clone, run, done — in about ten
minutes, without a consulting engagement.

**Rationale.** Existing open-source dataspace implementations are modular
construction kits rather than runnable systems. The official Eclipse EDC
Minimum Viable Dataspace states outright that it is a developer playground, not
production-grade, and that EDC itself is not a turn-key application. That leaves
a gap — "who assembles it for me?" — which is commonly filled by paid vendor
services. Documentation quality is part of the same gap. This project treats
that gap as the product: neutral, runnable code plus honest documentation.

**Non-goals.**

- Production-grade deployment guarantees for arbitrary environments
- Feature parity with EDC
- Multi-protocol support (see §3)

**Trade-off accepted.** A narrower feature set than any vendor offering.
Simplicity is the differentiator; losing it means losing the reason to exist.

---

## 1. Implement DSP directly, do not wrap EDC

**Decision.** Write an independent DSP implementation rather than packaging
EDC/MVD with compose files and seed scripts.

**Rationale.** Compliance is owed to the *protocol*, not to any codebase. DSP
defines messages, state machines, and an HTTPS binding; implementation
architecture is unconstrained. Wrapping EDC would inherit exactly the complexity
this project exists to avoid, and the wrapper would break on every upstream
change.

**Trade-off accepted.** Substantially more implementation work up front, and no
free ride on upstream bug fixes.

---

## 2. Compliance target: DSP 2025-1, verified by the official TCK

**Decision.** Target DSP 2025-1. Scope is the four core protocols in TCK order:
metadata → catalog → contract negotiation → transfer process. The official DSP
TCK runs as a CI gate, with the pass rate shown in the README.

**Rationale.** A TCK badge is a claim anyone can verify and nobody has to be
paid to explain. It converts "are you standards-compliant?" from a sales
conversation into a CI artifact. It also fixes the v1 scope for free: whatever
the TCK covers is in, everything else is out.

- Spec: https://docs.internationaldataspaces.org/ids-knowledgebase/dataspace-protocol
- TCK: https://github.com/eclipse-dataspacetck/dsp-tck

**Trade-off accepted.** The TCK defines the roadmap, so features it does not
cover get deprioritized regardless of user demand.

---

## 3. IEEE 3800 series: terminology alignment only

**Decision.** Align vocabulary with the published IEEE 3800-2024 standard.
Only published material is used; unpublished or draft standards material is out
of scope.

**Rationale.** A project whose premise is neutrality can only be built on
sources anyone else can obtain and check. Restricting inputs to published
material keeps every claim in this repository independently verifiable.

**Trade-off accepted.** Terminology may lag work that is still in progress
elsewhere.

---

## 4. No plugin system

**Decision.** No SPI, no inheritance-based extension points, no plugin loader.
Extension happens by fork or pull request.

**Rationale.** Most of EDC's complexity comes from extension points designed in
advance, and that complexity is precisely what creates demand for outside help.
Extensibility bought at the cost of simplicity defeats the project's purpose.
SQLite has validated the alternative model — one implementation, contributions
by patch — at scale.

**Trade-off accepted.** Users with unusual requirements must fork. Accepted.

---

## 5. License: FSL-1.1-Apache-2.0, plus CLA and Sponsors

**Decision.** Functional Source License 1.1 with Apache 2.0 as the future
license. Contributors sign a CLA (automated via CLA Assistant). GitHub Sponsors
is enabled.

**Rationale.** FSL restricts only competing commercial use; internal use,
modification, and production operation stay free. That matches the grievance
exactly: prevent a vendor from reselling this as their product, while leaving
actual users unrestricted. Each release converts to Apache 2.0 after two years,
which preserves the public-good argument. The CLA is not optional — without
assigned rights, relicensing and any future commercial arrangement break.

- https://fsl.software/

**Note on naming.** The canonical abbreviation for this variant is now
`FSL-1.1-ALv2`; the `FSL-1.1-Apache-2.0` name redirects to it upstream. Same
license, and `LICENSE.md` carries the current text verbatim.

**Trade-off accepted.** FSL is not OSI-approved open source. The README must say
"Fair Source / source-available", and some organizations will decline on policy
grounds regardless of the actual terms.

---

## 6. Independent ownership

**Decision.** Personal GitHub account, copyright held by the registered sole
proprietorship b7g. The project carries no organizational affiliation.

**Rationale.** The value on offer is neutrality: an implementation and a
compliance claim that serve no vendor's interest. An unaffiliated project is the
only kind that can make that claim credibly.

**Trade-off accepted.** No institutional credibility to borrow. The TCK result
has to carry the argument on its own.

---

## 7. Stack: Go, single static binary, embedded UI

**Decision.** Go. One statically linked binary. The Svelte UI is built to static
assets and embedded via `go:embed`.

**Rationale.** "Download one file and run it" is the shortest possible path to a
working dataspace, and the ten-minute promise is the product. A separate
frontend service, a runtime, or a package manager each add a step where a new
user gives up.

**Trade-off accepted.** UI changes require a rebuild and a release.

---

## 8. Storage: SQLite file with WAL

**Decision.** A single SQLite file under a configurable `data_dir`, WAL mode.
In-memory SQLite is used in tests only.

**Rationale.** Connector runtime state — negotiation and transfer state
machines, agreements — needs durability and transactions, and nothing more. An
external database would add an operational dependency larger than the connector
itself. Advertised datasets are not runtime state: they are an operator
declaration and live in the configuration file (see §22.1).

**Trade-off accepted.** One writer per data directory; no horizontal scaling of
a single participant. Out of scope for v1.

**Note (2026-08, contract negotiation milestone).** This section's
justification stops being aspirational here: the `negotiations` table is the
first runtime state this project persists. See §23.1 for the migration
mechanism this introduced.

---

## 9. Identity: did:web plus an operator-signed static roster

**Decision.** Participants are identified by `did:web` with Ed25519 keys. The
authoritative participant registry is a static JSON file signed by the operator,
managed with the `dsops` CLI and served as a plain file.

**Rationale.** Governance events — adding or removing a participant — are rare.
A rare event does not justify a running service with a database and an admin UI.
A signed file can be served from anywhere, verified offline, and diffed in git.
The operator's signature is the trust anchor.

**Trade-off accepted.** Roster changes require redistribution, and adding a
participant is only as fast as propagation. Acceptable at the assumed change
frequency.

*Amended by §36 (2026-08-25).* Removing one was worse than this said: a
superseded roster carried no revision and no expiry, so it verified forever
wherever it was held and nothing detected a rollback. §36 puts both inside
the signature, which gives revocation an upper bound — the expiry — rather
than leaving it at "as fast as propagation". What the bound costs is that the
whole fleet shares one `expires_at` and stops at it, and only the
`roster_signer` key can restart it (§36.5). `maxRosterLifetime` in
`internal/auth/roster.go` caps how far ahead that instant may sit, so the
bound is the connector's and not whatever the operator typed.

---

## 10. Connector auth: self-signed short-lived JWT; DCP deferred to v2

**Decision.** Connectors authenticate to each other with self-signed JWTs
(5-minute expiry) verified against the public keys in the roster. Decentralized
Claims Protocol and Verifiable Credentials are out of scope for v1.

**Rationale.** DCP is a separate overlay specification with its own TCK. Pulling
it into v1 would roughly double the surface area before a single core protocol
was proven. The roster already provides a sufficient trust anchor for the
scenarios v1 targets.

**Trade-off accepted.** No credential-based access policies, and no
interoperability with dataspaces that mandate DCP. Revisit in v2.

---

## 11. Management API auth: one static bearer token

**Decision.** A single static bearer token from the config file. No login, no
sessions, no user accounts.

**Rationale.** The management API has exactly one user: the operator of that
participant. User management would be infrastructure serving a population of
one.

**Trade-off accepted.** No multi-user access, no audit trail by identity, and
token rotation means editing config and restarting.

---

## 12. Split ports: public DSP and internal management

**Decision.** DSP endpoints and the management API + UI listen on separate ports
(e.g. 8080 public, 8081 management). The management port binds to localhost by
default.

**Rationale.** A firewall mistake should not be able to expose the management
API. Separating by port makes the safe configuration the default one and turns a
policy question into a structural guarantee.

**Trade-off accepted.** Remote management requires an SSH tunnel or an explicit
bind-address change.

---

## 13. TLS terminates at a reverse proxy

**Decision.** The connector speaks plain HTTP and assumes a reverse proxy
(Caddy, nginx, a cloud load balancer) in front. No TLS options in config, no
certificate handling, no ACME.

Three consequences are binding:

1. **`public_url` is a required config value.** Behind a proxy the connector
   cannot determine its own external address, and DSP messages carry callback
   addresses while `did:web` identifiers derive from the external URL. Never
   infer this from the `Host` header.
2. **Proxy headers are for logging only.** `X-Forwarded-For` is logged and never
   used for authentication or authorization; identity comes from the JWT, so IP
   trust is unnecessary.
3. **`dev_mode: true` permits `http://` participants.** `did:web` assumes HTTPS.
   The local demo runs connectors directly against each other with no proxy, so
   the relaxation is explicit and confined to dev mode.

**Rationale.** Certificate management is solved better by dedicated software.
Omitting it removes code, config surface, and documentation at once. Requiring
`public_url` explicitly is simpler than any header-trust heuristic.

**Trade-off accepted.** Standalone HTTPS is impossible; production deployment
always involves a second component.

---

## 14. Policy (ODRL) scope in v1

**Decision.** Evaluate two policy shapes: unrestricted use, and a validity
period constraint. Any other constraint parses successfully but causes the
negotiation to be rejected.

**Rationale.** A general policy engine is a project in its own right. Explicit
rejection is honest; silent non-enforcement is a security bug wearing a feature
costume.

**Trade-off accepted.** Real-world policies will frequently be rejected. Policy
engine work belongs in v2 or in contributions.

---

## 15. Config: one YAML file with environment overrides

**Decision.** A single YAML file, overridable by environment variables. No TOML,
no JSON, no config directories, no merge layers.

**Rationale.** Multi-format support is pure cost — more code, more docs, more
"which one wins?" questions — with no user benefit.

**Trade-off accepted.** Users who prefer another format convert it themselves.

---

## 16. Releases: semver, goreleaser, prebuilt binaries

**Decision.** Semantic versioning, GitHub Releases with linux/macOS binaries for
amd64 and arm64, built by goreleaser.

**Note.** Under FSL, each released version starts its own two-year clock toward
Apache 2.0. Tags therefore carry licensing meaning, not just engineering
meaning. State this explicitly in the release documentation.

**Trade-off accepted.** Release discipline is now a licensing obligation, not
just a convention.

---

## 17. Native execution first, Docker as demo packaging

**Decision.** Running the binary directly is the documented default. Docker and
compose exist to package the demo.

**Rationale.** A static binary with an embedded UI and a SQLite file has no
dependencies worth containerizing. Making Docker the primary path would add a
prerequisite to the ten-minute promise for no gain.

**Trade-off accepted.** Users who expect a container-first project will need the
README to correct that expectation.

---

## 18. Documentation and UI in English only

**Decision.** English for documentation, UI, and code comments. Introductions in
other languages live on a blog, not in the repository.

**Rationale.** The audience is the global dataspace community, which is
predominantly European. A localized repository reduces the reach that is the
entire point of publishing.

**Trade-off accepted.** Higher barrier for domestic readers, and translation
requests will need to be declined.

---

## 19. Test layering

**Decision.** Four layers, one CI pipeline (GitHub Actions):

| Layer | Scope |
|---|---|
| Go unit tests | State machines and pure logic, in-memory SQLite |
| Go integration tests | Multiple connector instances in one process |
| DSP TCK | Compliance gate |
| Playwright smoke | A minimal path against the production binary with embedded UI |

**Rationale.** Running several connectors in-process makes multi-party scenarios
fast and debuggable without containers. The TCK is the only layer that proves
anything to an outsider, so it gates. Browser tests are the slowest and least
reliable layer and stay deliberately minimal — enough to catch a broken embed.

**Trade-off accepted.** In-process integration tests do not exercise real
network behavior; the TCK partially compensates.

---

## 20. JSON-LD handling

**Decision.** Process the fixed compact form and validate with JSON Schema. No
general RDF processing.

**Rationale.** Go's JSON-LD library ecosystem was the largest identified
technical risk at design time. DSP 2025-1 having moved toward fixed compact-form
serialization validated by JSON Schema removes most of that risk: the messages
can be treated as ordinary structured JSON.

**Trade-off accepted.** Arbitrary JSON-LD input — different context forms,
expanded form — is not handled. Acceptable while the spec mandates the compact
form.

---

## Deferred to implementation

Not decisions, just choices with no strategic weight. Decide when first needed
and record here afterwards:

- ~~Go version and minimum supported release~~ → decided, see §21
- ~~HTTP router~~ → decided, see §21
- ~~Logging library and output format~~ → decided, see §21

---

## 21. Toolchain choices (settled at first milestone)

**Decision.**

| Item | Choice |
|---|---|
| Go | 1.26 (`go.mod`), latest stable at project start |
| HTTP router | standard `net/http` with 1.22+ pattern routing |
| Logging | standard `log/slog`, JSON handler |
| Test framework | standard `testing` only |

**Rationale.** The default answer to "which dependency?" is the standard
library, and for each of these the standard library is sufficient. Pattern
routing landed in `net/http` in Go 1.22, which removes the last common reason to
add a router. A new project has no backward-compatibility obligation, so
tracking the latest stable Go costs nothing.

**Trade-off accepted.** Users on older Go toolchains must upgrade to build from
source. Prebuilt binaries (§16) make this a contributor concern only.

---

## 22. Catalog: advertised from configuration

**Decision.** Five decisions taken while implementing the catalog protocol.

**22.1 Advertised datasets come from the configuration file, not from storage.**
§8 justifies SQLite with connector runtime state — negotiation and transfer
state machines, agreements. A dataset list is none of those; it is something the
operator declares. Introducing storage here would drag the still-open migration
question in with it and double the milestone. SQLite arrives when a state
machine needs it.

*Trade-off accepted.* Changing what is advertised means editing the
configuration and restarting — the same cost §11 already accepted for token
rotation.

**22.2 The configuration carries dataset identifiers and nothing else.** The
connector synthesizes the offer, the distribution, and the data service.
Advertising a policy and enforcing one are different acts, and the code that
enforces belongs to the negotiation milestone. Exposing a policy syntax now
would ship a vocabulary nothing checks. When evaluation is written, the
configuration grows a `policy:` key alongside it.

*Trade-off accepted.* Every advertised dataset carries the same unrestricted-use
offer until negotiation lands.

**22.3 `participant_id` is a required configuration value.** No inference. §9
will eventually make this a `did:web` identifier; deriving one now would mint
DIDs that nothing can resolve, because the roster does not exist yet. The key
stays the same when that day comes — only its value changes.

*Trade-off accepted.* One more required value in the smallest possible
configuration file.

**22.4 A catalog request carrying `filter` is rejected with `CatalogError`.**
DSP leaves the filter expression implementation-defined, which means a provider
cannot know what an arbitrary filter means. Returning the full catalog to a
consumer that asked for a subset lets it believe it received a filtered view.
Explicit rejection is the stance §14 already takes on policy constraints.

*Trade-off accepted.* Consumers that attach a filter unconditionally will not
interoperate until filtering is implemented.

**22.5 Incoming messages are validated by direct field checks, not by a JSON
Schema library.** §20 specifies JSON Schema validation; the standard library has
none, and the default answer to a dependency question is the standard library.
This milestone validates one incoming message, `CatalogRequestMessage`, with
two required fields — that does not justify a validation engine. §20 stands as
written and is revisited when negotiation and transfer push the message count
past a dozen.

*Trade-off accepted.* Validation coverage is whatever the handwritten checks
cover, and a missed field is a silent gap rather than a schema failure.

---

## 23. Contract negotiation (provider role)

**Decision.** Twelve decisions taken while implementing the contract
negotiation protocol's provider role.

**23.1 No migration framework — an idempotent `CREATE TABLE IF NOT EXISTS`
plus one hand-written, self-checking `ALTER TABLE` per added column, both run
at startup.** §8 named "migration approach for the SQLite schema" as deferred
until first needed; the `negotiations` table is that moment. A second schema
change is what decides whether a real migration tool earns its place — one
table does not. The check is not optional decoration: `CREATE TABLE IF NOT
EXISTS` is a *no-op* against a table that already exists, so editing the
schema literal changes nothing for a database file an earlier build created —
it opens without complaint and then fails on the first query naming the new
column. `store.migrate` therefore asks `pragma_table_info('negotiations')`
whether `rerequested` (§23.9) is there and runs
`ALTER TABLE negotiations ADD COLUMN rerequested INTEGER NOT NULL DEFAULT 0`
when it is not. On a fresh database the `CREATE` already made the column, the
check finds it, and nothing runs.

*Trade-off accepted.* No down-migrations, no versioned schema history, and
nothing enforces the pairing: a future column added to the schema literal
without its own check-and-add step here is a bug that a fresh database — every
test, every new install — cannot reproduce. Only an upgrade of an existing
file surfaces it, which is exactly why `store_test.go` builds a
pre-`rerequested` database by hand and opens it.

**23.2 `modernc.org/sqlite` is the SQLite driver.** Pure Go, no CGO. §7
commits to a single static binary and §16 to `goreleaser` cross-compiling
linux/macOS × amd64/arm64; a CGO-based driver needs a C toolchain per target
and would break that promise the first time someone builds from source
without one.

*Trade-off accepted.* Less mature than `mattn/go-sqlite3` for exotic SQLite
features. The SQL this project writes stays inside what any SQLite driver
supports: `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, and — since §36 —
`INSERT … ON CONFLICT DO UPDATE` for the single-row `roster_version` upsert
in `Store.RecordRosterVersion`. This driver runs it — `internal/store/`'s
roster-version tests exercise the insert and the update path against a real
database — so the conclusion is unchanged. The list is updated rather than
left to read as exhaustive when it is not.

**23.3 Provider pids are generated with `crypto/rand`, not a UUID
package.** 16 random bytes formatted per RFC 4122 UUID v4. §21's default
answer to a dependency question is the standard library, and this project
fully controls the shape of a value it both generates and consumes.

*Trade-off accepted.* No UUID variant beyond v4, and no parsing of
externally-supplied UUIDs — neither is needed here.

**23.4 A validity-period policy constraint, checked at accept-time, is the
trigger for the autonomous-termination scenarios `CN:02-05` and
`CN:02-06`.** §14 already permits a validity-period constraint as one of
exactly two enforceable v1 policy shapes; this milestone is its first real
use. The check runs at two points: when an unmatched request would otherwise
earn only an informational counter-offer, and when an `ACCEPTED` event would
otherwise advance the negotiation to `AGREED`.

To be precise about what is being checked, since the wording invites a
stronger reading than the code supports: the validity period is *this
connector's own*, `config.Dataset.ValidityUntil` from YAML, evaluated by
`isValid`. It is not an ODRL constraint evaluated from an inbound message —
no code in `internal/dsp` evaluates one. (Since §24.6, inbound constraint
nodes are *parsed*, structurally: `Permission.Constraint []json.RawMessage`
and `carriesConstraint` establish that one is present, which is exactly what
§14 means by "parses successfully". What no code does is interpret what it
says.) See §24.6 for what does happen to a counterparty-authored constraint,
and why the provider role never needed to ask.

*Trade-off accepted.* `CN:02-07` needs a termination trigger that fires
*after* a negotiation has already passed this check once (at `AGREED`) — a
check performed once at accept-time cannot produce that. `CN:02-07` is
tracked as a named, deliberate gap (see §23.5 and `docs/follow-ups.md`), not
forced to pass with an unjustified mechanism.

**23.5 The TCK compliance gate (`cmd/tckgate`) gained a named-exemption
mechanism.** The existing per-suite exact-count model could not express
"this suite's 15 results all arrive, but one specific, named test is known to
fail." A second map, `exempt`, holds individual test IDs excused from the
failure gate; a suite's expected count still includes them, so the gate still
proves the suite ran to completion. The exemption is self-cleaning in one
direction: an exempted test that unexpectedly *passes* fails the gate, on the
theory that a stale exemption hiding a real pass is worse than one hiding a
real failure.

*Trade-off accepted.* An exemption can go stale in the other direction — the
TCK could change what `CN:02-07` asserts, and this project would not notice
until it happened to pass. Acceptable: the gate already re-runs the full
suite on every CI run, and a stale exemption is not a silent regression, only
a silently-outdated comment.

**23.6 The unauthenticated `callbackAddress` SSRF guard rejects loopback,
link-local, and unspecified addresses, but not RFC1918/ULA private
ranges.** The first version rejected `net.IP.IsPrivate()` too, which the real
TCK's own callback address — a Docker-bridge private IP — immediately failed
against, making the harness untestable. Loopback and link-local stay blocked
regardless: they always resolve to *this connector's own host*, on any
network, so nothing about the deployment can make pointing a push at them
safe. A private-range address is different — it names the operator's own
network, which is exactly where a legitimately-deployed counterparty
connector is expected to live, and is not a path back into this process the
way loopback is.

*Trade-off accepted.* A consumer on the operator's own private network can
still direct this connector's outbound pushes at other private-network
services (an SSRF pivot, just not against this process itself). Considered
and rejected: removing the guard entirely, on the theory that the operator's
own firewall owns this class of exposure — loopback/link-local are not
something a firewall mitigates, since they never leave the host, so "the
firewall is the user's responsibility" only actually covers the
private-range case this section allows through.

**23.7 A failed callback push retries up to 4 more times, with a
300ms/700ms/1.5s/3s backoff, before being dropped.** Real TCK evidence,
decompiled from the pinned image's own source (`AbstractDspPipeline`,
`AbstractAsyncPipeline`): the TCK's async-listener registration for a given
test runs as a pipeline stage that only starts once the *previous* stage's
synchronous call has returned — a real, TCK-side window in which this
connector's near-instant push can arrive before anything is listening for
it. A single real run needed as many as 2 attempts (of the eventual 5) for a
CN:02-06 push that raced this way.

*Trade-off accepted, with an open question.* This schedule is not proven to
close the registration-race window in general — a separate real run,
widened experimentally to 10 attempts spanning roughly 54 seconds, still saw
every attempt for several other tests rejected. That deeper failure turned
out to have a different, structural cause (§23.8): once fixed, the
registration race shrank back down to the occasional single retry this
schedule was originally sized for. Whether 5 attempts is enough margin for
every real deployment's network conditions is not something either run
answered, only this project's own conditions.

**23.8 `dispatch` and `pushAndStore` are always invoked with `go`, from
every handler, never called inline.** Discovered against the real TCK, not
predicted: `net/http` buffers a response under roughly 2KB (all of this
project's response bodies) and does not put it on the wire — not even the
status line — until the handler function *returns*. A handler that writes
its synchronous response and then calls the push machinery inline never
actually finishes sending that response until the push either succeeds or
exhausts its retry schedule, because the push runs inside the same,
still-executing handler goroutine. Against the real TCK this was not a slow
edge case; it was total and consistent — the TCK's own client timed out
waiting for the *synchronous* response itself, and every async push
downstream of that raced a consumer that had no way to be ready, because the
response that would have told it to get ready was the thing stuck behind
the push. `go dispatch(...)` (and `go pushAndStore(...)` at the two call
sites that invoke it directly, `handleReRequest` and `handleVerification`)
breaks the cycle: the handler returns immediately after writing its
response, the response goes out, and the push happens genuinely afterward.

*Trade-off accepted.* Every push is now a fire-and-forget goroutine with no
handle for the caller to observe or bound; a test that wants to assert on
push side effects has to poll (`negotiation_handler_test.go`'s
`waitForState`) rather than check state synchronously after the handler
call returns. A production request volume this milestone does not anticipate
could accumulate unbounded goroutines — acceptable for the same reason the
retry schedule in §23.7 is: this is a v1 provider role for a small
dataspace, not a high-throughput gateway.

**23.9 A negotiation accepts exactly one re-request while `OFFERED`;
whether that one re-request repeats the offer already on the table decides
what happens *within* it, not whether it is accepted.** The design spec's
original inference — that repeating the current offer is itself the
synchronous-rejection case (`CN:03-04`) and anything else is the
asynchronous-termination case (`CN:01-02`) — was backwards, confirmed the
first time this ran against the real TCK. `CN:03-04`'s own sequence sends
the *identical* offer twice: the first is accepted (`200`, the negotiation
unchanged, still `OFFERED`), and only the second — a re-request with
nothing left to decide, because the first one already re-affirmed the same
offer — is the `4xx`. A mismatched re-request (`CN:01-02`) is accepted too,
but is treated as the consumer walking away from what this connector can
actually offer, so it is followed by an asynchronous termination push,
exactly as before. `store.Negotiation.Rerequested`, set the first time
`handleReRequest` accepts a re-request and checked on every subsequent one,
is what the synchronous rejection actually keys on.

*Trade-off accepted.* A consumer gets exactly one shot at a counter-offer or
resend per negotiation, matching, mismatched, or otherwise — there is no
provision for a second, different counter-offer after the first is
rejected. Nothing in the TCK's `CN` suite exercises that case, so this is
the smallest rule the two real tests actually require, not a general
renegotiation protocol.

**23.10 The TCK harness's `dataspacetck.dsp.default.wait` is in seconds,
not milliseconds.** The pinned image's own source
(`DspSystemLauncher.start`) reads this key with `getPropertyAsLong` and
passes it straight into `java.util.concurrent`'s `SECONDS`-unit `await`
calls, unconverted; its own built-in fallback if the key is absent is `15`.
`test/tck/config.properties` originally carried `10000`, copied from
upstream `sample.tck.properties`'s value for the same key — but that
sample's own value is `10000000`, which under this same unit is roughly 115
days, so it was never a literal template to copy in the first place. At
`10000` seconds (2h46m), a single genuinely-failing async push turned every
test that needed one into a multi-hour wait instead of a fast, debuggable
failure — which is how the real bugs behind §23.7-§23.9 took most of a day
to isolate. Corrected to `20`.

*Trade-off accepted.* None — this was a harness misconfiguration, not a
choice with a cost on the other side. Recorded here because reintroducing a
large value by copying an upstream sample number without checking its unit
is an easy mistake to make twice.

**23.11 Negotiation endpoints ship unauthenticated (until §27), and the
provider pid is accepted as the only thing protecting a negotiation.** §10's
connector-to-connector JWT is specified but not yet enforced on the DSP
listener, so every endpoint this milestone adds is open, exactly as the
catalog endpoints are. That posture was settled for a *read-only* protocol.
Negotiation is not read-only: it writes persistent state and makes outbound
requests, so carrying the posture over is an escalation, and it is recorded
here rather than left implied. Two specific consequences follow. First, the
provider pid is a de-facto capability token — no handler checks a message's
`consumerPid`/`providerPid` against the stored negotiation, so anyone who
learns a pid can terminate or finalize that negotiation. It is a 128-bit
`crypto/rand` value (§23.3) that only ever travels between the two parties,
which is what makes this defensible, but it is a bearer secret and should be
named as one. Second, `POST /negotiations/request` is an unauthenticated
amplifier: one anonymous request creates a row, a goroutine, and up to ten
outbound POSTs to an address the caller chose — two pushes for a mismatched,
expired dataset, five §23.7 attempts each. §23.6 covers where those POSTs may
point; it does not cover how many of them one request buys.

*Trade-off accepted.* Adding a pid cross-check was considered and deliberately
not done in this milestone: this branch has a hard-won TCK-confirmed pass, and
which pid the TCK puts in which field on each message has not been verified
against such a check — spending that verified state on a defense nobody asked
for is the wrong trade at this milestone. Both consequences are future work,
and both are properly closed by enforcing §10's connector-to-connector JWT on
this listener, not by patching the handlers one field at a time.

**Corrected by §32: that prediction was tested and was wrong.** The JWT
shipped in §27 and narrowed the attacker set from anyone on the network to any
roster participant, which closed neither consequence — a roster is shared by
strangers. The handlers were checked one field at a time after all. The rest of
this section, including the reason the cross-check was not spent at this
milestone, stands as written.

**23.12 State transitions are compare-and-swap, but the push still happens
*before* the state is written, not after.** Two halves, and the second one is
the surprising one.

The compare-and-swap: `store.SetState` takes the state the caller believed
the negotiation was in and updates only while that still holds, and
`store.SetRerequested` is conditional on the flag still being clear. Without
it, §23.8's fire-and-forget goroutines can write a state decided against a
read taken seconds earlier — a verification's `FINALIZED` write, spawned
before a termination arrived and still working through the §23.7 retry
backoff, lands after it and overwrites `TERMINATED`, on `CN:03-01`'s exact
boundary. With it, the stale write matches no row and is dropped.

The ordering: writing the state first and pushing second looks strictly
better — it would stop `GET /negotiations/{id}` from reporting a state older
than one already announced — and it fails the TCK. `CN:03-03` regressed the
moment it was tried. Its consumer accepts, then verifies about 100ms later
*without having received the agreement*, and the provider must reject that.
Storing first makes the negotiation `AGREED` a millisecond after the accept,
so the illegal verification becomes legal. Pushing first holds the old state
for exactly as long as delivery is still being attempted, which keeps "not
`AGREED` yet" and "the consumer does not have the agreement yet" the same
fact — and that identity is what makes `handleVerification`'s state check a
real guard. In DSP the provider does not become `AGREED` and then announce
it; it becomes `AGREED` by delivering the agreement.

*Trade-off accepted.* `GET /negotiations/{id}` can report a state one
transition behind a push that has already landed, for the length of the push.
That is the staleness the reordering would have removed, and it is the price
of the guard above. A lost compare-and-swap is also silent to the
counterparty: the push already went out, and only the write is dropped, so a
negotiation can be told `FINALIZED` and then correctly recorded `TERMINATED`
by the request that overtook it — unavoidable in an asynchronous protocol,
and the provider's record ends up right. On the synchronous handlers a lost
race becomes a `400`, the same rejection the state precondition would have
produced had it been checked a moment later.

## 24. Contract negotiation (consumer role)

**Decision.** Seven decisions taken while implementing the contract
negotiation protocol's consumer role. No decision in §23 is reversed or
amended: everything here is additive, and the `CN` suite's pass count is
unchanged by this milestone. The §23 *text* did move — §23.4 gained a
paragraph pinning down what its "validity-period constraint" is and is not,
which §24.6 made worth saying explicitly. That is a clarification of what
§23.4 always decided, not a change to it.

**24.1 Consumer-role negotiations live in a second table,
`consumer_negotiations`, not in the existing `negotiations` table behind a
`role` column.** The provider table is 14 of 15 TCK tests deep. A shared
table would make every row carry columns that mean different things
depending on which role wrote it — `callback_address` is per-negotiation
data for a provider and a constant for a consumer, `rerequested` (§23.9)
guards an external actor's second HTTP call and has no consumer-role
equivalent at all — and would put a `WHERE role = 'provider'` into every
query the provider milestone shipped without one. A second table costs one
more `CREATE TABLE IF NOT EXISTS` and a handful of CRUD functions shaped
exactly like the ones already there, including the same compare-and-swap
`SetConsumerState` for the same reason §23.12 needed `SetState`: consumer
reactions also run in goroutines and can outlive a termination that arrived
while they were retrying.

*Trade-off accepted.* Two tables that hold the same protocol's state, so a
future query spanning both roles has to union them, and `explainNoUpdate`
needed a consumer-table twin rather than being reused — it hard-codes a
lookup against `negotiations`, and sharing it would have made a failed
consumer-side update report the wrong table's state.

**24.2 `POST /negotiations/initiate` is a TCK-shaped trigger hook on the
*public* listener, unauthenticated until §27, and a real management-API
trigger is deliberately not built in this milestone.** `DspSystemLauncher.start()`
requires `dataspacetck.dsp.connector.negotiation.initiate.url`
unconditionally, and the TCK's own client POSTs plain JSON (not JSON-LD:
`{providerId, offerId, datasetId, connectorAddress}`, confirmed from the
`postJson(url, body, false, true)` call site) and requires `200`. Building a
real "start a negotiation" management feature is a different concern —
it would mean settling a production UX question (how does an operator pick a
provider and an offer?) as a side effect of a test-harness requirement.
Unauthenticated is consistent with §23.11's already-accepted posture, which
covers this connector's negotiation surface regardless of which role
receives the request.

*Trade-off accepted.* Until a management trigger exists, this endpoint is
the *only* way to start a negotiation as consumer, and it is open to
anonymous callers — one request creates a row, a goroutine, and outbound
POSTs to an address the caller chose. §23.6's SSRF guard
(`validateOutgoingCallback`) is applied to `connectorAddress` for exactly
that reason, and §23.11's closing note applies unchanged: the real fix is
enforcing §10's connector-to-connector JWT on this listener, not patching
this handler.

**Corrected by §32, in both halves.** "Open to anonymous callers" stopped being
true at §27, which put every DSP route but the version document behind a
participant credential: with `require_auth` on this endpoint is reachable by
any roster participant, and only with it off by anyone. And the closing note
inherited from §23.11 does not apply unchanged — the JWT shipped and did not
close what §23.11 predicted it would. What §32 does *not* change is this
endpoint's lack of an ownership check of its own; §32.3 records why it gets
none.

**Corrected again by §35, in the two halves §32 left.** The listener in the
headline above is no longer the right one: §35.1 moves this hook and its
transfer twin off the DSP listener onto the management listener, behind the
operator's token, and a roster participant reaching them now needs that token
rather than a participant credential. And what §32 declined to change has
changed — §35.2 refuses a `providerId` the roster does not list, and §35.3
applies `refuseIfNotParty` at every consumer-role resolver, so what this
endpoint records as a counterparty is an authorization anchor rather than a
string a caller chose. The trade-off above goes with them: this is still the
only way to start a negotiation as consumer, and it is now the management
trigger rather than a stand-in for one, so "a real management-API trigger is
deliberately not built" reads as what it is, a statement about a milestone
that has since passed.

**24.3 The initial outbound `ContractRequestMessage` is sent once, with no
retry — the only outbound call in this connector that is not routed through
`pushCallback`.** §23.7's retry schedule exists to survive a race on the
*receiving* side: the TCK registers a callback listener as a sequential
pipeline stage that can still be running when this connector's push lands.
Here this connector is the initiator, and the provider it calls is an
already-listening server by the time `/negotiations/initiate` fires. There
is no equivalent registration race in this direction. The call is also
genuinely different work rather than a `pushCallback` variant:
`pushCallback` discards the response body, and this call exists to read
`providerPid` out of the provider's synchronous `ContractNegotiation`
response.

*Trade-off accepted.* A transient network failure on the initial request
loses the negotiation silently — the row stays `REQUESTED` with an empty
`provider_pid`, nothing retries, and only the log records it. Acceptable
because there is nothing to retry *toward* until `providerPid` is known, and
because a real trigger (24.2) would be the natural place to surface the
failure to whoever asked for the negotiation.

**24.4 What this connector does with an offer, an agreement, or silence is
configuration (`consumer_policies`), keyed by the `dataset_id` this
connector itself requested — not a rule derived from the content of what it
receives.** All 16 `CN_C` tests send the identical wire input at a given
juncture and require different reactions: `CN_C:01-01` must auto-accept an
offer, `CN_C:02-04` must take no action on the same kind of offer,
`CN_C:01-03` must terminate on it, `CN_C:01-02` must counter it. There is no
content-based signal that could distinguish them even in principle — the
TCK's mock provider echoes this connector's own `datasetId`/`offerId` back
verbatim and `NegotiationFunctions.createOfferPolicy` hard-codes an empty
constraint list. `AbstractContractNegotiationConsumerTest` declares exactly
one `@ConfigParam` this project controls per test, `datasetId`, so the
reaction has to be a function of what this connector chose to request. Three
independent fields, each defaulting to the behavior a real consumer should
have with no configuration at all: `on_offer` (accept), `on_agreement`
(verify), `on_idle` (wait).

*Trade-off accepted.* This is honestly a TCK-driven mechanism today, and is
recorded as one rather than presented as a finished product feature: a
connector configured in advance to negotiate for named datasets under named
acceptance rules is a reasonable "minimum operational dataspace" capability,
but nothing outside the TCK harness can reach it yet, because 24.2 is the
only trigger that exists.

**24.5 `pushCallback` returns whether the push ultimately succeeded, and the
consumer's local state advances to `VERIFIED` only when it did.** `CN_C:03-06`
is not a timing window that could be widened away. Unlike `01-01`, `01-04`,
and `02-06`, that test never calls `.expectVerifiedMessage(...)`, so no
handler is ever registered for this connector's verification POST, and the
TCK's dispatcher answers an unregistered path with a plain `404` — for the
whole life of the test. Storing `VERIFIED` on send, mirroring §23.12's
provider-role push-then-store, makes that test fail deterministically. The
signature change is small (`func pushCallback(url string, v any) bool`, the
retry loop unchanged) and every other caller discards the result, including
all of the provider role's pushes.

*Trade-off accepted.* This connector's `VERIFIED` now depends on a
counterparty's acknowledgement rather than on its own action, so a provider
that receives the verification and fails to answer leaves this side at
`AGREED` while the provider considers itself verified. That divergence is
real, and it is the correct side to err on: claiming `VERIFIED` for a
message nobody confirmed receiving is the stronger lie. It is also a
deliberate asymmetry with §23.12, which stores unconditionally in the
provider direction — the two are different because §23.12's ordering rule
exists to keep "not `AGREED` yet" identical to "the consumer does not have
the agreement yet", which has no consumer-side counterpart.

**24.6 A received offer or agreement carrying *any* constraint is rejected,
because the consumer role enforces none.** `CLAUDE.md` states the rule
without exception — "never accept a constraint that is not enforced" — and
§14 calls silent non-enforcement "a security bug wearing a feature costume",
so this cannot be a documented exemption. Since v1 evaluates no constraint at
all, "any constraint this connector cannot enforce" is simply "any
constraint", and the guard needs no taxonomy and no evaluator: `Permission`
gained an inbound-only `Constraint []json.RawMessage` (omitempty, so nothing
this connector emits changed by a byte), and `decideOfferReaction` /
`decideAgreementReaction` divert the two adopting actions — `on_offer:
accept` and `on_agreement: verify` — onto the existing reject path when one
is present. That is §14's "parses successfully but causes the negotiation to
be rejected", literally: `json.RawMessage` requires well-formed JSON and
interprets none of it. The other actions are untouched, because none of them
adopts the counterparty's terms: `counter` re-proposes this connector's own
ask, `passive` holds `OFFERED` agreeing to nothing, `reject` already
terminates.

The agreement side is guarded as well as the offer side, and not for
symmetry: an agreement is the binding artifact, and `CN_C:01-04`'s
direct-agreement path sends one with no offer ever pushed, so a check on the
offer alone would leave the rule violable through a door the TCK itself
uses.

**Why this gap was consumer-only.** Not because the provider role checks
constraints — no non-test code in `internal/dsp` evaluates an ODRL
constraint, only detects that one is there, and §23.4's "validity-period
constraint" is this connector's own advertised
`config.Dataset.ValidityUntil` read from YAML (`isValid`), not anything
recovered from an inbound message. The provider role is *structurally
immune*: it matches a request against its own advertised offer by identifier
and then emits an agreement built entirely from its own configuration, so no
counterparty-authored policy content ever becomes a term it commits to. The
consumer role has no such immunity — the provider pushes the offer and the
agreement, and accepting them means adopting terms this connector did not
write. That asymmetry, not a difference in diligence, is the whole reason
this guard belongs here.

*Trade-off accepted.* A legitimate counterparty whose offer carries an
ordinary, reasonable constraint gets terminated rather than negotiated with,
which will feel blunt the first time a real provider is on the other end.
That is §14's accepted cost ("real-world policies will frequently be
rejected"), now paid on this side too. Two narrower limits are worth naming
rather than implying: the check is presence-only, so it cannot one day
"allow the validity-period constraint through" without a real evaluator
first existing; and it looks only at `permission`, since `NegotiationOffer`
declares no `prohibition` field (an inbound prohibition is dropped at decode
and is a rule, not a constraint — outside both this rule's wording and this
milestone's scope, but a real question for whoever adds prohibition
support). No TCK fixture exercises any of this — `createOfferPolicy`
hard-codes an empty constraint list — so it is unit tests, per §19's
layering, that hold it: both directions are pinned, including the empty
constraint list the TCK actually sends.

**24.7 The consumer's outbound `ContractRequestMessage` is its own type,
`ConsumerRequestMessage`, separate from the inbound-decoding
`RequestMessage`.** The same DSP message has opposite obligations in the two
directions. Inbound, §22.5's direct-field-check approach declares only the
fields this connector reads, and the nested offer must not depend on `@type`
at all — the TCK's own source marks that field
`@DspTestingWorkaround(Remove @type)`. Outbound, the TCK validates what this
connector emits against `negotiation/contract-schema.json`, where an offer
is a `MessageOffer`: `@id` and `@type` both required, and an `anyOf` that
needs a `permission` (or `prohibition`) array of `minItems: 1`. Reusing the
lean inbound struct as the outbound body is exactly the defect the first real
`CN_C` run found — all 16 tests were rejected before the negotiation could
start, with `required property 'permission' not found, required property
'prohibition' not found, required property '@type' not found`. Every offer
this connector emits *in a negotiation message*, in either role, now goes
through one constructor, `newNegotiationOffer` — `buildOfferMessage`,
`buildConsumerRequestMessage`, and `buildCounterRequestMessage`. The
catalog's `hasPolicy` offer is a different node and stays where it is, built
inline by `buildDataset` in `catalog.go`: it is that file's own `Offer` type,
not a `NegotiationOffer`, because the catalog schema *forbids* the `target`
`newNegotiationOffer` always sets. Anyone changing the shape of an emitted
offer therefore has two places to look, not one.

*Trade-off accepted.* Two Go types for one DSP message, which has to be kept
in mind whenever that message's shape changes. The alternative — one struct
carrying every field — would have forced the inbound path to declare an
offer `@type` it is specifically forbidden to rely on. Note also what the
error taught: the `anyOf` names both `permission` and `prohibition` because
the whole branch failed, not because both are required, and `minItems: 1`
means emitting an empty `prohibition` to "satisfy" it would turn a valid
message invalid.

## 25. Transfer process (provider role), Phase A

**Decision.** Eight decisions taken while implementing the transfer process
protocol's provider role. Phase A is the control plane only: this connector
runs a transfer's lifecycle and moves no bytes. That scope line is
load-bearing rather than cosmetic, because the `TP` suite does not move bytes
either — no test in it sends, receives, or asserts one — so a green `TP`
cannot be cited as evidence that data transfer works. The data plane is Phase
B, planned separately, and it will raise no number in the gate.

Nothing in §23 or §24 is reversed. §23.7's retry schedule and §23.8's
dispatch-on-a-goroutine rule are reused unchanged, and §23.12's
push-then-store ordering is what the autonomous driver in 25.7 walks.

**25.1 A `TransferRequestMessage` naming an agreement this connector has no
record of is rejected with `400`.** The alternative — accept it and record the
gap — is the shape §24.6 took in the previous milestone and had to be
reversed, and the stakes here are higher than a policy constraint: accepting
an unknown `agreementId` means a counterparty can start a transfer by citing a
contract that was never made. That is `CLAUDE.md`'s "never accept a constraint
that is not enforced" applied to the contract itself. `400` and not `404`,
which is a standing rule of this connector rather than one this document
numbers anywhere: **every rejection a DSP endpoint emits is in `[400, 500)`
and never `404`, and `404` means only that the `{id}` names nothing.** The
reason is the counterparty. Its own client checks for `404` *before* it
consults whether an error was expected, so a `404` raises immediately and
aborts the exchange instead of being read as the refusal it is. The evidence
is in the wire contract's §1.6
(`docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md`),
and this milestone's plan carries it as a constraint.

The agreement is matched by id alone — **until §32, which makes that
conditional.** The reason for not requiring the request's `consumerPid` to
match is unchanged and still holds: that would reject every imported
agreement, since an imported agreement deliberately carries no consumer pid —
the negotiation that produced it did not happen here — and imported agreements
are exactly the case the import path exists to serve. What §32 adds are two
conditions that are not the consumer pid. The agreement must not be one this
connector holds as consumer (§32.4), and, *when it names a counterparty*, that
counterparty must be the verified issuer of the request (§32.2). Both leave
this paragraph's case intact: an imported agreement with no named owner is
still matched by id alone.

*Trade-off accepted.* The TCK sends a random UUID as `agreementId` unless a
fixture pins one, so this decision is what forces the harness to seed
agreements before the suite runs (25.8) — the validation is the reason the
harness is more complicated than it would otherwise be. A connector that
skipped the check would need no seeding at all. It would also start transfers
under contracts that do not exist.

**25.2 An agreement is a row in `agreements`, with exactly three writers, and
not a list in the config file.** Two at this milestone, **three since §24**:
negotiation reaching `AGREED` in the provider role, because that is the moment
this connector issues the agreement document; import through the management
API, which records an agreement concluded elsewhere with
`origin = 'imported'`; and a consumer-role negotiation accepting a remote
provider's `ContractAgreementMessage`, with `origin = 'agreed'`. The third
arrived with the consumer role after this section was written, and
`store.Agreement`'s own doc comment has said three since. §32.4 turns that
third origin into an authorization fact rather than only a provenance record.
An earlier draft put externally-concluded agreements in configuration instead,
and the way that was wrong is worth keeping: **an agreement is runtime state,
not a static declaration of what this connector advertises.** Putting it in
the config file creates a second source of truth for one concept and makes
"edit a YAML file, restart" the way contracts come into being. The tell was
that the design needed a warning attached to that list — *writing an id here
creates a contract* — and a design that needs a warning is usually the wrong
design.

That boundary is the general rule, and it is stated here so a later milestone
does not have to re-derive it: **runtime state goes in the database, static
declarations of what this connector offers go in configuration.** §22.1 put
advertised datasets in configuration by the same rule read from the other
side. Inferring an agreement instead — scanning `negotiations` for a matching
provider pid — was rejected for a third reason: it leaves an
externally-concluded agreement unrepresentable, having no negotiation row to
be found in.

*Trade-off accepted.* Importing an agreement now needs a running connector, an
authenticated HTTP call, and therefore the whole of 25.3 — where a config list
would have needed a struct and a loop. The weight is real either way; this
version puts it where an operator action belongs instead of in a file that
nothing authenticates.

**25.3 The management API's authentication was stood up in this milestone, and
`POST /agreements` records an agreement and nothing more.** §11 settled that
the management API takes one static bearer token — but only as a decision. No
authentication existed: `internal/mgmt` was `NewRouter()` with no arguments
serving `/health` alone, and `config.Config` had no token field. So this
milestone did not add an endpoint to an authenticated API; it built that API's
auth model for the first time — a config field with its own validation, a
constant-time bearer check, a router that now takes config and store, and the
wiring in `cmd/dsbox`.

"And nothing more" is now true of the endpoint rather than of the API. The
data-plane milestone added a read side, `GET /agreements`, because an operator
otherwise had no way to learn the agreement id a transfer has to cite — and
§32.5 put the counterparty into it, because an operator who cannot see who an
agreement is with cannot audit a check that depends on it. It reads and does
not write, so the rule in the trade-off below is unchanged.

The alternative weighed was a CLI subcommand writing to the store directly,
which needs no auth, no HTTP surface, and no config field. The management API
was chosen because §11 had already committed to it and an operator importing
an agreement is a real need rather than a harness convenience.

*Trade-off accepted.* A write path into this connector that is not a DSP
message now exists, and its blast radius is bounded by a rule rather than by
the code: **this is not the start of a general management CRUD surface.** A
later milestone that wants one argues for it on its own merits. The concrete
guard is that `POST /agreements` takes two required strings and one optional
one — §32.5's `counterpartyId` — and writes one immutable
row — there is no update path and no delete path anywhere in this connector,
which is also what makes the duplicate-detection re-query in
`importAgreement` sound, and what that function's comment says will stop
holding if a delete path is ever added.

**25.4 An absent `mgmt_token` makes the management API refuse every
authenticated request, rather than allow them.** The check is written so that
an empty configured token fails before the comparison rather than by falling
through it. A missing credential must never read as "no auth required": the
management listener binds to localhost by default (§12), and if absent meant
open, then changing `mgmt_addr` — one line, in the direction an operator moves
when they want remote access — would silently turn an unauthenticated write
endpoint onto the network. `/health` stays unauthenticated either way, because
a readiness probe should not need a credential.

*Amended by §36 (2026-08-25).* This used to add "because it carries no
information", and that is no longer true: `/health` answers `503` with
`{"status":"roster expired"}` once this connector's roster has expired
(`internal/mgmt/router.go`). The reason it stays open is unchanged and is the
half that was load-bearing — a probe must not need a credential — and a probe
that could not see this would keep a connector in rotation that can serve no
counterparty. §36.7 accepts the disclosure and says what it is.

*Trade-off accepted.* A connector started with no token has a management API
that answers `401` to everything, which will read as a bug the first time
someone hits it. That is paid for with a startup warning rather than by
relaxing the rule. The `mgmt_token` minimum length is a second, smaller
instance of the same posture: a placeholder value fails at load rather than in
production.

**25.5 The transfer states are their own constants, separate from the
negotiation states even where the strings are identical.** `REQUESTED` means
different things in the two protocols, and `TransferRequested` and the
negotiation's requested state are not interchangeable even though both are the
string `"REQUESTED"`. Sharing one set would let a wrong-protocol comparison
compile silently and pass every test that happened to use a state name the two
protocols agree on.

*Trade-off accepted.* Two constant blocks holding overlapping strings, which
looks like duplication to anyone reading them side by side and will invite a
tidy-up. It is not duplication: they are two vocabularies that happen to share
spellings.

**25.6 Phase A emits no `dataAddress` on its `TransferStartMessage`.** Not
merely "does not need to" — emitting one is strictly worse. The field is
optional at every layer that could demand it (schema, predicate, and
null-handling, all confirmed against the pinned TCK), and no provider-role
test asserts anything about it. Sending one instead activates
`data-address-schema.json`, which then requires `@type: "DataAddress"` and an
`endpointType` read in `@id` form — two new ways to fail for zero assertion
benefit. It would also be a claim: a `dataAddress` says where the data is, and
in Phase A there is no data.

*Trade-off accepted.* Whoever gives this connector a real HTTP-PULL data plane
has to put the field back and satisfy that schema shape at the same time, and
will not have a working example in the repository to copy. The
`TransferStartMessage` type carries a comment saying exactly that, so the cost
is a paragraph to read rather than a discovery to make.

**25.7 The connector drives its own transitions after accepting a transfer,
along a sequence configured per agreement — and the implementation plan missed
this entirely.** This is the largest thing this milestone got wrong on the
first pass, and it is recorded as a correction rather than quietly absorbed,
because the way it was wrong is more instructive than the fix.

The plan assumed the provider pushes one `TransferStartMessage` and thereafter
only reacts to what the counterparty sends. Reading the `TP` suite out of the
pinned runtime falsified that. In the `TP:01-xx` tests the TCK sends this
connector exactly one message — the `TransferRequestMessage` — and afterwards
only polls `GET /transfers/{id}`. There is no trigger, no control call, no
out-of-band nudge. `TP:01-04` requires the provider to start, suspend,
restart, and complete a transfer of its own accord, with nothing inbound
between the request and the end of the test.

Two consequences, and the second is the one that is easy to miss:

1. The connector must be able to emit a provider-initiated suspension,
   completion, or termination on a transfer of its own.
2. **Starting must itself be conditional.** Four provider tests carry no
   "provider started" step at all — `TP:01-05`, `TP:02-05`, `TP:03-01`,
   `TP:03-02` — and poll for `REQUESTED`. An unconditional start does not
   merely fail to help there; it breaks them. `TP:03-01` and `TP:03-02` assert
   `REQUESTED` twice, the first time immediately after the request.

The only test-varying field on the wire is `agreementId`, so that is what
selects the behavior — the same shape §24.4's `consumer_policies` already
uses, where `dataset_id` selects a policy. `config.TransferPolicy` is a list
keyed by `agreement_id`, each carrying the sequence of states this connector
walks on its own. An agreement with no entry gets `[STARTED]`; an entry with
an empty sequence means accept and stay in `REQUESTED`, which is a different
thing from having no entry and is the only way to say it. Each step pushes its
message through `pushCallback` and then advances the stored state — §23.12's
ordering, unchanged, for the reason §23.12 gives: in DSP the provider does not
become `STARTED` and then say so, it becomes `STARTED` by delivering the start
message.

**A configured step is checked against the same legality table an inbound
message is, and an illegal one ends the sequence.** Configuration validation
can only check that each element names a known state; whether a step is legal
depends on where the previous one left the transfer, which is runtime. So
`sequence: [COMPLETED]`, `[STARTED, STARTED]`, and `[TERMINATED, STARTED]` all
load cleanly and would otherwise emit messages this same connector answers
`400` to when they arrive from the other side. Enforcing the table outward but
not inward makes it advisory, which is `CLAUDE.md`'s "never accept a constraint
that is not enforced" read from the side that is easier to forget. The check
lives in `dsp`, beside the table, rather than in configuration validation:
`dsp` imports `config`, so the reverse would be an import cycle. It ends the
sequence rather than skipping the step, because a sequence that has gone
illegal has no meaningful remainder. Every shipped fixture is legal, so nothing
this connector does today changes; a misconfigured deployment does.

The steps are spaced by `transferStepDelay`, 200 ms. That is not a robustness
nicety either: the counterparty registers its handler for step N+1 only after
step N has arrived and released its latch, so two messages pushed back to back
can hit a path with no handler yet, which its callback endpoint answers `404`.
§23.7's retry schedule is the second line of defence, not the first.

*Trade-off accepted.* A configured script is a stand-in for judgement this
connector does not have. A real provider suspends or completes a transfer for
operational reasons — the data ran out, the window closed, an operator
intervened — and v1 has none of those inputs, exactly as v1's consumer role
has no reason of its own to reject an offer and takes that from
`consumer_policies` instead. The honest framing is the same in both places:
this is the connector's *configured* autonomous behavior, and the
configuration is where a real reason would eventually plug in. The narrower
cost is that `transfer_policies` reads like a test fixture in
`config.example.yaml`, because in this milestone that is mostly what it is.

Two further consequences follow from the choice of key and the choice of
clock, and neither was named above:

- **The selector cannot address a negotiated agreement.** A negotiated
  agreement's id is the issuing negotiation's provider pid — that is the id
  `buildAgreementMessage` puts on the wire and the one the row is written
  under, in `negotiation_handler.go` — and it does not exist until that
  negotiation is under way, so nobody can write it into a configuration file
  beforehand. `transfer_policies` is therefore usable only for *imported*
  agreements, whose ids the operator chose — which is exactly the TCK's
  situation and nobody else's today. A deployment that wants autonomous
  behavior on agreements it negotiated needs a different key, and there is no
  obvious one on the wire: `agreementId` is the only test-varying field, and
  `datasetId` is not carried on a `TransferRequestMessage` at all.
- **The sequence is driven by a clock, and Phase B makes state the access
  control.** The only thing between two steps is `transferStepDelay`.
  `[STARTED, COMPLETED]` therefore completes 200 ms after starting, which is
  fine while there is no data plane and wrong the moment there is one: `STARTED`
  is what will authorize a pull, so a terminal step on a timer cuts access
  before a byte could be fetched. Phase B has to either bound autonomous
  completion by something the data plane knows, or say plainly that a terminal
  step and a real `source_file` cannot be configured together. Tracked in
  `docs/follow-ups.md`.

**25.8 The TCK harness imports its fixture agreements through the management
API, in `run.sh`, and fails the run loudly if any import does not answer
`201`.** 25.1 makes seeding unavoidable; putting it in the harness script, in
the open, keeps it a visible part of the setup rather than a config convention
somebody has to infer. It runs against the already-published management port,
after the existing `/health` readiness loop.

The loud failure is the point. A connector that rejects an unknown agreement
and a connector that was never given the agreement both answer `400`, so a
silent seeding failure would be indistinguishable from a protocol bug for the
whole run.

The first real run settled the two values the plan marked unconfirmed, and
both turned out as predicted. The `@ConfigParam` key is
`<TEST METHOD NAME UPPERCASED>_<FIELD NAME UPPERCASED>`, per test method with
no class-level fallback: all fifteen `TP` transfer requests arrived carrying a
configured agreement id and none a random UUID, which is conclusive because an
unread key falls back silently to a random UUID and no random UUID is ever
seeded. The same count settled the gate. `urn:uuid:tck-tp-default` is
configured for eight test methods and arrived exactly seven times — the
missing eighth is `TP_02_04`, which carries JUnit's `@Disabled` and never ran.
The suite declares 16 `@MandatoryTest` methods and produces 15 results, and
the gate counts results, so `"TP": 15`.

*Trade-off accepted.* The fixture is spread across three files that must agree
— `config.properties` names an agreement per test, `run.sh` seeds it, and
`dsbox.yaml` keys its autonomous sequence on it — and nothing checks that they
agree except the run itself. Each file says so in a comment. The alternative,
generating all three from one source, would put a code generator into a test
harness to save seven lines.

## 26. Policy constraints (validity period)

**Decision.** §14 already fixed v1's scope to two policy shapes; this
milestone is the first to build the second one rather than only reserve it.
It closes two accounts §23.4 and §24.6 opened and left open on purpose:
§23.4's validity period was "this connector's own... not an ODRL constraint
evaluated from an inbound message — no code in `internal/dsp` evaluates
one", and §24.6's guard was "presence-only, so it cannot one day 'allow the
validity-period constraint through' without a real evaluator first
existing". Both statements describe code that no longer exists in this
form; this section is the pointer forward rather than an edit to either.

**26.1 One helper builds every permission this connector emits for its own
datasets.** `buildPermission(validityUntil *time.Time) []Permission` — nil
returns exactly `buildPermission`'s pre-existing output (`Permission{Action:
useAction}`, no `constraint` key), so every dataset with no
`validity_until` is byte-identical to every wire shape this project already
produced. Non-nil attaches one constraint element: bare (unprefixed)
`leftOperand: "dateTime"`, `operator: "lteq"`, `rightOperand` the RFC 3339
form of the value — the same "rely on the DSP `@context`'s imported ODRL
vocabulary" convention `useAction = "use"` already established. Three
builders that used to write `Permission{Action: useAction}` inline now call
it: the catalog offer (`buildDataset`), the negotiation offer's provider-role
call site (`buildOfferMessage`, via `newNegotiationOffer`'s new
`validityUntil` parameter), and the agreement (`buildAgreementMessage`). The
two consumer-role call sites that echo a *remote* provider's offer back to
it (`buildConsumerRequestMessage`, `buildCounterRequestMessage`) always pass
`nil`: this connector's config has no `ValidityUntil` opinion about a
dataset it does not advertise.

**26.2 §24.6's presence-only guard becomes a one-shape evaluator, and the
rest of that section's reasoning is unchanged.** `carriesConstraint`
(any constraint present → reject) is replaced by `hasUnenforceableConstraint`,
which is presence-only for everything except the one shape 26.1 builds:
`isValidityPeriodConstraint` requires exactly one constraint element with a
matching `leftOperand`/`operator` and a `rightOperand` that parses as RFC
3339. `decideOfferReaction`/`decideAgreementReaction` keep their exact
signatures; only what the bool means changes, from "a constraint is
present" to "an unenforceable constraint is present". §24.6's own limits
still hold verbatim: this looks only at `permission`, not `prohibition`, and
the guard is still a rejection, not an interpretation, for anything outside
the one recognized shape — including a well-formed validity-period-shaped
constraint whose `rightOperand` fails to parse, which is unenforceable
exactly as an unrecognized `leftOperand` is.

**26.3 The data plane, not only negotiation, enforces the window — the one
check that did not exist in any form before this milestone.** Every prior
check ran once, at negotiation time (§23.4's accept-time check; `isValid`
generally). Nothing re-checked anything after a transfer reached `STARTED`,
so a transfer that started while an offer was valid kept serving bytes on
every future pull regardless of what the dataset's `validity_until` said by
then. `handleData` now resolves the dataset a transfer's agreement covers —
`datasetFor`, replacing the narrower `sourceFileFor` so `SourceFile` and
`ValidityUntil` come from one lookup rather than two — and refuses with
`409` once `now` is no longer before `ValidityUntil`, in the same status
family as the sibling "wrong state" and "no `source_file`" checks it sits
beside: a currently-true precondition failed, not a structural or ownership
problem.

**26.4 The lookup is live config, not a value captured at negotiation
time.** `datasetFor` reads `h.cfg.Datasets` on every call, the same "config
is an operator declaration, re-read on every request" choice `buildCatalog`
already makes for the catalog document. `store.Agreement` gains no new
column. The alternative — snapshot `ValidityUntil` onto the agreement at
negotiation time — would let an agreement outlive a config edit meant to
revoke it sooner, which is the wrong direction for a security-relevant
value to be stale in.

*Trade-off accepted.* An operator who *extends* `validity_until` after an
agreement was struck extends every transfer already running under it, with
no distinction between "this consumer's specific agreement" and "this
dataset's current advertised terms" — the agreement's own wire copy of the
constraint is not what gets checked, the live config is. Revisiting that
distinction is real future scope, not a defect this milestone leaves
undocumented.

**26.5 Verified against the TCK specifically for the regression this milestone
could plausibly cause, not only against the aggregate count.**
`test/tck/dsbox.yaml`'s `urn:dataset:cn-expired` already carried
`validity_until` in the past, for `CN:02-01`, `CN:02-05`, `CN:02-06`'s
expired-offer paths — so 26.1 means those tests are the first ones ever to
see an Offer this connector pushes carrying a constraint on the wire.
`docs/milestone-sequence.md`'s finding that "the CN suites negotiate
unconstrained offers" predates this milestone and does not by itself cover
it. All three tests were re-run and pass; the gate stays 64 of 65, one
tracked exemption, unchanged.

## 27. Roster signing and did:web resolution

**Decision.** §9 decided both pieces already and built neither. The
connector-auth milestone's design spec named the gap and narrowed it on
purpose: "a roster fetched from anywhere other than local disk is not safe
under this milestone, and adding the signature is the prerequisite for
distributing one." This milestone adds the signature and a real `did:web`
resolver, and does not reopen the auth spec's other decision: resolution
still has no part in answering "is this request authenticated" — see 27.4.

**27.1 The roster is signed by a key that is not any participant's.** A new
config value, `roster_signer` — a bare Ed25519 public key, not a file path,
required whenever authentication is on. Not a participant's key: the roster
is the registry itself, and signing it with an entry's own key would let
that entry vouch for its own trustworthiness. It sits in `config.yaml` under
the same "this file's integrity is already assumed, on the same disk, in
the same deployment" reasoning the auth spec already gave for the roster
file itself.

**27.2 What gets signed is a re-marshal, not the file's own bytes, so no
canonical-JSON scheme was needed.** `roster.json` gains a `signature` field.
The bytes signed are `json.Marshal` of the parsed document — Go's
`encoding/json` marshals struct fields in declaration order, so this is
deterministic for a fixed struct without a canonicalization spec. The
signer and `LoadRoster` both compute it from their own parsed value, so
reformatting the source file (whitespace, key order) cannot change what is
checked, and only the file's actual content can.

*Amended by §36 (2026-08-25).* This said "the parsed `[]rosterEntry`" and
"the file's actual content — the participants". Both are narrower than the
code now is: `canonicalRosterBytes` in `internal/auth/roster.go` marshals a
struct carrying `participants`, `version`, and `expires_at`, in that order,
so each of them is inside the signature and none can be edited without
breaking it. The `signature` field itself is outside, which is what lets the
signer compute over a file that does not carry one yet. The argument for a
re-marshal over canonical JSON is unaffected — it turns on declaration order
for a fixed struct, and the struct is still fixed.

**27.3 `dsops roster sign` prints the signature; it does not rewrite the
file.** `dsops`'s own package doc already states why: "a command that
rewrote \[the roster\] would put a tool between the operator and a file
they are meant to read." Signing is mechanical, unlike deciding who belongs
in the roster, but the shape stays consistent with `keygen` and `token` —
print, and the operator pastes it in — rather than carving an exception
into a principle stated once for a reason.

**27.4 `did:web` resolution is real and is nowhere near the authentication
path.** `ResolveDIDWeb` fetches a DID document over HTTP(S) and reads an
Ed25519 key out of a `publicKeyJwk` verification method — JWK (RFC 7517),
narrowed to OKP/Ed25519 (RFC 8037), not `publicKeyMultibase`: multibase is
base58btc with a Multicodec header, and this project's default answer to a
dependency question is the standard library, which decodes JWK's base64url
`x` already and would need a base58 decoder written for multibase to save a
few bytes over JWK. It is exposed as `dsops resolve <did:web:...>`, printing
a key in the same shape `keygen` does, for an operator to paste into a
roster entry — theirs from `keygen`, a counterparty's from `resolve`,
identically. It is never called from `internal/dsp`: the roster already maps
identifier to key, so resolving on every request would add a network
dependency to authentication and change nothing about who ends up trusted —
the auth spec's own reasoning, unchanged by this milestone existing.

**27.5 `-allow-http` mirrors `dev_mode`'s reasoning without touching
`config.DevMode`.** `didWebURL` takes a plain `bool`, not a `config.Config`:
`internal/auth` takes no dependency on `internal/config`, and resolution is
a CLI-driven operator action, not something a running connector's config
shapes. `dsops resolve -allow-http` builds an `http://` URL instead of
`https://`, for the same reason §13 already relaxes `public_url` under
`dev_mode: true` — local demos and tests with no TLS to terminate — without
being the same field or the same code path.

*Trade-off accepted.* Two problems this milestone does not solve, stated
rather than left implicit. First, `roster_signer`'s own distribution is the
same bootstrap problem one level up: a signature protects the roster once a
connector has a copy of both it and the right signer key, and says nothing
about how either first reaches a new connector — still "diffed in git",
still local disk, still a governance question and not a cryptographic one.
Second, no key rotation path: a `did:web` document that changes its key
propagates nowhere on its own, and the roster stays a static, manually
refreshed cache of whatever `resolve` last returned. `make demo` and
`make tck` both now sign the roster they build (a `dsops roster sign` step
`run.sh` in each carries); neither exercises `did:web` resolution live —
`ResolveDIDWeb`'s own tests, against `httptest.Server`, are the evidence for
that half, and folding it into either harness would mean standing up a
document server neither actually needs, for proof the unit tests already
give more directly.

## 28. Replay defense is not built, and closing it as originally scoped is not possible without leaving §10

**Decision.** Do not add `jti`-based single-use token enforcement. The
connector-auth design spec's own accepted trade-off — "Replay inside the
five-minute window... Closing this needs storage and expiry sweeping" —
described a real gap, but its implicit premise, that a token is meant to be
presented once, does not hold for a client this connector must interoperate
with: the official TCK. This section replaces that framing with why, rather
than editing §10 or the design spec's original text.

**Rationale.** `test/tck/run.sh` mints exactly one token per suite run and
sets it as a static configuration property, `dataspacetck.dsp.connector.
http.headers.authorization`, that `DspSystemLauncher` attaches to every
request the TCK makes: it is registered once as a process-wide interceptor
and cannot be refreshed mid-run. (This paragraph used to quote a comment in
`test/tck/run.sh` saying so. §35.4 rewrote that comment block — the same
property is now what forces one string to satisfy both listeners — so the
fact is stated here rather than attributed there.) A TCK run is dozens of
authenticated requests across 65 tests, all carrying the identical credential. That is not
an artifact of this project's harness; it is the TCK exercising the
credential the way DECISIONS.md section 10 designed it to be used — a
short-lived *bearer* token, valid for repeated calls within its window, not
a one-time-use artifact.

Not left as an inference from that comment: `jti`-based single-use
enforcement — a `Jti` claim, `store.RecordCredential` tracking one row per
credential, and the auth middleware refusing a second presentation with a
401 and a log line — was built in full and run against the real pinned TCK
image. Result: 63 of 65 required tests failed, everything past the second
authenticated request in every suite. The implementation was then reverted
in the same session rather than kept behind a flag or left disabled;
`go test ./...` and `make tck` (64 of 65, unchanged) were re-run clean
afterward. This project's own "mutation testing as verification" habit,
applied here as "run it against the real gate before believing a
description of what it does."

The asymmetry that made this easy to miss: this connector's own outbound
code already mints a fresh token per HTTP attempt (`pushCallback`'s "minted
per attempt, not once per call"), so nothing in this connector's own
behavior ever exercises a token twice. The TCK, playing the counterparty,
does — and the TCK is the authority on what a conformant client does, not
this connector's own habits.

No mechanism this milestone considered closes the real gap — a captured
token usable by anyone until it expires — without either rejecting
TCK-conformant behavior or requiring information this design has nowhere to
get it from. Scoping rejection to "the same `(jti, path, method, body)`
twice" was considered and set aside unverified: the `CN_C`/`CN` suites
include deliberate identical re-requests as their own test subject
(`CN:03-04`'s "two re-requests carrying the *identical* offer, first
succeeds, second is rejected" — by the negotiation state machine, not the
auth layer), and adding a second, auth-layer rejection ahead of that logic
risks changing what those tests observe in a way this session did not have
grounds to certify safe.

*Trade-off accepted.* A captured credential remains usable by anyone holding
it until it expires, five minutes after minting — unchanged from before this
investigation, and, per the reasoning above, not closable by storage and a
sweep alone as originally scoped. What bounds the exposure is §10's own
choice of window length and the assumption TLS terminates in front of this
connector in any deployment where interception is a real threat (§13); this
milestone does not reopen either. Real replay defense for a reusable bearer
token needs proof-of-possession (DPoP, mTLS) or a token model this project
has not chosen, which is a §10-level decision, not an implementation detail
— `CLAUDE.md`'s rule applies: ask before working around a decision
`DECISIONS.md` already made, and this section is that ask answered "not yet,"
recorded rather than worked around.

## 29. `CN:02-07` closed: VERIFIED's choice of FINALIZED or TERMINATED is a dataset declaration

**Decision.** `config.Dataset` gains `TerminateOnVerify bool` (default
false). `handleVerification` looks it up by `n.DatasetID` — the same
accessor `isValid` already uses for `ValidityUntil` — and pushes a
termination instead of the `FINALIZED` event when it is true. The gate is
65 of 65, no exemptions, as of 2026-08-21.

## 30. Three data-plane integrity gaps closed from `docs/follow-ups.md`

**Decision.** Three items the transfer-process (provider, Phase A) and
contract-negotiation (consumer) milestones deferred, closed together because
the second was mostly a re-derivation of a decision the code had already
made, and the third became live only once the first two were true.

**30.1 A consumer-role agreement whose `target` differs from the dataset this
connector actually requested is rejected, not verified.**
`handleAgreement` recorded `msg.Agreement.Target` into the `agreements` row
while `reactToAgreement` decided verify-or-reject from `n.DatasetID` — what
this connector itself asked for in `POST /negotiations/initiate` — and the
two were never compared. Harmless when nothing downstream read the
agreement; live now that Phase B does (`data_handler.go`'s `datasetFor`) and
now that `POST /transfers/initiate` starts a transfer from an agreement id
alone (§25.1's rule, read from the consumer side). A provider that swapped
the target could get this connector to evaluate the wrong dataset's policy
and later fetch data under a contract it never meant to hold.

The fix is `decideAgreementReaction`'s existing shape, widened by one bool:
`unenforceable || wrongTarget` both divert `on_agreement: verify` to
`reject`, computed in `handleAgreement` as
`msg.Agreement.Target != n.DatasetID` and passed down alongside
`unenforceable`, the same way §24.6 already treats "a term this connector
did not write." The offer path (`handleOffers`/`reactToOffer`) is
deliberately left alone: accepting an offer sends only an event referencing
this connector's own state, so a mismatched offer target commits nothing
durable the way a mismatched agreement target does — there was no matching
gap to close there.

*Trade-off accepted.* The agreement row still stores the message's true
`target`, not the requested one — recording what was actually agreed to
before recording that it was agreed, per §25.2, even on the branch that then
rejects it. Storing the requested value instead would misrepresent the wire
content for a debugging session that later needs to see what the provider
actually sent.

**30.2 `POST /agreements` still accepts any non-empty `datasetId` — no code
changed, because Phase B had already answered the question the follow-up
posed.** The follow-up asked whether an unknown `datasetId` should be a
`400` at import time or a failure at pull time. `data_handler.go`'s
`datasetFor` already resolves an agreement's dataset id against live
`config.Datasets` on every pull, and answers "not configured" with a
distinguishable `409` and its own log line — the same "config is an
operator declaration, re-read on every request" rule `ValidityUntil`
already follows, not a value snapshotted at negotiation or import time.
Rejecting at import time would contradict that rule: an operator can import
an agreement before the matching dataset is configured, or after it is
removed and re-added, and none of that is an error either side needs to
know about until a pull is actually attempted. This section exists to
record that the decision was already made by existing code, so the
follow-up entry could close without one being invented.

**30.3 The provider-role agreement guard's residual race is closed by one
transaction, not left as a documented residual.** `negotiation_handler.go`'s
`dispatch` used to re-read a negotiation's state and then, in a second call,
insert its agreement row — a termination landing between the two left a
stale `agreements` row for a dead negotiation, a gap the code accepted in a
comment rather than closed, on the reasoning that closing it "needs one
transaction spanning the state write and the agreement insert, which is a
larger change than this call site should make."

That transaction turned out to be small: `store.Open` already pins the
connector to a single `*sql.DB` connection
(`SetMaxOpenConns(1)`), so a `*sql.Tx` spanning the `SELECT` and the
`INSERT` holds that one connection from open to commit, and no concurrent
`SetState` can land in between — the same guarantee the single-connection
design already gives every other store method, applied across two
statements instead of one. `Store.CreateAgreementIfNegotiationAgreed`
replaces the two-call sequence with this one transaction; `dispatch`'s
`pushAgreement` branch is otherwise unchanged, including which three states
(`AGREED`, `VERIFIED`, `FINALIZED`) count as proof the agreement was
issued.

*Trade-off accepted.* None beyond what the single-connection design already
costs elsewhere: this transaction, like every other store call, briefly
serializes against the rest of the connector's writes. At this project's
scale that cost was already being paid; this is the first place it is paid
across two statements instead of one.

**Rationale.** Two prior accounts of this gap were each half right and
half wrong, corrected in order rather than by editing either:

§25.5's premise — "`CN:02-07` needs a termination trigger that fires after
a negotiation has already passed [the accept-time] check once" — assumed a
*trigger* was the missing piece. Decompiling the pinned TCK's own
`cn_02_07` test method
(`org.eclipse.dataspacetck.dsp.verification.cn.ContractNegotiationProvider02Test`)
and diffing it against `cn_03_01` (`ContractNegotiationProvider03Test`) —
the sibling test that reaches `FINALIZED` normally — found the two send a
*wire-identical* verification: same dataset, same generic
`ContractAgreementVerificationMessage` body. Nothing in what the TCK sends
distinguishes one from the other. There is no trigger to find because there
is nothing to detect.

What actually decides it is the protocol's own state machine. DSP 2025-1's
published negotiation state diagram
(`figures/contract.negotiation.state.machine.puml`) declares both
`VERIFIED -> FINALIZED` and `VERIFIED -> TERMINATED` as legal,
provider-initiated transitions — `P`, not derived from any consumer
message. The TCK's own `@TestSequenceDiagram` annotations on the two
methods confirm the same split: `cn_02_07` ends
`CUT->>TCK: ContractNegotiationTerminationMessage`; `cn_03_01` ends
`CUT->>TCK: ContractNegotiationEventMessage:finalized`. Two mandatory tests
exist to confirm a conformant provider can produce *either* — not to
assert which one a given verification requires. That makes the choice an
operator declaration by construction, the same shape as `ValidityUntil` and
`SourceFile` already are on `Dataset`, and the same shape `transfer_policies`
already is for the transfer protocol's own provider-discretion points —
negotiation had no equivalent hook before this section.

**29.1 A second dataset, not a second field on the existing one.**
`CN:02-07` and `CN:03-01` need *opposite* outcomes from otherwise identical
requests, and the request is the only thing observable before an agreement
id exists — so the two tests cannot share `cn-match` the way `CN_01_04` and
`CN_02_03` already do. `test/tck/dsbox.yaml` gains
`urn:dataset:cn-verify-terminate`: matches immediately like `cn-match`,
differs only in carrying `terminate_on_verify: true`. `CN:03-01` keeps
`cn-match` unchanged.

**29.2 What was actually broken before either investigation started.**
`docs/follow-ups.md`'s original account of this gap asserted `CN:02-07`'s
"sequence reaches a clean `AGREED`" — a claim never checked against a live
run, and false: `test/tck/config.properties` had no
`CN_02_07_DATASETID`/`OFFERID` override, so the TCK's random,
unadvertised default meant `decideInitialRequest` returned `outcomeNone`
and the negotiation never left `REQUESTED`. `CN:02-07` was failing on a
config gap unrelated to termination, before ever reaching the scenario its
own name describes. Fixed by giving it the same matching-pair treatment
`CN_01_04`/`CN_02_03` already had, pointed at the new dataset. Worth naming
because it is the second time this project has found a "the TCK is
expected to fail here anyway" comment that turned out to be an assumption
standing in for a check (docs/follow-ups.md's original `CN_02_02` comment
was correct; this one, reasoning by analogy from it, was not) — the fix in
both cases was running it rather than trusting the analogy.

*Trade-off accepted.* None beyond what §14 already accepted for
`transfer_policies`'s existence: a config surface with no purpose in a real
deployment (an operator has no reason to advertise a dataset whose
agreements always terminate) — a test affordance, declared as one.

## 31. Transfer robustness: `Range` support and resumption

**Decision.** The provider's data endpoint supports one `Range` form,
`bytes=N-`: `206 Partial Content` when `0 <= N < size`, `416 Range Not
Satisfiable` when `N >= size`, and today's unconditional `200` for anything
else — no header, an unparseable one, or a form this connector does not
implement (multi-range, a closed range, the `bytes=-N` suffix form), per RFC
7233's own guidance for a range a server does not support. The consumer's
`pullTransferData` writes to a deterministic path,
`downloads/.partial-<consumerPID>`, instead of a random temp name, and on a
restart sends `Range: bytes=N-` for whatever it already has. `206` appends;
`416` discards the partial and starts fresh on the next restart; any other
answer to a resumed pull is logged and leaves the partial untouched — the
one behavior change from before this milestone, where any failure discarded
everything received.

**31.1 Integrity across a resume is a size check, not a content check.** A
partial file at or past the provider's current file size cannot be a valid
prefix of it, so `416` is what tells the consumer "this is not the file I
was receiving." No hash, no `ETag`. A same-size content replacement between
attempts is not caught — accepted, not solved, the same posture
`SourceFile`'s own doc comment already states for a file swapped
underneath the connector.

**31.2 `resolveTransferSequence` gains a dataset-keyed fallback, closing a
gap §25.7 named but left open.** §25.7 recorded that `transfer_policies`
cannot key a negotiated agreement, because that id does not exist until the
negotiation producing it is already under way, and named the absence of any
other wire-observable key as the reason. `config.Dataset.TransferSequence`
supplies the key that was missing off the wire: an agreement's `dataset_id`
is known regardless of whether the agreement was negotiated or imported (the
same lookup `hasSourceFor` already performs). An `agreement_id` match in
`transfer_policies` always takes precedence when both exist. This exists to
let the demo prove resumption against a real negotiated agreement,
but it is not demo-only: it is the general answer to §25.7's open question.

**31.3 A concurrency guard was needed that the old design did not require.**
The deterministic partial-file path (31 above) is a new appendable target
two `pullTransferData` calls could race on, unlike the old random-named
temp file. DSP's own legality table allows a restart to arrive while a
previous pull is still running — nothing ties the provider's autonomous
suspend/restart timer to how long the consumer's own fetch takes.
`transferHandler.pulling`, a `*sync.Map` shared across every copy of the
handler, drops a restart's trigger if a pull for the same `ConsumerPID` is
already in flight, logged the same way a stale state-update elsewhere in
this connector already is, rather than run two writers against one file.

**31.4 Fault injection mechanism: `config.Dataset.SimulateInterruptAfterBytes`
for testing resumption.** `config.Dataset.SimulateInterruptAfterBytes`
truncates a non-`Range` request at that many bytes and severs the
connection via `http.Hijacker`, so test code can force a real
interruption rather than mock one. As of the data-path milestone (§33) the
truncated response still declares the file's **full** `Content-Length`,
because that header is now set once after `Stat` and before the `Range`
branch, so the interrupt path carries it too. What that changes is narrow
and worth stating exactly, because a wider claim was made here first and was
wrong: an interrupted response now promises a length it does not deliver, so
the consumer can tell **how much** it was short by, and a first attempt
records a real expected total instead of none.

It does not change *whether* the client sees an unexpected EOF. A chunked
response severed by `Hijack` never receives its terminating chunk, and
`net/http/internal/chunked.go` turns that `io.EOF` into
`io.ErrUnexpectedEOF` — at a clean chunk boundary as much as mid-chunk — so
both framings fail the read. There is no short-but-well-formed body to
contrast against.

Nor does `make demo`'s resume round depend on this header. The branch that
discards a partial on a changed representation is guarded by
`t.ExpectedBytes > 0`, so a first attempt that recorded nothing cannot trip
it: the resume takes `expected` from the `206`'s own `Content-Range` and
appends. The demo would pass without the header. It is set because an
interruption that declares a length is what a real one looks like, and
because `expected_bytes` is worth recording on the first attempt rather than
only the second — not because anything would otherwise break.
`TestSimulatedInterruptStillDeclaresTheFullLength` pins it.

It never fires on a `Range` request, which keeps the interrupt-then-resume
sequence testable — the field is what `make demo`'s second round exercises.
That round runs against a dedicated
dataset, `urn:dataset:sample-resume`, rather than the original
`urn:dataset:sample`, so the baseline scenario's pass/fail signal stays
unambiguous. It checks two things, not one: a byte-for-byte diff of the
recovered file against what was sent, and a grep of the consumer's log for
the `"resumed transfer data pull"` line the append path logs on a resumed
pull — the diff alone cannot tell a real resume apart from a coincidental
full refetch after a failure, only the log line can. The implementation is
unit-tested in
`internal/dsp/data_handler_test.go`.

*Trade-off accepted.* An orphaned `.partial-<consumerPID>` file — a
transfer that terminates instead of restarting after being interrupted
leaves one behind forever — is not cleaned up. A counterparty that answers
every ranged request with a plain `200`, never honoring `Range` at all,
turns this from an occasional leak into the transfer's only possible
outcome: the `default:` case above refuses to append, so every restart
repeats the identical abort and the transfer can never complete. The only
way out — starting an entirely new transfer — abandons the same partial
file rather than reclaiming it. Pre-existing risk in a smaller form (a
stray random-named temp file could already leak on an unclean process
exit); this milestone makes the leaked file larger and its name
predictable. Tracked in `docs/follow-ups.md` rather than solved here: it
needs a retention policy this project has none of yet.

## 32. An agreement records who it is with, and the exchange endpoints check it

**Decision.** §27 settled *that* a caller is admitted. It did not settle
*which* row an admitted caller may touch — that check existed at exactly one
endpoint, the data endpoint, and everywhere else an admitted participant could
act on any exchange whose id it knew. This milestone makes the identity §27
established load-bearing: five provider-role resolvers refuse a message about
an exchange the sender is not party to, `agreements` gains a counterparty
column so an agreement can be checked at all, and serving data as provider
under an agreement this connector holds as *consumer* is refused outright.

This section supersedes §23.11's closing prediction, and §24.2's by reference.
§23.11 recorded the gap and said how it would close:

> Both consequences are future work, and both are properly closed by enforcing
> §10's connector-to-connector JWT on this listener, not by patching the
> handlers one field at a time.

**The JWT shipped in §27 and did not close it.** It narrowed the attacker set
from anyone on the network to any roster participant, which is not closure: a
roster is shared by parties who are strangers to each other, and that is the
boundary this protocol exists to keep. This section is the thing §23.11 said
would not be needed — the handlers, checked one at a time — and it is here
because the prediction was tested and was wrong. A superseding section rather
than an edit, for the same reason §26, §28, and §30 are: §23.11's judgment at
its own milestone was defensible on what was known then, and only its forecast
is overturned. Both sections carry a pointer here; neither is rewritten.

Severity was high, not critical. Every path needed a roster credential, and
impersonating another participant needs their private key. There is no release
(`git tag` is empty) and no deployment known beyond this repository's own
harnesses. The design spec is
`docs/superpowers/specs/2026-08-23-exchange-authorization-design.md`.

**32.1 A refusal is `403`, never `404`, and the ownership check runs before the
state check.** §25.1 already made "never `404`" a standing rule of this
connector, on the ground that the counterparty's own client checks for `404`
before it consults whether an error was expected, so a `404` aborts the
exchange instead of being read as the refusal it is. The TCK enforces the same
— a `404` is fatal on every path, including ones that expect an error — and
`403` is what this connector already answered for exactly this condition at the
data endpoint. One helper, `refuseIfNotParty` in
`internal/dsp/auth_middleware.go`, writes all five, in the same register the
data endpoint's own "this transfer is not yours" already used.

`403` is an existence oracle: it distinguishes "not yours" from `404`'s "no
such id". Accepted rather than solved. The sibling oracle already exists — a
transfer request citing an unknown agreement gets a distinguishable `400` — and
collapsing this one into `404` would break the rule above.

The ordering is part of the decision. The ownership check runs *before* the
state check, so a prober learns nothing about an exchange's progress from the
refusal. `handleData` already made that choice and a test already pins it; the
five new checks are placed to match.

**32.2 An empty counterparty means "not known", and is permitted — on the
agreement check only.** `counterparty_id` defaults to `''`, and the exchange
tables have carried it that way since §27 for rows that predate anyone to
address. Refusing on empty would be the safer-sounding choice and it is not
available: `test/tck/run.sh` seeds twelve agreements through `POST /agreements`
with no owner, so a deny fails all fifteen `TP` and all fifteen `TP_C` results.
The same holds for any operator who imports an agreement today. So the
agreement comparison is `stored != "" && issuer != stored`.

The exchange checks keep the stricter form, with no empty clause — the form
`handleData` already used. Two reasons for the asymmetry. Nothing forces the
permissive form on exchange rows, since every one the TCK creates carries a
verified issuer, and permissiveness should be bought only where it is required.
And `handleData` must not be refactored through a helper that acquires the
empty clause: a transfer row with an empty counterparty is refused to everyone
today, and adding the clause would serve it to any roster participant.
`refuseIfNotParty` is therefore written *beside* that check rather than
extracted from it, and the existing test that pins `handleData`'s behavior is
left alone.

**32.3 Compare only against a counterparty that came from a verified issuer.**
`counterparty_id` has two sources. Provider-role rows take it from the verified
issuer of the request that created them. Consumer-role rows take it from the
`providerId` field of an operator's own initiate call — a string the caller
chose. Only the first is an identity, and comparing against the second is
comparing against an unverified assertion, which is not authorization.

It also breaks immediately. The TCK harness authenticates as
`urn:participant:tck` while hardcoding `TCK_PARTICIPANT` as the `providerId` in
its initiate hook, so consumer-role rows in the harness hold a string no
inbound request will ever present: enforcing there loses all fifteen `TP_C`
results and all but one of `CN_C`, 65 − 30 = 35. The rule is not a carve-out
for the harness — **a row's counterparty is an authorization anchor only where
this connector verified it** — but the harness is what demonstrates it.

So five points carry the check, all provider-role: `transferHandler.lookup`
(after `GetTransfer` succeeds, where the consumer branch has already returned),
`negotiationHandler.lookup`, `handleProviderAcceptedEvent`,
`handleProviderTermination`, and `handleGetNegotiation`'s provider branch.
`handleOffers` and `handleAgreement` resolve only through `GetConsumer` and are
deliberately unguarded; they are named here because anyone enumerating
resolvers will find them and must know the omission is a decision rather than
an oversight.

Two consequences stay open, recorded rather than discovered. The consumer
role's inbound messages are unauthorized, which needs `providerId` validated
against the roster at initiate time — a separate change with its own
compatibility question. And that same unverified `providerId` becomes the
*audience* of a credential this connector signs, addressed to a caller-chosen
endpoint: an impersonation primitive against a third participant, reachable
through `POST /negotiations/initiate` with no agreement at all. Nothing here
creates it, and it is the sharpest reason the initiate hooks deserve a
milestone of their own.

**Both closed by §35** (2026-08-24), which is the milestone the paragraph
above asks for. §35.2 validates `providerId` against the roster at initiate
time; §35.1 moves both hooks to the management listener, which removes the
impersonation primitive rather than narrowing it, because the primitive needs
an untrusted caller. §35.3 then applies `refuseIfNotParty` at every
consumer-role resolver — including ones this section's own list does not
reach, and §35.3 records why finding them mattered.

**The cost measured above did not have to be paid, and §35.4 records why.**
The result count this section arrives at was never a property of roster
validation. It was the price of the harness authenticating under one name
while sending another, and correcting the harness's own identity is what
dissolves it.

**32.4 Serving data as provider under a consumer-role agreement is refused, and
that is where the forging path closes.** A roster participant can initiate a
negotiation naming itself as provider, read this connector's own consumer pid
out of the request that arrives, post a `ContractAgreementMessage` to it, and
cite the resulting row in a transfer request. `handleAgreement` writes the body
verbatim, so the row is real, and every ownership check above passes on that
path because the forger is the honest owner of what it forged. Ownership checks
around a mintable agreement produce confidence, not security.

The fix needs no new state. `store.Agreement` already records `Origin`, and it
answers the question directly: `OriginNegotiated` means this connector is the
provider — serve; `OriginImported` means an operator asserted it — serve;
`OriginAgreed` means this connector is the **consumer** — never serve as
provider. One predicate, `servableAsProvider`, gates all four provider-role
readers of an agreement (`handleTransferRequest`, `datasetFor`, `hasSourceFor`,
`driveTransfer`) so each is testable on its own rather than protected only
transitively.

That closes the forged path's **byte exit** completely: `transfer_processes`
has exactly one writer and this check sits in front of it. A forged agreement
can only ever be recorded with `OriginAgreed` — `handleAgreement` is its sole
writer, and the attacker cannot reach `OriginImported`, which is behind the
management token on a localhost listener, or `OriginNegotiated`, which carries
an id this connector generates.

**Why the fix is at consumption rather than intake.** The forged message is not
detectably forged: it is exactly what an honest provider sends in a negotiation
the attacker legitimately owns. `assigner` matches, because the attacker named
itself as provider at initiate. "Must arrive from the participant the
negotiation is with" matches, for the same reason. §30.1's `wrongTarget` does
not help either — it compares against the negotiation's own dataset id, which
the forger also chose, and it is not an intake check at all: the row is written
before it is computed. The defect is role confusion at consumption, so
consumption is where it is caught.

Two statuses, deliberately. `handleTransferRequest` refuses with `403` — a
request to open a transfer under someone else's role is a refusal, and it
belongs in the same family as the other two refusals that endpoint now makes.
`datasetFor` returns the same not-found it returns for "nothing configured
behind this dataset", and so answers `409` at the data endpoint: reshaping its
signature to carry a reason was not worth the ripple for a path only a
pre-existing `transfer_processes` row can reach. What distinguishes the two
`409`s is a log line written for that purpose.

**Corrected by §35, in the opening sentence.** A roster participant cannot
initiate a negotiation any more: §35.1 moved both initiate hooks off the DSP
listener onto the management listener, so the first step of the path above now
needs `mgmt_token` rather than a participant credential. The path is left as
written because it is the argument for `servableAsProvider`, and the move does
not retire that argument — an operator who initiates with a real roster
participant leaves that participant able to post a `ContractAgreementMessage`
back on the negotiation it legitimately owns, which `handleAgreement`
(`internal/dsp/negotiation_consumer_handler.go`) records verbatim with
`OriginAgreed`, and to cite the resulting row in a transfer request. Refusing
that request is what this section is about, and it is unchanged.

**32.5 A third column-add does not earn a migration tool.** §23.1 left that
open — "a second schema change is what decides whether a real migration tool
earns its place". The answer is no, and the way this column actually landed is
the sharper support for that rather than an argument against it.

`counterparty_id` is one column on five tables, added by one loop in
`migrate`. On `agreements` it is in the `CREATE TABLE` literal *and* in that
loop. On the other four it is in the loop and in no `CREATE` literal at all —
so a fresh database creates those four without the column and immediately
alters them to add it, which is not the sequence `migrate`'s own comment
describes; that fresh-database case now holds only for `agreements`. Both
routes converge on the same schema, because the check-and-add is idempotent
either way, and nothing has ever failed on it.

That inconsistency is exactly the pairing §23.1's trade-off warned nothing
enforces, and it is still unenforced. What it is not is a failure a tool would
have caught: it produces no wrong schema and no wrong query, only two
different routes to one column. A versioned tool buys ordering, history, and
down-migrations, none of which this connector has needed across three
column-adds, at the price of a dependency and a version table. So: no.

The column is role-relative, matching the convention the four exchange tables
already use: for a negotiated agreement it is the consumer, for one accepted as
consumer it is the provider, and for an imported one it is whoever the operator
named. Both negotiation writers set it. `POST /agreements` gains an
**optional** `counterpartyId` — optional because required would break
`test/tck/run.sh` and every existing operator import in lockstep, and 32.2
already fixes what empty means. `GET /agreements` exposes it, because an
operator who cannot see who an agreement is with cannot audit a check that
depends on it, and it is declared **last** in that view's struct: Go emits
fields in declaration order and `demo/run.sh` extracts an agreement with a
`sed` that requires `agreementId` and `datasetId` to stay adjacent.

*Trade-offs accepted.* Four. The first two are what 32.2 and 32.5 cost, the
third is what 32.4 does not reach, and the fourth is a behaviour change rather
than a gap.

**An agreement with no recorded owner stays exactly as open as it was, and one
subset of those can never be given one.** 32.2's empty clause means an agreement
whose `counterparty_id` is empty is servable to any roster participant that
knows its id — the pre-milestone posture, unchanged. The agreement half of this
work is partial by construction, and the twelve seeds in `test/tck/run.sh` are
why.

This paragraph used to end "it closes for an operator who names the counterparty
on import, and for every negotiated agreement automatically". The second half was
false and is corrected here rather than left as the flattering reading: it holds
only for rows written after this milestone. Three subsets stay open.

*Imports that name nobody* — the ones that already exist, and any future one, since
`counterpartyId` is optional. An operator can close these, but only by concluding
the agreement again under a new id; see the next trade-off for why there is no
correction in place.

*Negotiated agreements recorded between §27 and this milestone.* The negotiation
row carries the verified counterparty and the agreement row defaulted to `''`,
because the column did not exist yet. Recoverable in principle by a backfill, and
deliberately not recovered: an `UPDATE agreements` is precisely the update path
§25.3 says this connector has nowhere, `importAgreement`'s duplicate re-query
depends on that clause, and buying back a handful of rows is not worth falsifying
an invariant the rest of the connector reasons from. Stating the gap was judged
the better trade than closing it.

*Every agreement concluded while `require_auth` was false — unrecoverable.*
`issuerFrom` returns `""` when authentication is off, so both writers record an
empty counterparty and **nothing anywhere holds the identity**; no later migration
can reconstruct what was never captured. This is not a hypothetical corner.
`config.example.yaml` documents that flag as existing for exactly this migration —
"switching a running connector from anonymous to authenticated is otherwise a flag
day for every counterparty at once" — so a connector taking the upgrade path this
repository recommends walks into it, and every agreement it concluded before
flipping the flag stays open to any roster participant that knows its id, for as
long as that agreement exists.

**Naming the wrong participant on import has no correction.** §25.3 guarantees
there is no update path and no delete path, so an operator who imports an
agreement against the wrong `counterpartyId` has locked the real counterparty
out of it, with no recourse but concluding the agreement again under a new id.
Adding a correction path means adding a write path, which §25.3 bounds by a
rule rather than by code — a later milestone that wants one argues for it on
its own merits, and this section is not that argument.

**The residuals 32.4 leaves.** The forged row survives, so four things remain
true. An id written first is permanently unimportable by its rightful owner,
because `CreateAgreement` refuses duplicates and there is no delete path. That
duplicate refusal is itself an existence oracle, and it leaves a row behind on
every miss. The forged row is indistinguishable from a real one in
`GET /agreements`, which is the operator's only audit surface. And
`handleTransferInitiate`'s own agreement gate is satisfiable by a minted id,
which makes it the sanity check it already was rather than an authorization
decision. Closing these needs the consumer-role agreement id space separated
from the provider-role one — a second table, or an `(agreement_id, origin)`
key — which reopens the "one table, one rule" argument `store.Agreement`'s
own doc comment makes, and is not attempted here.

**A connector initiating against its own public address is now refused
outright.** Recorded so it is not rediscovered as a regression. Pointed at its
own `public_url`, this connector negotiates with itself, and §23.12's
push-before-state ordering decides how that lands: the self-addressed
`ContractAgreementMessage` reaches `handleAgreement` first and writes the
agreement row with `OriginAgreed`, and `dispatch`'s own
`CreateAgreementIfNegotiationAgreed` then loses the primary key to it and logs
the collision. The single surviving row says this connector is the consumer, so
32.4 now refuses every transfer request citing it with `403`. It half-worked
before by accident, not by design, and §23.6 already rejects a loopback
callback — so this shape needs a connector's real public address and is a
self-test, not a deployment.

## 33. A data transfer is bounded by progress, not by elapsed time

**Decision.** Both ends of the data path give up on a transfer that stops
moving, and on nothing else. The provider streams through
`copyUnderRollingDeadline`, which pushes the connection's write deadline out
by `data_idle_timeout` before every chunk it writes. The consumer fetches
through `dataPullHTTPClient`, which carries no `Client.Timeout` at all: a
timer armed around `Do` bounds the dial, the handshake, and the header wait,
and `idleTimeoutReader` bounds the body by the same duration. Both bounds are
one configuration value, `data_idle_timeout`, defaulting to 60s;
`max_download_bytes` is the second bound, and the two are validated as
positive at load. The design spec is
`docs/superpowers/specs/2026-08-24-data-path-correctness-design.md`.

**33.1 A bound on total time is a file size limit written in seconds.** What
this replaces is the previous arrangement, in which a pull inherited
`callbackHTTPClient`'s ten-second timeout and a provider response inherited
the server's thirty-second `WriteTimeout`. Neither was a decision about
transfers; both were decisions about small JSON messages that a data body
happened to be routed through. A cap on elapsed time cannot distinguish a
counterparty that has stopped from a file that is simply large, so it sets a
maximum transferable size — and sets it in a unit nobody chose, because the
size it implies moves with whatever bandwidth is available on the day. Time
without progress is the property actually worth bounding: it is what a stuck
transfer has and a slow one does not.

**33.2 `io.Copy` is banned on the provider's streaming paths.** This is not a
stylistic preference and it does not read as a rule from the call site, which
is why it is recorded. `*http.response` implements `io.ReaderFrom`. On a
response that is not chunked — which is every response carrying a
`Content-Length` — that implementation hands the file to
`*net.TCPConn.ReadFrom`, a single `sendfile` that blocks until the whole
transfer finishes. A handler parked inside it cannot roll anything, so
whatever deadline was set before the call governs the entire response.
`copyUnderRollingDeadline` writes through `http.ResponseWriter` in a loop
instead, and takes an `http.ResponseWriter` rather than an `io.Writer` for
the same reason: a helper accepting `io.Writer` would find `ReadFrom` again
through the interface and silently restore the problem.

The consequence worth stating plainly is retrospective. The `206` path set
`Content-Length` before this milestone, so resumed transfers were already
non-chunked and already collapsed into one `sendfile` under the server's
`WriteTimeout`. Resumption has therefore been capped at thirty seconds since
§31 shipped, with nothing in the code able to observe the cap or report it.
The bug is older than the milestone that fixed it.

**33.3 The server-wide `WriteTimeout` stays, and the reason is narrower than
it first appears.** It remains at thirty seconds because it still bounds
every response that is not a data stream — negotiation, catalog, transfer
control — none of which roll a deadline of their own, and any of which a
client that stops reading would otherwise park indefinitely.

It does **not** stay because removing it would leak a deadline into the next
request on a keep-alive connection. That mechanism was investigated and
refuted: `net/http`'s `conn.serve` clears the write deadline unconditionally
after every request (`server.go:2080-2081`) and re-arms it from
`WriteTimeout` while reading the next one, so a deadline the data endpoint
set cannot survive the request that set it. Recorded because the refuted
explanation is the plausible one, and a future reader who reasons it out
rather than checking will arrive at it.

**33.4 A `SetWriteDeadline` error is fatal to the stream.** If the deadline
cannot be set, the loop is not rolling anything and the response has silently
reverted to whatever bound was already in force — the exact condition this
section exists to remove. Aborting is the honest answer, and it makes the
failure loud instead of invisible. The cost lands in the tests rather than in
production: `httptest.ResponseRecorder` implements neither `SetWriteDeadline`
nor `Unwrap`, so `http.ResponseController` reports `ErrNotSupported` against
it and every streaming test would abort on the first chunk. `deadlineRecorder`
in `internal/dsp/data_handler_test.go` is a two-line shim that supplies the
method. The shim moved; the production behavior did not.

**33.5 `expected_bytes` of `0` means not known, never known to be zero.** The
consumer records what the counterparty stated for the whole representation —
`Content-Length` on a fresh pull, `Content-Range`'s complete length on a
resume — in `consumer_transfers.expected_bytes`, and compares the finished
file against it. A counterparty that streams chunked states nothing, and this
connector's own provider did exactly that until this milestone, so `0` had to
mean "no claim was made" rather than "a claim of zero was made". Every read
guards on `expected > 0` before comparing. The column is also written when
the value is `0`, which is how a stale total from a discarded representation
leaves the row instead of outliving it: a fresh attempt seeds nothing from
storage, because a length recorded from a representation that has already
been thrown away has no authority over the one arriving now.

**33.6 Shutdown waits for in-flight pulls, and `Add` runs before the goroutine
does.** `NewRouter` returns a `sync.WaitGroup` alongside the handler, every
data pull it dispatches is counted in it, and `run` waits on it — with a cap —
after both listeners have shut down and before the deferred `st.Close()`
fires. The group is returned rather than kept inside the package because the
thing that must wait on it lives outside: nothing else in `internal/dsp`
outlives a request.

This is a milestone-specific need, not an oversight corrected late: a pull
touches the store at all only as of this milestone, because 33.5's
`expected_bytes` is the first thing it has ever had to record. Removing the
consumer's overall client timeout widened the window in the same milestone —
a pull used to outlive shutdown by at most the ten seconds
`callbackHTTPClient` allowed, and now by however long the counterparty keeps
it alive.

**What the wait bought at this milestone was narrower than it looks, and the
picture has since changed — read this paragraph as history.** When this
section was written, `expected_bytes` was the only thing a pull wrote, and it
was written immediately after the response headers arrived: *before*
`os.OpenFile`, before the copy. It was the pull's first store act, not its
last. The window in which shutdown could lose it was therefore the short one
between dispatch and the counterparty's first response, not the whole length
of an hours-long transfer — and against a window that shape a five-second cap
was ample rather than useless, which is the opposite of the conclusion a
reader would draw from thinking the write landed at the end. Past that write,
what the wait protected was not a store row at all but the file itself: up to
the cap for the copy to finish and `os.Rename` to place it, after which the
pull touched nothing shared.

**A pull now writes at the end as well, so that reasoning no longer stands on
its own.** §34.1 added an outcome write in a `defer`, at exactly the point an
unbounded transfer is most likely to be caught by shutdown. What keeps the
same five seconds adequate is not the paragraph above but §34.3's
cancellation, which is what turns the cap from a bound on a copy into a bound
on an unwind.

**Those columns have landed, and the re-examination this paragraph asked for
happened: its answer is §34.3.** `data_completed_at` and `data_error` were
added by the milestone recorded in §34, together with `received_bytes` and
`data_path`, and the outcome write is now the pull's last act. The
re-examination did not change the cap — it changed what the cap is racing.
§34.3 has the argument and the ordering it turns on; it is not repeated here.

The placement rule is the part a later edit will get wrong, so it is stated
rather than implied. `Add(1)` runs at the dispatch site, on the handler's own
goroutine, *before* the `go` statement; only `Done` is deferred inside the
wrapper. Two independent reasons, either sufficient. An `Add` that runs inside
the goroutine can be outrun by a `Wait` entering the window between `go` and
the goroutine's first line, which returns with that pull unregistered and
defeats the entire guarantee. And many tests in this package call
`pullTransferData` directly rather than through `go` — the count is left
unstated because a maintained count in a document rots, and this one already
had — so an `Add` outside the function paired
with a `Done` inside it would decrement a counter nothing had incremented and
panic on a negative. Keeping both halves at the dispatch site, and
`pullTransferData` free of the group entirely, makes that imbalance
structurally impossible rather than merely unreached.
`TestShutdownWaitCoversAnInFlightPull` covers both halves: it asserts the
download landed immediately after `Wait` returns, with no poll, because a
poll would pass whether or not anything waited.

**This deviated from the design spec, which asked for cancellation rather
than a wait — and §34.3 has since closed the deviation.** Spec §1.5 specified
that pulls be given a context derived from a connector-lifetime context
`run()` cancels before waiting, so shutdown would *stop* an in-flight pull and
then wait briefly for it to unwind. At this milestone the code gave each pull
`context.Background()`, which `run()` had no handle on: shutdown could not
cancel a pull, only outlast it, and a pull still streaming when the cap
expired was abandoned mid-copy rather than told to stop. The deviation was
recorded here as live because cancellation was the better design and the wait
was what that branch had; §34.3 is where it was built.

One half of the cost was never the deviation's, and outlives it. `srv.Shutdown`
carries its own five seconds, and a streaming `handleData` will exhaust them
every time — `Shutdown` waits on active handlers, and the provider side of a
large transfer is exactly that — so the two bounds run back to back and
shutdown costs up to ten seconds where it used to cost five. Cancelling pulls
does not shorten that: what is cancelled is the *consumer's* pull, and what
`srv.Shutdown` waits on is the provider's handler.

*Trade-off accepted.* Four things.

`sendfile` is given up on exactly the case it was built for. A large file now
moves through a 256 KiB userspace buffer with a syscall per chunk instead of
one kernel-side copy. That is the direct cost of 33.2 and it is worth paying:
an unbounded transfer that is somewhat slower beats a fast one that stops at
thirty seconds.

A same-length replacement between attempts is still not caught. §31.1 said
this when length was not checked at all; recording the expected total narrows
it — a replacement of a *different* size is now refused rather than appended
to — without closing it. Only a digest would close it, and there is none.

And removing the ten-second cap makes an orphaned partial download
unboundedly large. Before this milestone a transfer that was interrupted and
never restarted left behind at most what ten seconds of transfer produced;
now it leaves behind whatever arrived before the counterparty went quiet, up
to `max_download_bytes`. The leak itself is not new — §31's *Trade-off
accepted* already recorded it, and `docs/follow-ups.md` already tracks it —
but its size bound is gone, which moves the retention policy that entry asks
for from a tidiness item to an overdue one. It is still not solved here: a
retention rule is a decision about operator expectations, not a line of code
this milestone can add on its way past.

And the shutdown wait can still lose the row it exists to protect. As this
milestone shipped it, the cap in 33.6 was five seconds and a pull whose
`expected_bytes` write had not happened by then was abandoned exactly as it
would have been without any of this. That was the better of the two failures
available — the alternative to a bounded wait is a connector that will not
shut down while a counterparty keeps dribbling at it, which trades one lost
row for an operator holding down `SIGKILL`.

**§34.3 changed what happens to that pull, and the paragraph above now
overstates the loss.** A pull caught by shutdown is cancelled rather than
outlasted: it stops at once and its deferred outcome write lands, so the row
records the shutdown by name instead of saying nothing. What is *not*
recorded is `expected_bytes` specifically — a pull cancelled before its
response headers arrived never learned a length to write, and the column
keeps whatever it held, which 33.5 already defines as "not known". The
residual worth stating is no longer the copy but the unwind: §34's *Trade-off
accepted* records that a cancelled pull's own cleanup can still outrun five
seconds. This paragraph also closed by saying the bound was chosen against a
window nobody had measured and that the spec's cancellation would have
removed the guess rather than sizing it. The cancellation was built; the
window is still unmeasured.

## 34. A pull records what it did, and the provider records who collected it

**Decision.** The data path stops being unobservable at both ends. Four
columns on `consumer_transfer_processes` — `received_bytes`, `data_path`,
`data_completed_at`, `data_error` — record what a pull did, written once from
a deferred site rather than at each exit. Shutdown *cancels* in-flight pulls
before it waits for them, which is what makes that write land. One read-only
management route, `GET /transfers`, lets an operator read the result for both
roles. And `handleData` logs the identity it served, so the connector can say
who collected its data and not only who it turned away. The design spec is
`docs/superpowers/specs/2026-08-24-data-path-correctness-design.md`; its §7
splits the work into two plans, and §33 is Plan A to this section's Plan B.

**34.1 Four columns, written together from one deferred site.** `pullOutcome`
in `internal/dsp/transfer_consumer_handler.go` is a value each exit sets a
field or two on, and `pullTransferData`'s single deferred call to
`store.RecordConsumerTransferOutcome` turns it into a row. The four columns
are written in one `UPDATE`, always all four, so no combination of them can
disagree: a completed download has `data_completed_at` set and `data_error`
empty, a failed one is the reverse, and `succeed` is the only thing that can
produce the first because it sets the stamp and clears the reason in the same
statement.

The alternative was a recorder called at each exit, and the argument against
it is arithmetic. `pullTransferData` returns from more than twenty failure
paths and succeeds from exactly one, so a per-site recorder is that many
chances to miss one, with nothing to catch the miss — a forgotten exit would
leave the row describing a *previous* attempt, which is worse than leaving it
blank because it reads as current. The exact count is deliberately not
maintained in this sentence; the argument does not depend on it, and a
maintained count in a document is a thing that rots.

The miss is covered rather than merely made unlikely. The outcome value is
seeded with a failure sentence — "the pull ended without recording a reason"
— before any exit is reachable, so an exit that returns without saying why
still records that the pull did not finish. It is a stated default rather
than the struct's zero value, and the difference is the reason. The zero
value is already not a success — nothing sets the completion stamp — but it
carries no reason either, and a row saying a pull did not finish without
saying why is a worse answer to an operator than one saying the code forgot
to record it.

The one return that must *not* record anything is placed where it cannot.
`pullTransferData` drops a restart's trigger when a pull for the same
transfer is already in flight, and that return happens **above** the outcome
value and its `defer` — deliberately, because a dropped duplicate that
recorded an outcome would overwrite the row belonging to the pull actually
running. Ordering is the whole mechanism here, so it is stated rather than
left to be inferred from the line numbers.

`expected_bytes` is not among the four. It is written earlier, when the
response headers arrive, and §33.5 records why it is written even when the
value is `0`.

**34.2 `data_error` holds a sentence, not a code.** The reasons a pull can
stop are already distinct sentences at their exits, and an operator reading
one column on one row has no second place to look up what a code would have
meant. So the column carries prose: "the data endpoint refused the pull",
"the download does not match the length the provider stated", "the data pull
exceeded max_download_bytes". Nothing switches on these strings, and nothing
should — that is what a code would be for, and adding one is a decision for
whoever first needs to branch on a failure rather than read it.

The sentence is the same *reason* the log line at that exit records, and at
no exit is it the same string. That is measured, not assumed: comparing all
twenty-one recorded strings against the `slog` message at their own exit
gives zero matches. But "not the same string" is a weaker relationship than
it sounds, and the weaker truth is the one worth writing down, because this
paragraph is what stands in for a test of these sentences.

**At ten of the twenty-one the recorded string is a prefix of its own log
message** — three of them exactly, seven more once a leading "the" is
allowed, and three of those seven differ by nothing but those four
characters. The three that differ by only "the " are named so this is
checkable by reading rather than by re-measuring: "the start message carried
no data endpoint", "the data endpoint sent no response within the idle
timeout", and "the data endpoint refused the pull". Where the log message is
a full sentence, the column is usually that sentence with a trailing clause
cut off: the log says "the download does not match the length the provider
stated; leaving the partial download in place" and the column says everything
before the semicolon.

Of the eleven that are not prefixes, **nine** diverge because the log there
follows the short-verb-phrase convention with the detail in structured fields
(`slog.Error("write download", "consumer_pid", …)`), which a column with no
fields cannot use. The remaining **two** are neither: their log is a full
sentence that simply says something different from the recorded reason. They
are the shutdown caught while waiting on response headers — logged as "the
connector shut down before the data endpoint responded" — and the 206 whose
`Content-Range` did not match, logged as "206 response's Content-Range does
not start where this connector's partial download left off; …". Nine of
eleven is a majority and not a rule, which is the same shape of
over-generalisation this paragraph was rewritten to remove; it is stated as
a count for that reason.

Two consequences follow, and they are why the measurement is here rather than
a claim of unrelatedness. Adjacent exits carry near-identical prose, so a
copy-paste between them would read as correct and is the likeliest way one of
these sentences goes wrong — anyone editing them should diff neighbours
rather than read each alone. And the prefix relationship is a convention, not
a mechanism: nothing checks it, so an edit to a log message silently stops
the pair matching, which is a drift a reader should expect rather than be
surprised by.

The one place a string is genuinely shared has nothing to do with the log.
`errConnectorShuttingDown` is a single `errors.New` value used in two roles —
the cause `NewRouter` attaches to its cancellation, and the reason recorded
on the row when a pull reads that cause back — so those two cannot drift
apart. That is the sharing being claimed, and it is the only one.

Its relationship to the `slog` calls is the ordinary one, and differs between
its two exits — which is worth spelling out, because this value is the single
place a reader is most likely to assume the column and the log are the same
string. At the exit caught while waiting on response headers the log says
something else entirely: "the connector shut down before the data endpoint
responded". At the exit caught mid-copy the log is the recorded sentence plus
"; leaving the partial download in place" — one of the three exact prefixes
counted above, not an exception to them.

**34.3 Shutdown cancels in-flight pulls, and then waits for them.** §33.6
promised this re-examination and this is it. That section sized a five-second
cap against a window it correctly described *at the time*: `expected_bytes`
was the pull's only store act and it happened early, so the cap covered the
short gap between dispatch and the counterparty's first response. 34.1 moves
the record to the end of the copy, which is exactly the change §33.6 said
would make the cap worth re-examining.

The answer is not a larger cap. It is the cancellation spec §1.5 asked for
and Plan A deviated from: `NewRouter` now returns a `context.CancelFunc`
beside its `sync.WaitGroup`, every pull the router dispatches derives its
request context from that connector-lifetime context, and `drainPulls` in
`cmd/dsbox/main.go` calls the cancel **before** it waits. That order is the
decision. A cancelled pull's body read fails immediately and its deferred
write lands at once, well inside the budget; an *uncancelled* pull keeps
copying for as long as the counterparty keeps feeding it, runs the budget
out, and is abandoned mid-copy — losing precisely the row the wait exists to
protect. A wait without a cancel is a wait whose length the counterparty
chooses.

So the budget stays where §33.6 put it, at five seconds, now as a named
constant `pullDrainBudget` rather than a literal at the call site. It is no
longer a guess about how long a transfer takes, because it no longer bounds a
transfer: it bounds an unwind.

The cancel carries a cause rather than being bare, which is what lets a pull
attribute its own stop. `context.Cause` reporting `errConnectorShuttingDown`
is the difference between "the connector shut down" and "something cancelled
this", and only the first is true of every cancellation `NewRouter` issues —
the idle-timeout cancel uses the same mechanism with a different cause.
Without the cause a shutdown would be recorded as an ordinary read failure,
which is a lie an operator would act on.

What this does **not** shorten is shutdown itself. §33.6 recorded that
`srv.Shutdown`'s own five seconds and the drain budget run back to back, so
shutdown can cost ten seconds where it used to cost five. That is unchanged
and is not a thing cancellation could have changed: the pull being cancelled
is the *consumer's*, while what `srv.Shutdown` waits on is a streaming
`handleData` on the **provider** side, which nothing here cancels.

`drainPulls` is a function rather than four lines inside `run` because `run`
cannot be tested — it parses flags on the global `CommandLine`, binds two
real listeners, and blocks on `os/signal`. `TestDrainPullsCancelsWhatItWaitsFor`
and `TestDrainPullsGivesUpAtTheBudget` cover the helper. The single line
inside `run` that hands it the real cancel is covered by nothing, and that is
stated in the trade-off below rather than left to be discovered.

**34.4 `GET /transfers` is read-only, lists both roles, and sits behind the
management token.** It exists for the reason `GET /agreements` exists, stated
in that route's own doc comment: an operator otherwise has no way to see what
happened. §25.3 drew a boundary — "this is not the start of a general
management CRUD surface" — and this route is inside it, because
that boundary is about *writing*. A surface that creates, updates, and
deletes is what invites a general CRUD API; a second read route is the same
principle applied a second time, not the boundary moving.

Both roles, with a `role` field, because a route named `/transfers` that
showed half the transfers would be a trap for whoever read it next.
Provider-role rows carry no download fields — they never fetch anything —
and the `role` field says so rather than leaving a reader to infer it from
four empty values. The wire shape is a view type separate from the store
structs, for the reason `agreementView` records: the management API does not
leak whichever columns storage happens to carry. It is behind the same
`authenticated(cfg.MgmtToken, …)` middleware as both `/agreements` routes, on
the management listener, which binds to localhost by default.

**This is the second use of an argument with no stated stopping point, and
that is recorded here so the third has to answer for itself.** §25.3 narrowed
its own boundary to writes when it admitted `GET /agreements`; this route is
admitted by the same narrowing. A third and a fourth would be free rides on
reasoning that has never said where it ends. Whoever adds the third should be
made to say why the management API is still small.

**34.5 The provider logs who collected its data — a line, not a table.** §27
went to real trouble to obtain a verified identity, and `handleData` used it
only to refuse the wrong caller. A connector that can name who it turned away
and not who it served has kept the wrong half of that identity, for a
component whose product is data. So both streaming paths log `served transfer
data` with the issuer, the provider pid, the dataset id, and the bytes
actually written; the `206` path adds `range_start`, because a resumed pull
that logged the same line as a fresh one would tell an operator the whole
dataset was collected.

Both real paths, and only those. The `SimulateInterruptAfterBytes` branch
truncates deliberately and then severs the connection, so the consumer did
*not* receive the dataset; a success line there would record a delivery that
did not happen. Its absence is a decision and is commented as one at the
site, because the next reader will otherwise see two of three call sites
covered and take it for an oversight.

A log line rather than a row. The provider holds no per-download state — it
opens a file, streams it, and is done — so there is nothing for a row to be
the current version *of*. A table here would be an audit store, with
retention, growth, and a query surface of its own, which is a larger decision
than this milestone should make on its way past.

*Trade-off accepted.* Five.

**Five seconds can still lose an outcome, and cancellation narrows that
without closing it.** A cancelled pull's own unwind — `body.Stop`,
`out.Close`, the deferred `RecordConsumerTransferOutcome` against a SQLite
file that may be busy — is not instantaneous, and a machine under enough load
can outrun the budget. What changed is the shape of the risk, not its
existence: before, the cap raced a copy whose length the counterparty chose,
which is not a race that can be won; now it races a bounded cleanup, which is
one that ordinarily is. The residual is accepted for the reason §33.6 gave
for having a cap at all — the alternative is a connector that will not shut
down while a counterparty keeps dribbling at it.

**`run`'s own shutdown path has no test.** `drainPulls` is covered on both
outcomes, and every pull-side behaviour it depends on is covered, but the
line in `run` that passes the real `cancelPulls` to it is exercised by
nothing in this repository — a mutation replacing it with a no-op cancel
compiles and passes the whole suite. `run` is untestable as written, for the
reasons 34.3 lists, and extracting it far enough to test would be a larger
change than the line is worth. Recorded so the gap is known rather than
assumed absent.

**`GET /transfers` is unpaginated and returns every transfer ever recorded.**
Matching `GET /agreements`, and for the reason that route records: a list
that outgrows one response is a problem worth having first. There is no
delete path (§25.3), so this list only ever grows, and the connector that
runs long enough to find that out will find it out as a large response body
rather than as a slow query.

**A failed outcome write leaves the row describing the previous attempt —
the exact hazard 34.1 argues against, reached by a different road.** The
deferred recorder logs `record pull outcome` and returns when
`RecordConsumerTransferOutcome` errors, because there is nothing else it can
do: it is the last act of a goroutine with no caller to report to, and
retrying against a store that just failed is a loop rather than a recovery.
So a store error at that moment produces precisely the outcome 34.1's single
write site exists to prevent — a row that reads as current and describes an
attempt that is over. The difference is that this one is *logged*, where a
missed exit would be silent, and it needs the database to fail rather than a
programmer to forget. Accepted on that basis rather than solved, and stated
here because 34.1 reads as though the hazard were closed outright.

**The audit line is a log, not a queryable record.** It is subject to
whatever retention the operator's log stack has, which this connector neither
knows nor configures — `main` writes JSON to stdout and stops there. An
operator who needs to answer "who collected dataset X last quarter" needs a
log pipeline; nothing in this repository provides one, and 34.5's reasoning
for not building a table is also the reason that question has no answer here.

## 35. The initiate hooks move to the operator's listener, and a consumer-role counterparty becomes an identity

**Decision.** `POST /negotiations/initiate` and `POST /transfers/initiate` are
removed from the DSP listener and mounted on the management listener, behind
the same static token `/agreements` and `/transfers` already sit behind. Both
refuse a `providerId` the roster does not list. With a consumer-role row's
counterparty now supplied by an authenticated operator and constrained to a
name this connector can verify a message from, `refuseIfNotParty` is applied
at every resolver that reaches such a row from an inbound DSP request — which
§32.3 recorded as deliberately unguarded, and which this section makes false
on purpose. The TCK harness's own participant identity is corrected to the
name it already sends. The design spec is
`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md`.

What it closes is what `docs/follow-ups.md` called its highest-severity entry
and §32.3 called the sharpest reason these hooks deserved a milestone: a
caller-chosen `providerId` becoming the audience of a credential this
connector signs, delivered to a `connectorAddress` the same caller chose.
Severity was high rather than critical for the reason §32 gives — there is no
release, no tag, and no deployment known beyond this repository's own
harnesses.

**35.1 The hooks move to the management listener, and the prior question was
which listener rather than what a call may name.**
`docs/milestone-sequence.md` framed this milestone as *what an initiate call
may name when the roster does not list it*. `docs/goal-gap-analysis.md`'s
second ordered item disputed the framing: the prior question is who may call
them at all — that is, which listener they belong on. The gap analysis is
right about the framing and this section adopts it.

An initiate call is not a DSP message. It carries no `@context`, no `@type`,
and no `dspace:` anything; it is a plain JSON object saying "start an exchange
on my behalf". `handleTransferInitiate`'s doc comment used to call it "the
TCK-shaped hook, not a management feature", and §24.2's headline put it on the
*public* listener for the same reason. That was a true statement about what
the DSP specification standardises and a misleading one about this connector:
whatever the specification declines to standardise, an endpoint that tells
this connector to go negotiate with somebody is an operator action, and this
connector already has a listener for operator actions.

**Putting them there is not a mitigation of the impersonation primitive. It is
its removal.** The primitive requires an untrusted caller, and after the move
the only caller is an operator holding `mgmt_token`.

The final path segments are kept and no `/2025-1` prefix is added, because the
management listener carries no protocol version on any route. The verb is kept
deliberately: §25.3 bounded this API with a rule rather than with code, and
`initiate` is a trigger, which is easier to hold that line against than a
resource-creating `POST /negotiations` would be.

**The handlers stay in `internal/dsp` as code.** They are methods on
unexported types using package-private machinery — `validateOutgoingCallback`,
`writeError`, and the outbound clients that attach a minted credential — so
they cannot move to `internal/mgmt`. Nothing needs exporting either: a method
value on an unexported type is assignable to an exported `http.Handler` field.
`dsp.NewRouter` returns them in `Routers.Initiate` and `cmd/dsbox` hands them
to `mgmt.NewRouter`. Routing through `main` is a layering choice rather than
cycle avoidance — nothing imports `internal/dsp` except `cmd/dsbox`, so `mgmt`
could import it without a cycle — and the reason is that `main` already
mediates the roster and the signing key, and `mgmt` having no opinion about
the protocol package is worth keeping.

**The old paths answer 405, not 404, and that is worth knowing.** Removing the
two registrations leaves `POST /2025-1/negotiations/initiate` matching
`GET /2025-1/negotiations/{id}` with `{id}` = `initiate`, so the mux answers
405 with `Allow: GET, HEAD`; the transfer path behaves the same way. Measured
against the real router rather than reasoned about. It matters because of how
the TCK treats status codes: a 404 throws immediately, while a 4xx that is not
a 404 is retried with backoff first. A harness configuration left pointing at
an old path therefore produces a slow, confusing failure rather than a fast
one. Nothing is added to change that — it is recorded so whoever meets it
recognises it.

*Corrected by §36 (2026-08-25).* This said "any other non-2xx is retried with
backoff first", which is over-broad and was carried in from
`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md`.
`docs/superpowers/specs/2026-08-16-transfer-process-tck-wire-contract.md`
records the measured behaviour of `HttpFunctions.postJson`: retry applies to
4xx-non-404 only and only when `expectError` is false, and on the negative
paths, which pass `expectError=true`, 2xx, 3xx, 5xx, and 404 all raise while
400 and 409 pass. The 405 this paragraph is about is a 4xx, so the paragraph's
own conclusion holds; what changes is that a 5xx would raise at once rather
than being retried. §36.3 depends on that being right, and this sentence is
why an earlier draft of it got it wrong.

**A guard was lost and is replaced.** `auth_middleware_test.go` parses
`router.go` as text and asserts every DSP route is behind authentication, and
its assertions needed no editing: removing routes removes them from both sides
of its comparison, so the two simply left its reach. The equivalent assertion
on the management side is new. `internal/mgmt/route_coverage_test.go` parses
both `mux.Handle` and `mux.HandleFunc` registrations — this listener uses both
forms and the `Handle` form is precisely the one carrying the token, so a
parser that saw one form would prove nothing about the other — and asserts
that every route except `/health` refuses an unauthenticated request with 401
and a `WWW-Authenticate` challenge. Further tests cover what that one cannot:
that the new POST patterns and the existing `GET /transfers` do not shadow
each other, and that each initiate route reaches the hook it belongs to, since
`mgmt.NewRouter` takes them as positional `http.Handler` values and a swapped
pair would compile. `TestEachInitiateRouteReachesItsOwnHook` builds the router
itself, so what it pins is the wiring **inside** `mgmt.NewRouter`;
`cmd/dsbox/main.go` passes `routers.Initiate.Negotiation` and
`routers.Initiate.Transfer` into that same call positionally, and a swap
*there* is invisible to this package and to `cmd/dsbox`'s own tests —
transposed on 2026-08-24 and `go test -count=1 ./...` stayed green, so only
`make tck` and `make demo` cover it.

**35.2 `providerId` must name a roster participant, and the check reaches the
handlers as a nil-able predicate.** Both hooks refuse with 400 a `providerId`
the roster does not list. This answers the sequence document's question with
**nothing**, on this repository's own rule — "never accept a constraint that
is not enforced" — applied to identity: a name the roster does not carry is
one this connector cannot authenticate a message from and cannot meaningfully
address a credential to. After 35.1 the only caller who can be refused is the
operator, who can read the error.

**The gap analysis concluded this check would be unnecessary rather than
difficult, and this section validates anyway.** The reason is diagnostic
rather than security: after 35.1 the impersonation primitive is gone whether
or not `providerId` is checked. What the check buys is that an operator who
names a participant this connector cannot verify gets a 400 at the point of
the mistake, instead of a successful initiate followed by every inbound
message on that exchange refused by 35.3, with a log line as the only clue.

**A predicate, not a roster, and that shape is load-bearing.** Neither handler
struct carries a roster; the roster exists in `internal/dsp` only as a
`NewRouter` parameter. Both structs gain `knownParticipant func(string) bool`,
set from the roster when authentication is on and left nil when it is off,
and each handler skips the check when it is nil. A `roster auth.Roster`
field would compile, and every way it differs is a way it is worse.
`auth.Roster`'s key map is unexported and `LoadRoster` is its only
constructor, so a zero `auth.Roster` answers "not a participant" for every
id — it fails **closed** where the default has to be open. The handler-struct
literals in this package's tests leave new fields zero against a
`config.Config{}` whose `AuthRequired()` is true by default, so gating on
`AuthRequired()` would not have spared them either. A nil predicate is the
check being *absent*, which is both the correct default and the same thing
`NewRouter` already says about a disabled check: a disabled check is absent,
not silently true. And a test that wants the check would otherwise have had
to build a signed roster file on disk, where a closure is two lines. A
package-level variable armed in `NewRouter` — the shape
`mintOutboundCredential` uses — was rejected because tests in this package run
in parallel and CI runs with `-race`.

**`NewRouter` returns from two places and both populate the handlers**, which
`TestNewRouterReturnsInitiateHandlersWithAuthenticationOff` pins. Populating
only the authenticated return would ship nil handlers in development mode that
nothing catches: the management listener wraps them in `authenticated`, which
is non-nil, so registration succeeds and the panic arrives at request time,
after the token check passes, as a connection reset with no error document.
The management route-coverage test would not catch it either, because a nil
handler still answers 401 to an unauthenticated request, and neither harness
runs with authentication off.

**The check runs last**, after required fields, `validateOutgoingCallback`,
and — in the transfer hook — the agreement lookup. That is a unit-test
constraint rather than a protocol one: every branch involved answers 400, so
no ordering changes anything the TCK sees. But existing tests asserted only a
status code — one pinning that an unknown agreement is refused, another that
an unsendable address is — and placed earlier, the roster check would answer
their requests first and leave them testing nothing.
`TestHandleInitiateRefusesAnUnsendableAddressBeforeTheRosterCheck` and
`TestTransferInitiateRefusesAnUnknownAgreementBeforeTheRosterCheck` pin the
ordering, and both older tests now assert the reason they are about so the
next reordering cannot silently void them.

**The refusal names the rejected `providerId`, and the endpoint's other
refusal still does not.** `validateOutgoingCallback`'s reason is logged and
never echoed because it reports which address a hostname resolved to, which
would make the endpoint a name-resolution oracle for the network this
connector sits on — an argument about what the *connector* learned and the
caller did not. A roster refusal repeats a string the caller just sent and
tells them one bit they could learn by trying any other name. There is nothing
to disclose, and an operator debugging a typo needs to see which name was
refused. The departure is deliberate, and both hooks carry a comment saying
so, because otherwise it reads as an oversight.

**35.3 Every consumer-role resolver carries `refuseIfNotParty`, and §32.3's
account of them is short.** With 35.1 and 35.2 in place, a consumer-role row's
`counterparty_id` is supplied by an authenticated operator and constrained to
a roster participant — which is exactly what §32.3 said the comparison needed
before it could mean anything. So `refuseIfNotParty`, unchanged, is applied at
every point that resolves a consumer-role row from an inbound DSP request.
Measured against the tree on 2026-08-24, those are `handleOffers`,
`handleAgreement`, `handleConsumerFinalizedEvent`, and
`handleConsumerTermination` in `negotiation_consumer_handler.go`,
`handleGetNegotiation`'s consumer branch in `negotiation_handler.go`, and
`transferHandler.lookup`'s consumer branch in `transfer_handler.go`. The error
type is `ContractNegotiationErrorType` at the negotiation points and
`TransferErrorType` at the transfer one, matching what each function already
emits.

**§32.3 accounts for `handleOffers`, `handleAgreement`, and the transfer
lookup's consumer branch, and that is not the whole set.** The rest are
reached by role dispatch rather than through a `lookup` helper: `handleEvent`
and `handleTermination` each try the provider table, then the consumer table,
and hand off to a consumer-role branch whose provider-role sibling *does*
carry the check; `handleGetNegotiation` carries it on its provider branch and
not on its consumer branch, in the same function. Every prior reading of this
code missed them, including the first draft of this milestone's own design
spec.

**Guarding only the resolvers §32.3 names would have been worse than guarding
none.** This section rewrites `refuseIfNotParty`'s doc comment to say a
consumer-role counterparty is an authorization anchor. Had the
dispatch-reached resolvers been left out, that sentence would have stood next
to resolvers that did not compare against it, and a roster participant that is
not the counterparty could still have read a consumer-role negotiation's
state, finalized it, and terminated it, which the design spec measured on a
throwaway copy with only the named resolvers guarded. The state before this
milestone was safer than that, because there the asymmetry was documented
where a reader met it.
The rule is the one this milestone's spec states in its §7: the documentation
must not claim a property the code does not have.

**The placement in `transferHandler.lookup` is deliberate and its doc comment
still says so.** `resolvedTransfer` carries `CounterpartyID` for consumer rows
too, so a check written against the value that function returns — or hoisted
above the branch split — would apply the provider-role comparison to consumer
rows. That happens to be right after this section and would have been
catastrophically wrong before it, which is why the warning is preserved in
rewritten form rather than deleted along with the part that went obsolete.

**No empty-counterparty clause, and the consequence is stated rather than
discovered.** `counterparty_id` was added by
`ALTER TABLE ... ADD COLUMN ... DEFAULT ''`, so consumer-role rows written
before §27 carry an empty string, and `refuseIfNotParty` has no empty-stored
clause. An empty counterparty means the row predates verification, and the
safe direction for a row nobody can authorize is to refuse. So a deployment
upgraded across this change refuses inbound messages on consumer-role
exchanges that were in flight before the upgrade, and those exchanges have to
be re-initiated. Testing it needed no migration fixture: the column appears in
no `CREATE` literal for the consumer tables, so every fresh database runs that
`ALTER` and a row's counterparty is empty simply by leaving the field unset
(`TestHandleOffersRefusesARowWithNoCounterparty`).

**A fixture accident is worth naming**, because the unit suite gives this
change less coverage than a green run suggests. The consumer-role tests that
were already there survive because their fixtures leave `CounterpartyID`
empty, so the comparison is `"" == ""`. The dispatch-reached resolvers
therefore needed tests written for them; nothing that existed would have
caught their omission.

**35.4 The harness's identity is corrected rather than worked around.**
`test/tck/run.sh` minted the TCK's credential with `-iss urn:participant:tck`
and wrote that id into the roster it generates, while the TCK hardcodes
`TCK_PARTICIPANT` as the `providerId` in both initiate bodies — confirmed by
disassembling the digest-pinned image, from the `ConstantValue` attribute on
`DspConstants.TCK_PARTICIPANT_ID` and at every call site, where javac has
inlined it. It is not configurable. The two names differed for no reason:
`urn:participant:tck` was chosen by `run.sh`. Both roster heredocs and the
mint now say `TCK_PARTICIPANT`, and they have to change together or the
signature check fails and the connector refuses to start.

**This is a fixture correction, not a harness bend**, and the same disassembly
says so from several directions. The TCK verifies no inbound credential —
every class that mentions `Authorization` does so on an outbound path, and the
terminal inbound handler takes the request headers as a parameter and never
reads them. It has no property naming its own participant id. And it already
calls itself
`TCK_PARTICIPANT` in message payloads, passing that constant to
`createAgreement` as its own side of the agreement.

**This is what dissolves the cost §32.3 measured** and
`docs/milestone-sequence.md` repeated. That result count was never a property
of roster validation. It was the price of one harness having two names, and
correcting the harness's own identity is what removes it. The visible
consequence is that agreements the *provider*-role negotiation suite
concludes now carry `assignee: "TCK_PARTICIPANT"`, because the provider role
fills
`Assignee` from the verified issuer — safe on evidence already in this
repository, since the exchange-authorization design records that suite passing
with a bare UUID in that field, which is not even an IRI.

**One credential has to satisfy both listeners.** The TCK registers
`dataspacetck.dsp.connector.http.headers.authorization` as a process-wide
static interceptor, and `HttpFunctions` is the only class in the runtime that
constructs an HTTP client: both initiate clients call the four-argument
`postJson`, which falls back to that interceptor, and no call site uses the
overload that would override it. So the header does reach the moved routes,
and the same value reaches every other endpoint — the TCK cannot express two
credentials. `run.sh` therefore mints one credential before
`docker compose up`, with a long `-ttl`, and passes it both as the TCK's
authorization header and as the connector's `mgmt_token` through the existing
`DSBOX_MGMT_TOKEN` override. `mgmt_token` is removed from
`test/tck/dsbox.yaml` so a missing `compose.yaml` passthrough fails at the
first seeded agreement with an explicit message, rather than silently and much
later as a lost suite result.

**And the counterparty the connector records is now observable in a run.** The
initiate handlers log nothing on success and the container's database dies
with it, so nothing in a passing run showed that `TCK_PARTICIPANT` was what
got recorded — while a silent divergence would present as every consumer-role
result failing on what reads like a protocol bug. `run.sh` reads it back
through the management API's `GET /transfers` after the suite and fails the
run unless a **consumer-role** row names `TCK_PARTICIPANT`.

**The role anchor is what makes that check falsifiable, and the version this
one replaces had none.** `GET /transfers` returns provider-role rows alongside
consumer-role ones (`listTransfers` in `internal/mgmt/router.go`), and a
provider-role counterparty comes from the verified issuer of the request that
created the row — which in this harness is `TCK_PARTICIPANT` as well, because
the credential above is minted with `-iss TCK_PARTICIPANT`. A response
captured from a passing run on 2026-08-24 carries provider-role and
consumer-role rows alike, every one of them naming `TCK_PARTICIPANT`. So a
bare substring match on `"counterpartyId":"TCK_PARTICIPANT"` was satisfied by
rows the check is not about, and stayed green in precisely the situation it
was commissioned for: the pinned image's constant moving, §35.2 refusing every
initiate call, and no consumer-role row written at all. Replaying that
captured response with its consumer-role rows removed, the old pattern still
matched and the current one does not.

**The pattern anchors both fields inside one row.** It requires
`"role":"consumer"` and `"counterpartyId":"TCK_PARTICIPANT"` separated by
`[^}]*`. `transferView` declares `Role` ahead of `CounterpartyID` and nests no
object, so between those two fields a row carries scalar fields only, and
`[^}]*` cannot cross the brace that ends one.

**It was proved to fail, which the version it replaces never was.** Rewriting
`run.sh`'s roster heredocs to name something other than `TCK_PARTICIPANT`
while leaving the mint's `-iss` alone makes §35.2 refuse every initiate call,
so no consumer-role row is written. Run that way on 2026-08-24 the read-back
reported the divergence and `make tck` stopped before the gate; run unmodified
it printed its confirmation and the gate passed. That mutation costs the
provider-role rows too — the roster no longer authenticates the TCK's inbound
messages either — so it shows the check can fail without isolating the anchor.
The captured-response replay above is what isolates the anchor, and together
they are why this check is not another one that cannot fail.

**35.5 The management API takes on write routes, and this is the answer §34.4
asked for.** §34.4 recorded that `GET /transfers` was admitted by the same
narrowing §25.3 made when it admitted `GET /agreements` — that the boundary is
about *writing* — and said whoever added the next route should be made to say
why the management API is still small. This is that milestone.

The answer: these routes are not new capability. An operator could already
tell this connector to start a negotiation or a transfer as consumer. This
section changes where that lives and who may use it, so the management API
grows by nothing an operator could not already do — and what is new is that
nobody else can do it. Measured by what an untrusted caller can reach, the
connector's surface shrank.

§25.3's rule stands unchanged: a later milestone wanting a general CRUD
surface argues for it on its own merits. What is worth holding onto as
precedent for whoever comes next is why these were admissible. They are
triggers rather than resources — `initiate` is a verb, and the boundary is
easier to hold against a verb than against a resource-creating
`POST /negotiations`. And they arrived by *subtraction* from another listener
rather than by addition; a route that cannot say the same is asking for
something these did not ask for.

**These routes answer with JSON-LD error documents while the rest of this
listener answers plain text, and that is accepted rather than fixed.** The
handlers keep this connector's negotiation and transfer error types, which are
unprefixed for the reason `internal/dsp/transfer.go` records. Changing them
would be churn with no reader — nothing consumes those bodies, and the TCK
asserts only on the status code — so the inconsistency is recorded here as a
decision instead of being tidied.

*Trade-off accepted.* Stated rather than left to be discovered.

**A roster participant is not necessarily the participant at
`connectorAddress`.** 35.2 constrains the name an initiate call may give;
nothing binds that name to the address the same call chooses. An operator who
names a roster participant and points the address at a different connector
still gets a row whose counterparty is wrong, and after 35.3 every inbound
message on that exchange is refused with only a log line to explain it. This
narrows the hole; it does not close it. Closing it means putting an address in
the roster, which is a schema change to a signed artifact.

*Reassigned by §36 (2026-08-25).* This said that change belongs with the
roster milestone `docs/goal-gap-analysis.md` puts next. That milestone has
shipped and deliberately did not take it (§36.9). The reason is scope rather
than difficulty: §36 changes the document's lifecycle — a revision and a
lifetime, both properties of the roster as a whole — while an address changes
what an *entry* means, and would make every address change a re-signed roster
and a fleet-wide restart. It moves to the gap analysis's ordered item 4,
discovery, which is where something actually consumes an address. Until then
this gap stays open, and `SECURITY.md` names it.

**The harness stops demonstrating the five-minute credential lifetime §10
set.** Nothing in the connector changes — `credentialTTL` is untouched, and
the DSP listener still enforces expiry on every other request the suite makes.
But `run.sh` now mints before `docker compose up`, because the same string has
to be the connector's `mgmt_token` at startup, and the token has to outlive a
cold image build and the pull of a digest-pinned image. So it is minted with a
long `-ttl`, and what is lost is that the harness no longer exercises the
value §10 chose.

**The management token becomes a string the DSP listener also accepts.** In
the harness, this connector's administrative secret is exactly the credential
the TCK presents to the protocol listener. It is contained by the harness
being a closed network with one counterparty, and it is an inversion of this
section's own premise, so it is named here rather than left for someone to
notice. `internal/mgmt`'s `authenticated` carries a comment saying what it is
actually doing: comparing a shared secret with `subtle.ConstantTimeCompare`
and never parsing it, so it keeps accepting that string after the credential
inside it has expired.

**The harness's roster identity is now a function of a constant in a
digest-pinned upstream image.** If that pin moves and the constant changes,
the failure presents as every consumer-role result failing on a refusal that
reads like a protocol bug. The roster heredoc in `run.sh` carries a comment
saying so. Accepted because the alternative is what §32.3 already paid for:
one harness with two names.

**`make demo` loses the only self-issued operator credential in either
harness.** `demo/run.sh` used to mint a credential the consumer signed from
itself to itself, purely to reach these hooks on its own DSP port. Those calls
now go to the management port with the management token the script already
used for `GET /agreements`, and the minting step and its explanatory comment
block are deleted. It was the only self-issued operator credential either
harness produced — a connector signing a token from itself to itself, which
is a different shape from the credentials the demo's two connectors present
to each other — and nothing exercises that shape any more.

**An upgraded deployment's in-flight consumer-role exchanges stop working.**
35.3. Accepted because there are no deployments, and because the alternative
is a permit-on-empty clause that would outlive the reason for it.

**Neither harness can show a refusal, so the refusal side is unit tests
only.** This is the same situation §32 had, arriving again: `make tck` at 65
of 65 and `make demo` are evidence that the pass side still works — and after
35.4 the TCK result is a meaningful gate rather than a fixture accident,
because the harness now presents the identity it claims. Everything this
section refuses is covered by unit tests and by nothing else.

---

## 36. The roster carries a revision and an expiry, both signed

**Decision.** `roster.json` gains `version` and `expires_at`, both inside the
operator's signature. The expiry is enforced while the connector runs — at
load, on every inbound DSP request that requires a credential, on the
management listener's initiate hooks, and on everything this connector sends
— so a superseded roster stops being usable at a known instant even on a
connector that never restarts. It is not enforced on the version document,
which sits outside the credential check, nor on the management API's read and
import routes; §36.4 states the boundary rather than leaving "stops serving"
to be read as "answers nothing". The version is a ratchet held in this
connector's own store: a roster older than one it has already run is refused
at startup. `dsops roster sign` refuses to sign a document whose version or
expiry the connector would refuse. The design spec is
`docs/superpowers/specs/2026-08-25-roster-version-expiry-design.md`.

What it closes is what `docs/goal-gap-analysis.md` called P3's first bullet
and §9's trade-off understated. `rosterDocument` was `{participants,
signature}`, so a roster removed from circulation verified forever wherever a
copy was held, and a rollback to one that still listed a removed participant
was undetectable. §9 recorded this as "revocation is only as fast as
propagation", which is weaker than what the code did.

**36.1 The two halves buy different things, and conflating them overstates
the milestone.** The expiry is what bounds revocation: after `expires_at` a
superseded roster stops verifying anywhere, including on a connector nobody
restarted. The version is a local anti-rollback memory and nothing more — it
stops *this* connector being handed an older document than one it has already
run. No version is exchanged with any participant, so during a rollout one
connector can be ahead of another and neither can tell, and issuing a new
version does not reach a running connector. Only the expiry does.

**36.2 The signature covers the whole document, and the connector caps how
far ahead the expiry may sit.** `canonicalRosterBytes` marshals
`participants`, `version`, and `expires_at` together, so neither new field
can be edited without breaking the signature (§27.2 is amended). The cap is
`maxRosterLifetime` in `internal/auth/roster.go`: a constant, not
configuration, because a configurable maximum is a second policy the
signature does not carry — a deployment could widen its own and the widest
one would be the weak link. Without the cap the upper bound this section
claims is whatever the operator typed, which is the defect §10's five minutes
has on the token side: a lifetime the issuer chooses and the verifier does
not check.

`version` is required and must be at least the first revision. An absent
field decodes to zero, and that zero is the rejection rather than a default,
because a compatibility path would let a document without the field keep the
guarantee the field exists to provide. `expires_at` is carried as a string
and parsed only for comparison, so `canonicalRosterBytes` can go on
discarding `json.Marshal`'s error on the argument that every field it
serializes is a plain string or an int.

**36.3 An expired connector answers `409` on the DSP listener, not `401` and
not `503`.** Not `401`, because the caller's credential may be perfect and
the fault is entirely local; answering "your credential is required" sends
their operator hunting across an organizational boundary, where they cannot
read this connector's log. Not `503` either: this repository's own wire
contract records that a 5xx raises immediately on the TCK's negative paths,
exactly like the `404` §25.1 forbids, so a `503` would sit outside that rule
rather than amend it. **§25.1 is not amended.** An earlier draft proposed an
exception for a `503` and withdrew it.

**36.4 Where the refusal reaches, and what it does not.**
`requireParticipant` refuses before it reads the credential, because
verifying one against a roster this connector has declared unusable is work
that cannot mean anything. Both initiate hooks
refuse ahead of their own required-field and address checks, because that
refusal is about this connector rather than about the request and no
correction to the body would make the call succeed — `requireParticipant`
never runs on the management listener, so without this an expired connector
would refuse every counterparty while going on starting exchanges and signing
them with its real key. And `mintOutboundCredential` refuses, which stops
what the connector sends.

**What it does not reach, stated precisely, because "stops serving" is not
"answers nothing" and this repository has had to correct that sentence
before:**

- **The version document.** `mountVersionEndpoint` puts it on the outer mux
  ahead of the wrapped catch-all in `internal/dsp/router.go`, so it is not
  behind the credential check and the guard never runs for it. It goes on
  answering 200. It stays open for the reason it always was: it is how a
  counterparty learns what to speak before it has any context, and it
  discloses only a protocol version.

  Held by a test on each side, because the halves are separable and only the
  structural one was covered before this section.
  `TestVersionEndpointStaysOpen` builds a router whose roster is still good,
  so it holds the route being outside the credential check;
  `TestExpiredRosterStillServesTheVersionDocument` builds the expired router
  and holds the 200 past `expires_at`, which the DSP refusal test cannot
  because it skips `openRoutes` and this is that path. Moving the mount
  inside the wrap fails both, and it fails them differently — 401 against the
  good roster, 409 against the dead one — which is why the expired case needs
  its own assertion rather than leaning on the older test.
- **The management API's agreement and transfer routes.** Only `/health`
  consults the predicate; `/agreements` and `/transfers` sit behind
  `mgmt_token` and nothing else. They are the operator's rather than a
  counterparty's, and an operator inspecting a connector that has stopped is
  exactly who needs them.
- **A DNS lookup for a counterparty-chosen host.** On the consumer transfer
  path `validateOutgoingCallback` resolves the data endpoint before
  `mintOutboundCredential` is consulted, so a refused pull has already made a
  name resolve. No message leaves; a name is resolved.
- **A data copy already in flight.** `copyUnderRollingDeadline` bounds a
  transfer by time-without-progress rather than elapsed time, so a
  counterparty that keeps reading holds bytes flowing past `expires_at`.
  Cutting it needs the pull context cancelled on a timer, and this section
  deliberately adds no timer.
- **A state machine that has already recorded a message that will not be
  sent.**

The copy in flight and the already-recorded message are pre-existing shapes
this section declines to change. And there is a case that cuts the other way:
when the minter refuses a data pull, the consumer records that failure over
whatever the row held, so an expiry can overwrite the record of a transfer
that had already succeeded — the file on disk is untouched, the row's account
of it is not. `internal/mgmt/router.go`'s `listTransfers` already warns that a
re-pull which fails writes an empty path and a reason over what a successful
earlier attempt recorded; this is that same overwrite with a new cause.

The warning is written once for the whole connector rather than once per
refusal, held in the guard that every surface shares. A per-request warning
is the log firehose `cmd/dsbox/main.go`'s authentication-off comment exists
to prevent.

**36.5 One `expires_at` for the fleet, and only the signer can restart it.**
Every connector shares the instant, so recovery needs a signature from
`roster_signer` — a key §27.1 deliberately places outside every participant —
plus redistribution and a restart of each connector. The interval is the
operator's and this section does not pick it, but `config.example.yaml`
recommends one and says what the choice trades: the interval **is** the upper
bound §36.1 claims, so a shorter one is a stronger revocation guarantee and a
more frequent fleet-wide restart.

The recovery sequence, written down because it has to be possible: edit
`version` if a participant changed and `expires_at` always, run `dsops roster
sign`, paste the signature, distribute the file, restart each connector.
Every step exists today except the two new fields. A revocation must raise
`version`; an expiry-only re-issue need not, because equal is accepted and
the older document's earlier expiry limits it on its own.

There is no grace period. A grace period is a second expiry the signature
does not cover, so its length would vary per deployment and an attacker
holding a superseded roster would pick the most generous one. The mitigation
is a warning in the boot log before the expiry arrives, which only reaches a
connector that restarts.

**36.6 The version ratchet lives in the store, and the wiring is what needed
guarding.** `roster_version` is a single-row table written by
`Store.RecordRosterVersion`, which refuses a version below the highest it has
seen and writes nothing for an equal one — an ordinary restart presents the
roster it presented last time, and refusing that would mean a connector boots
exactly once. It says nothing about the schema's own revision, and §23.1
still declines a schema-version table.

`internal/auth` does not reach the store and does not learn to: the roster is
a parsed document and the ratchet is this connector's memory of what it has
run, which is a different concern with a different lifetime. `cmd/dsbox`
mediates them, and the call sits after the store opens and before
`dsp.NewRouter` — that call arms what this connector sends, so a rolled-back
roster has to be refused before anything can go out under it.

*Trade-off accepted.* A fresh store is fail-open on version: with nothing
recorded, any version is accepted, and the code cannot tell "first start"
from "database wiped". The expiry is what bounds that window, which is why
the two mechanisms ship together rather than separately. The mark only goes
up, so an operator who signs a number far above what they meant keeps going
from there; there is no reset, and none is added, because a reset is exactly
the capability the mark exists to deny. The load error names the version
offered and the version remembered, so the operator can see what happened.

**36.7 `/health` reports it, and that is a new unauthenticated disclosure.**
The probe answers `503` with `{"status":"roster expired"}`. A probe that
could not see this would keep a connector in rotation that can serve no
counterparty, which is not a readiness report. `503` rather than the `409`
the DSP listener answers with, because the wire contract that rules out a 5xx
governs a DSP endpoint and not this one. §25.4 is amended: `/health` no
longer carries no information, and the reason it stays open is the half that
was always load-bearing.

*Trade-off accepted.* Before this, a connector with an unusable roster
refused to start and a prober got a connection refused. Now a live connector
says the roster expired wherever it can be asked: `409` on the DSP routes
behind the credential check, `409` from the initiate hooks (behind the
management token, so to the operator only), and `503` on `/health`, which is
open to anyone who can reach that listener. The version document, which is
open to the same anonymous caller, still discloses only a protocol version.
Since the expiry is fleet-wide, that is a fact about the dataspace's
governance and not only about this connector. It is accepted because the
alternative is a refusal that misdescribes itself, and because an expired
roster is not a secret an attacker can act on: it names no participant and it
opens nothing. `SECURITY.md` carries the sentence.

**36.8 `dsops roster sign` refuses what the connector would refuse about the
document.** It applies the same required-field rules as `LoadRoster`, parses
`expires_at`, and refuses one already past or further ahead than the cap.
What it does not do is walk the participant entries: `SignRoster` calls
`checkRosterDocument` and `checkRosterExpiry` and stops there, so it prints a
signature for a roster `LoadRoster` will reject for an empty or repeated id
or a malformed `public_key`. That gap is the ordering this decision exists to
prevent, surviving in a narrower place, and it is recorded rather than
rounded off; `config.example.yaml` tells the operator to check the entries.
Printing a signature for a roster that cannot be loaded is a success the
operator acts on and a failure they meet days later, which is the worst
available ordering. It does not judge how far in the future the expiry sits
within the cap — that is the operator's policy. The CLI surface does not
change and it still writes nothing, so §27.3's print-don't-write principle is
intact: validating is not managing.

**36.9 What this section did not take.** Clock leeway on `exp` and a maximum
token lifetime are deferred to their own milestone: they share no code and no
decision with the roster, and their evidence is opposite — both harnesses
exercise the roster half simply by coming up, and neither can exercise a
clock difference, since every container shares one host clock. `nbf` is not
deferred, it is declined: without it a token from an issuer whose clock runs
ahead is accepted, and adding it would newly refuse pairs that transact fine,
so it tightens where what was wanted is a bound on `exp - iat`.
`docs/goal-gap-analysis.md`'s ordered item 3 says the same.
Binding `connectorAddress` to a roster entry is also not here; it moves to
`docs/goal-gap-analysis.md`'s ordered item 4, and the argument is in this
file at 35.5 and in that document.

*Trade-off accepted.* Deferring leeway is what makes the fleet stop across
its clock spread rather than at one instant, and a connector whose clock runs
ahead stops early. Startup now depends on the wall clock at all: a boot
before NTP converges can refuse an unexpired roster, and `run()` has no
retry. Under a restart policy that is a crash loop, which is 36.10's shape
seen from operations and the strongest argument the other way.

**36.10 An expired roster is a startup failure, and the crash loop is
accepted.** A connector that starts with a roster it cannot use can verify
nobody, and "started fine, refuses everyone" is a harder symptom to trace
than a refusal to start. Under a restart policy a connector that goes down
after `expires_at` therefore restarts forever, and stops answering `/health`
while it does.

**36.11 The adversary this closes against does not own the host.** `version`
defends against a superseded roster being placed where this connector will
read it. §27.1 puts `roster_signer`'s public half in `config.yaml` under the
same "this file's integrity is already assumed" reasoning, so an attacker who
can write that file, or delete `dsbox.db`, defeats both halves of this
section. What it closes against is an attacker who can substitute the
*roster* — over its distribution channel, or in a directory the connector
reads. That is narrower than "revocation is detectable" sounds, and it is the
honest scope.

**36.12 Every existing roster must be re-signed, and there is no
compatibility path.** A document written before this section lacks both
fields and will not load. That is deliberate: a permitted-absent version is a
document that keeps the guarantee the field exists to provide from ever
applying. The load errors name the missing field and say to re-sign, rather
than reporting a signature failure and sending the operator to look at the
wrong thing. `config.example.yaml` tells the operator the same.

**36.13 `go test` is the only gate that carries this section.** An
implementation of the design was mutation-tested and both `make tck` and
`make demo` stayed green under every mutation, including the ones aimed at
the initiate-hook and outbound checkpoints. Both harnesses write a roster
that expires a day out, so they exercise the *pass* side simply by coming up
— and permanently trip the boot log's approaching-expiry warning, which is
expected noise in `tck-connector.txt` rather than a defect.

The version-regression refusal is unreachable from either harness, for
reasons neither can fix incidentally: the TCK connector mounts no volume for
`data_dir`, so its database dies with the container, and `demo/run.sh`
removes the generated directory at the start of every run even though the
demo consumer does bind-mount `data_dir`. The demo could reach it cheaply —
a second boot with a lowered `version` would exercise the refusal end to
end — and does not,
because the demo's job is to show a transfer working and a run that
deliberately fails a boot in the middle of it is a different artifact. That
is a judgement, not an impossibility.
