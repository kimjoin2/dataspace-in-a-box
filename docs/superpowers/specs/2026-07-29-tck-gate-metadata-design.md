# Design: TCK gate and the metadata protocol

Date: 2026-07-29
Status: approved, ready for implementation planning

This is the first of several sub-projects. It establishes the compliance
harness and makes exactly one DSP protocol pass it.

## Why this first

`DECISIONS.md` §2 makes the official DSP TCK the definition of compliance and,
by extension, the roadmap. A roadmap that cannot be executed on day one is a
wish. Standing the TCK up before writing protocol handlers means every
subsequent feature is developed red-to-green against the authority that decides
correctness, and the README carries a verifiable pass rate from the first
commit rather than a promise.

The alternative — build a working two-node demo first, verify compliance later —
was rejected. It optimizes for a screenshot instead of for the claim the project
is actually making.

## Scope

Implement the DSP version metadata endpoint and the machinery that proves it
passes the TCK.

**In scope**

- Single binary `dsbox` with two listeners: DSP (public) and management
  (localhost by default), per §12
- YAML configuration with environment overrides, with `public_url` required
  per §13
- `GET {base}/.well-known/dspace-version` returning the DSP 2025-1 version
  metadata document
- A `/2025-1/` mount point on the DSP listener, empty for now
- A `/health` endpoint on the management listener, so the harness can wait for
  readiness without depending on protocol behavior
- Docker Compose harness running the connector and the TCK runtime on one
  network
- A CI gate that requires the metadata (`MET`) suite to pass and reports the
  rest without failing the build
- Repository publication material: license, README, contribution guide, CLA
  automation

**Out of scope**

Catalog, contract negotiation, and transfer process handlers. SQLite and any
persistence. `did:web`, the participant roster, JWT authentication, and the
`dsops` CLI. The management API beyond `/health`. The web UI. goreleaser and
release automation.

The metadata protocol is stateless, so no storage layer is needed. Adding one
now would be speculative structure, which §4 rules out.

## Done criteria

Each is mechanically checkable.

1. `go build ./cmd/dsbox` produces a binary; running it starts both listeners
2. `GET /.well-known/dspace-version` returns the version metadata document
3. `make tck` runs the TCK locally and every `MET` test passes
4. The same run in GitHub Actions is green under the whitelist gate
5. `README.md` states which TCK suites are gated and which protocols are not
   implemented yet, so the pass rate cannot be read as broader than it is
6. `go test ./...` passes and covers configuration parsing and validation, and
   the version endpoint response

## Repository and publication

- Local path `~/b7g/dataspace-in-a-box/`, ignored by the `b7g` operations
  repository — an independent repository, not a submodule
- GitHub `kimjoin2/dataspace-in-a-box`, public from the first commit
- Copyright b7g. No organizational affiliation anywhere, per §6 and the hard
  rules in `CLAUDE.md`
- License FSL-1.1-Apache-2.0 per §5, with CLA Assistant wired up before the
  first external contribution is possible

Publishing from the first commit means the pass rate rises from zero in public.
That is the intended effect: the project's claim is honest documentation, and a
history that starts at zero is evidence for it.

## Architecture

```
cmd/dsbox/main.go          load config, start both listeners, handle signals
internal/config/           parse YAML, apply env overrides, validate
internal/dsp/version.go    version metadata handler and response types
internal/dsp/router.go     DSP listener routes; /2025-1/ mount point
internal/mgmt/router.go    management listener routes; /health
```

Boundaries are enforced by passing values, not by sharing state.

- `internal/config` is pure: it takes bytes and an environment lookup and
  returns a `Config` or an error. It does not read files or touch globals, so
  its validation rules are testable in isolation.
- `internal/dsp` and `internal/mgmt` each take a `Config` value and return an
  `http.Handler`. No package-level variables, no `init()`.
- `cmd/dsbox` is the only place that performs I/O to start the process.

### Version metadata response

Serialized from a Go struct in the fixed compact form. No JSON-LD library, no
RDF processing, per §20.

```json
{
  "@context": ["https://w3id.org/dspace/2025/1/context.jsonld"],
  "protocolVersions": [
    { "version": "2025-1", "path": "/2025-1", "binding": "HTTPS" }
  ]
}
```

The exact `@context` URL, the `binding` value, and whether this endpoint may
require authentication are **determined by running the TCK, not by interpreting
the specification**. Where the first TCK run disagrees with the values above,
the TCK wins and this document is amended. This is the hard rule — the spec and
the TCK decide — applied to its first concrete case.

Note that `path` is relative to the base path hosting the version metadata
endpoint, so the DSP routes live under `{base}/2025-1/`.

## TCK harness

The TCK is bidirectional: it calls the connector, and the connector calls back
to the TCK's callback address. Both sides therefore need to resolve each other
by name. Running both as Compose services on one network makes local (macOS) and
CI (Linux) behave identically, which running the connector on the host would
not.

```
test/tck/compose.yaml       services: dsbox, tck
test/tck/config.properties  minimal configuration for the MET suite
Dockerfile                  connector image for the harness
Makefile                    `make tck`
```

Key TCK properties:

| Key | Value |
|---|---|
| `dataspacetck.dsp.connector.http.base.url` | `http://dsbox:8080` |
| `dataspacetck.dsp.connector.http.url` | `http://dsbox:8080/2025-1` |
| `dataspacetck.callback.address` | `http://tck:8083` |
| `dataspacetck.port` | `8083` |
| `dataspacetck.dsp.connector.agent.id` | `urn:connector:dsbox-test` |

The connector runs with `public_url: http://dsbox:8080` and `dev_mode: true`,
which is exactly the relaxation §13 carves out for running without a proxy.

The TCK image is pinned by digest rather than `latest`, so a compliance result
is reproducible and an upstream change cannot silently alter the gate.

The consumer-side suites require the connector to expose endpoints the TCK pokes
to start a negotiation or a transfer. Those are out of scope here and the
decision about where they live — a regular management API feature or something
excluded by build tag — belongs to the contract negotiation sub-project.

### Gate

The gate requires all `MET` tests to pass. Other suites run and are reported,
but do not fail the build. A protocol is declared done by adding its suite
prefix to the whitelist, which makes "done" a commit rather than an opinion.

The TCK's report format has not been verified. The implementation plan therefore
begins by running the TCK against a connector that does nothing and capturing
its output; the gate's parsing strategy is chosen from that observed output.
Writing a parser against a guessed format first would be the same mistake as
implementing a protocol against a guessed specification.

## Toolchain

Recorded in `DECISIONS.md` §21: Go 1.26, standard `net/http` with pattern
routing, `log/slog` with a JSON handler, standard `testing`.

One dependency is introduced: `gopkg.in/yaml.v3`. The standard library has no
YAML parser and §15 fixes YAML as the configuration format. This is the only
approved dependency for this milestone; anything further needs its own
discussion.

## Risks

| Risk | Mitigation |
|---|---|
| TCK report format unknown, so the gate cannot be designed up front | First implementation step captures real TCK output before any parser is written |
| The spec-derived response shape may not satisfy `MET` | The TCK run is the acceptance test; values are corrected from its output |
| Upstream TCK image changes alter results | Pin by digest |
| macOS and Linux networking differ | Both sides run as Compose services; the host network is never in the path |

## What this unlocks

The next sub-project is the catalog protocol. It inherits a working gate, so its
own done criterion is simply that `CAT` joins the whitelist. It will also be the
first to need persistence and dataset seeding, since the TCK expects specific
dataset identifiers to exist.
