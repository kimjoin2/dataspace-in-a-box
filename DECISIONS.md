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

**23.11 Negotiation endpoints stay unauthenticated in v1, and the provider
pid is accepted as the only thing protecting a negotiation.** §10's
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

**24.2 `POST /negotiations/initiate` is an unauthenticated, TCK-shaped
trigger hook on the *public* listener, and a real management-API trigger is
deliberately not built in this milestone.** `DspSystemLauncher.start()`
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

The agreement is matched by id alone. Also requiring the request's
`consumerPid` to match would reject every imported agreement, since an
imported agreement deliberately carries no consumer pid — the negotiation that
produced it did not happen here — and imported agreements are exactly the case
the import path exists to serve.

*Trade-off accepted.* The TCK sends a random UUID as `agreementId` unless a
fixture pins one, so this decision is what forces the harness to seed
agreements before the suite runs (25.8) — the validation is the reason the
harness is more complicated than it would otherwise be. A connector that
skipped the check would need no seeding at all. It would also start transfers
under contracts that do not exist.

**25.2 An agreement is a row in `agreements`, with exactly two writers, and
not a list in the config file.** The two writers are negotiation — reaching
`AGREED` writes the row, because that is the moment this connector issues the
agreement document — and import through the management API, which records an
agreement concluded elsewhere with `origin = 'imported'`. An earlier draft put
externally-concluded agreements in configuration instead, and the way that was
wrong is worth keeping: **an agreement is runtime state, not a static
declaration of what this connector advertises.** Putting it in the config file
creates a second source of truth for one concept and makes "edit a YAML file,
restart" the way contracts come into being. The tell was that the design
needed a warning attached to that list — *writing an id here creates a
contract* — and a design that needs a warning is usually the wrong design.

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

The alternative weighed was a CLI subcommand writing to the store directly,
which needs no auth, no HTTP surface, and no config field. The management API
was chosen because §11 had already committed to it and an operator importing
an agreement is a real need rather than a harness convenience.

*Trade-off accepted.* A write path into this connector that is not a DSP
message now exists, and its blast radius is bounded by a rule rather than by
the code: **this is not the start of a general management CRUD surface.** A
later milestone that wants one argues for it on its own merits. The concrete
guard is that `POST /agreements` takes two strings and writes one immutable
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
it carries no information and a readiness probe should not need a credential.

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
The bytes signed are `json.Marshal` of the parsed `[]rosterEntry` — Go's
`encoding/json` marshals struct fields in declaration order, so this is
deterministic for a fixed struct without a canonicalization spec. The
signer and `LoadRoster` both compute it from their own parsed value, so
reformatting the source file (whitespace, key order) cannot change what is
checked, and only the file's actual content — the participants — can.

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
request the TCK makes — the script's own comment already says so: "It is a
static string for the whole run: DspSystemLauncher registers it as an
interceptor once and cannot refresh it." A TCK run is dozens of authenticated
requests across 65 tests, all carrying the identical credential. That is not
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
interruption rather than mock one. It never fires on a `Range` request,
which keeps the interrupt-then-resume sequence testable — the field is what
`make demo`'s second round exercises. That round runs against a dedicated
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
