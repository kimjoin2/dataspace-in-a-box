# dataspace-in-a-box

A minimum operational dataspace: clone, run, and move a file between two
connectors with no outside help. `docs/quickstart.md` is that path and CI runs
every command in it; keep it that way rather than describing the path
somewhere else. An independent DSP 2025-1 implementation — not a wrapper
around EDC.

Full design decisions and their rationale live in `DECISIONS.md`. Read it before
proposing any architectural change. If you want to do something it rules out,
ask first — do not work around it.

## Hard rules

- No plugin system, no SPI, no inheritance-based extension points. Extension is
  by fork or PR. Simplicity is the product; do not trade it for flexibility.
- **Permitted inputs are published, publicly obtainable sources only** — the
  DSP 2025-1 specification, the official TCK, published IEEE standards, and
  public European dataspace documents. Unpublished or draft standards material,
  non-public documents, and private repositories or notes must not enter this
  repository in any form, including as architecture or design ideas.
- Compliance is owed to the DSP 2025-1 spec, verified by the official TCK.
  When behavior is unclear, the spec and the TCK decide — not intuition and not
  how EDC does it.
- Ask before adding a dependency. The default answer is the standard library.
- No organizational affiliation anywhere in the repo. Copyright is b7g.

## Conventions

- **Storage**: one SQLite file under `data_dir`, WAL mode. In-memory SQLite is
  for tests only — never a runtime path.
- **TLS**: the connector speaks plain HTTP behind a reverse proxy. There is no
  TLS config. Never infer the external address from the `Host` header; use the
  required `public_url` config value. `X-Forwarded-For` is logged, never used
  for auth.
- **Ports**: DSP endpoints and the management API/UI are on separate listeners.
  The management listener binds to localhost by default.
- **Auth**: connector-to-connector is a self-signed JWT (5-minute expiry)
  verified against roster public keys. The management API takes one static
  bearer token from config. No sessions, no user accounts. DCP/VC is v2.
- **Policy**: v1 evaluates unrestricted use and a validity-period constraint
  only. Any other constraint parses, then the negotiation is rejected. Never
  accept a constraint that is not enforced.
- **Config**: one YAML file plus environment overrides. No other formats.
- **JSON-LD**: fixed compact form, processed as ordinary structured JSON. v1
  validates incoming messages by direct field checks rather than a schema
  library — see DECISIONS.md §22.5. No RDF processing.
- **Language**: English for docs, UI, and comments. This includes working
  documents under `docs/` — everything committed here is public.

## Build order

Work one protocol at a time, in TCK order. Each step should raise the TCK pass
rate before the next begins.

metadata → catalog → contract negotiation → transfer process

The CI gate holds a per-suite map of expected result counts: only the suites
whose protocol is implemented are required to pass. Adding a suite to that map
is how a protocol is declared done. (The gate is `cmd/tckgate`; see
`docs/glossary.md` for the words this repository loads.)

## Commands

- Test: `go test ./...`
- TCK: `make tck` (runs the harness, then the gate)
- Quickstart: `make quickstart` (assembles the script from `docs/quickstart.md`
  and runs it — two native connectors, one file moved)
- Build: `make build`
