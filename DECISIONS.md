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

**Trade-off accepted.** Roster changes require redistribution, and revocation is
only as fast as propagation. Acceptable at the assumed change frequency.

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

**Decision.** Ten decisions taken while implementing the contract
negotiation protocol's provider role.

**23.1 No migration framework — a single idempotent `CREATE TABLE IF NOT
EXISTS`, run once at startup.** §8 named "migration approach for the SQLite
schema" as deferred until first needed; the `negotiations` table is that
moment. A second schema change is what decides whether a real migration tool
earns its place — one table does not.

*Trade-off accepted.* No down-migrations, no versioned schema history. Adding
a column later means writing an `ALTER TABLE ... ADD COLUMN` by hand, once —
already done once, for `rerequested` (§23.9).

**23.2 `modernc.org/sqlite` is the SQLite driver.** Pure Go, no CGO. §7
commits to a single static binary and §16 to `goreleaser` cross-compiling
linux/macOS × amd64/arm64; a CGO-based driver needs a C toolchain per target
and would break that promise the first time someone builds from source
without one.

*Trade-off accepted.* Less mature than `mattn/go-sqlite3` for exotic SQLite
features. This project only uses `CREATE TABLE`, `INSERT`, `SELECT`,
`UPDATE` — no feature it needs is exotic.

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
