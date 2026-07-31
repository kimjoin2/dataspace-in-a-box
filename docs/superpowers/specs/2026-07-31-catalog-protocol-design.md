# Design: the catalog protocol

Date: 2026-07-31
Status: approved, ready for implementation planning

The second sub-project. It adds the DSP catalog protocol and makes the `CAT`
suite join the compliance gate.

## Scope

**In scope**

- Two DSP endpoints: catalog request and dataset request
- Configuration for what this participant advertises: `participant_id` and a
  list of dataset identifiers
- DSP error responses, starting with `CatalogError`
- A gate that checks how many tests each whitelisted suite produced, not only
  that none failed
- TCK harness configuration that seeds the identifiers the `CAT` suite asks for

**Out of scope**

SQLite and any persistence. The management API beyond `/health`. Contract
negotiation, transfer process, the data plane. `did:web`, the participant
roster, JWT authentication. Catalog filtering. Policy evaluation.

## What the TCK actually requires

The first milestone's spec predicted that this sub-project "will be the first to
need persistence and dataset seeding, since the TCK expects specific dataset
identifiers to exist." Half of that is wrong, and the correction is what makes
this milestone small.

Every statement below was verified by decompiling the pinned TCK image
(`eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa4…`), not inferred from
documentation.

### The three tests

| Test | Request | Assertion |
|---|---|---|
| `CAT:01-01` | `POST {base}/2025-1/catalog/request` | the response's `dataset` array contains a node whose `@id` equals the expected identifier |
| `CAT:01-02` | `GET {base}/2025-1/catalog/datasets/{id}` | the response's `@id` equals the requested identifier |
| `CAT:01-03` | `GET {base}/2025-1/catalog/datasets/{unknown}` | the response's expanded `@type` is `https://w3id.org/dspace/2025/1/CatalogError` |

`CAT:01-03` currently fails for a reason unrelated to the catalog: the router's
default 404 body is the text `404 page not found`, and the TCK's JSON parser
reports "expected a JSON object, encountered NUMBER" on the leading `404`.

### Seeding needs no API

`Catalog01Test.datasetId` is annotated `@ConfigParam`. `InstanceInjector.getKey`
builds the lookup key as `<TEST METHOD NAME UPPERCASED>_<FIELD NAME UPPERCASED>`,
resolved from the TCK's configuration properties first and from a system
property or environment variable second. So the keys are:

```
CAT_01_01_DATASETID
CAT_01_02_DATASETID
CAT_01_03_DATASETID
```

The field's constructor default is `UUID.randomUUID()`, and JUnit's per-method
lifecycle gives each test its own instance. An unset key therefore means "a
fresh random identifier every run" — which is exactly the condition `CAT:01-03`
needs, so that key stays unset deliberately.

This removes the seeding problem entirely. The connector advertises what its
configuration says it advertises, and the harness is told to ask for those
identifiers. No seed endpoint, no fixture loader, no database.

### The response shape is pinned by bundled schemas

The TCK image carries the DSP JSON Schemas. Reproduced here as requirements
rather than as prose:

| Node | Required |
|---|---|
| Catalog (root) | `@context`, `@id`, `@type` = `Catalog`, `participantId` |
| Dataset | `@id`, `hasPolicy` (≥1 Offer), `distribution` (≥1) |
| Dataset (root, for the dataset endpoint) | the above plus `@context` |
| Distribution | `accessService`, `format` |
| DataService | `@id`, `@type` = `DataService`, `endpointURL` |
| Offer | `@id`, and `permission` or `prohibition` (≥1); `target` is forbidden |

`@context` is an **array** containing `https://w3id.org/dspace/2025/1/context.jsonld`
— the context schema is `type: array` with a `contains` constraint, and the TCK
sends it that way itself.

### `@type` is required on every node, including where the schema does not say so

The DSP context defines most terms inside **type-scoped contexts**:

- `participantId`, `dataset`, `service` exist only inside `Catalog`
- `hasPolicy`, `distribution`, and the imported ODRL vocabulary only inside `Dataset`
- `format`, `accessService` only inside `Distribution`
- `endpointURL` only inside `DataService`
- `code`, `reason` only inside `CatalogError`

A node without `@type` therefore loses those keys silently during expansion —
the document still parses, and the information is simply gone. Every node this
project emits carries `@type`.

`"action": "use"` expands to `http://www.w3.org/ns/odrl/2/use`, which is the
exact value the TCK's own reference dataset uses.

## Decisions taken here

Five decisions, to be recorded in `DECISIONS.md` as §22.

**1. Advertised datasets come from the configuration file, not from storage.**
`DECISIONS.md` §8 justifies SQLite with "connector runtime state — negotiation
and transfer state machines, agreements." A dataset list is none of those; it is
something the operator declares. Introducing storage here would drag the
still-open migration decision in with it and double the milestone. SQLite
arrives when a state machine needs it.

*Trade-off:* changing what is advertised means editing the configuration and
restarting, the same cost §11 already accepted for token rotation.

**2. The configuration carries dataset identifiers and nothing else.** The
connector synthesizes the offer, the distribution, and the data service.

Advertising a policy and enforcing one are different acts, and the code that
enforces belongs to the negotiation milestone. Exposing a policy syntax now
would ship a vocabulary nothing checks — the situation `CLAUDE.md` describes as
"never accept a constraint that is not enforced," seen from the advertising
side. When evaluation is written, the configuration grows a `policy:` key
alongside it.

**3. `participant_id` is a required configuration value.** No inference. §9
will eventually make this a `did:web` identifier; deriving one now would mint
DIDs that nothing can resolve, because the roster does not exist yet. The key
stays the same when that day comes — only its value changes.

**4. A catalog request carrying `filter` is rejected with `CatalogError`.** DSP
leaves the filter expression implementation-defined, which means a provider
cannot know what an arbitrary filter means. Returning the full catalog to a
consumer that asked for a subset lets it believe it received a filtered view.
Explicit rejection is the same stance §14 takes on policy constraints.

*Trade-off:* consumers that attach a filter unconditionally will not
interoperate until filtering is implemented.

**5. Incoming messages are validated by direct field checks, not by a JSON
Schema library.** §20 specifies JSON Schema validation; the standard library
has none, and `CLAUDE.md` requires the default answer to a dependency question
to be the standard library. Three messages with two or three required fields
each do not justify a validation engine. §20 stands as written and is revisited
when negotiation and transfer push the message count past a dozen.

*Trade-off:* validation coverage is whatever the handwritten checks cover, and
a missed field is a silent gap rather than a schema failure.

## Architecture

```
internal/config/config.go        + participant_id, + datasets
internal/dsp/catalog.go          catalog and dataset document construction
internal/dsp/catalog_handler.go  the two handlers
internal/dsp/error.go            DSP error documents
internal/dsp/router.go           mounts the catalog routes
cmd/dsbox/main.go                passes configuration into the DSP router
```

No new package. Document construction is a set of pure functions, testable
without a server from inside `internal/dsp`, so a separate package would buy
nothing today. When negotiation needs to read offers, the split can happen
then — at that point it is a rename, and the requirement will be real rather
than anticipated.

`NewRouter` gains a parameter. It currently takes none because there was
nothing to configure; the catalog changes that.

### Configuration

```yaml
public_url: https://connector.example.org
participant_id: urn:participant:example

datasets:
  - id: urn:dataset:sample
```

- `participant_id` is required. `DSBOX_PARTICIPANT_ID` overrides it. A list has
  no sensible environment representation, so `datasets` has no override.
- `datasets` may be empty or absent. The catalog then omits the `dataset` key
  entirely, because the schema allows the key to be absent but requires at least
  one entry when present.
- Each `id` must be an **absolute IRI**: `url.Parse` must succeed and the result
  must report `IsAbs()`. Two reasons: a relative identifier's fate under JSON-LD
  expansion depends on a document base the TCK never sets, and `@id` values are
  IRIs by definition. `urn:` names satisfy this and contain no reserved path
  characters.
- Each `id` must survive as a single URL path segment: no `/`, `?`, `#`, or
  whitespace. The dataset endpoint routes on `{id}` directly.
- Duplicate identifiers are a configuration error.

### Catalog document

`POST /2025-1/catalog/request` with `public_url: https://connector.example.org`:

```json
{
  "@context": ["https://w3id.org/dspace/2025/1/context.jsonld"],
  "@id": "https://connector.example.org/2025-1/catalog",
  "@type": "Catalog",
  "participantId": "urn:participant:example",
  "dataset": [
    {
      "@id": "urn:dataset:sample",
      "@type": "Dataset",
      "hasPolicy": [
        {
          "@id": "urn:dataset:sample#offer",
          "@type": "Offer",
          "permission": [ { "action": "use" } ]
        }
      ],
      "distribution": [
        {
          "@type": "Distribution",
          "format": "dsbox:unspecified",
          "accessService": {
            "@id": "https://connector.example.org/2025-1",
            "@type": "DataService",
            "endpointURL": "https://connector.example.org/2025-1"
          }
        }
      ]
    }
  ]
}
```

Derived values, all deterministic:

| Value | Rule |
|---|---|
| catalog `@id` | `{public_url}/2025-1/catalog` |
| offer `@id` | `{dataset id}#offer` |
| data service `@id` and `endpointURL` | `{public_url}/2025-1` |

Responses carry `Content-Type: application/json`, matching the version metadata
endpoint.

The offer identifier is derived rather than generated so that it is stable
across restarts without storage. When negotiation has to pin an offer to an
agreement, that is the point at which storage decides the identifier.

`accessService` holds the full `DataService` object rather than a string
reference. The schema permits either, but the context does not declare
`accessService` as `@type: @id`, so a bare string expands to a literal rather
than to a link. The inline object is unambiguous under both the schema and
JSON-LD. The catalog-level `service` array is deliberately omitted: it would be
a second representation of the same node with no consumer.

`format` is a placeholder. DSP does not define the vocabulary, and the TCK's own
reference dataset uses the literal string `format`. Advertising a real transfer
format — `HttpData-PULL`, say — would claim a transfer capability this connector
does not have, which is the failure mode decision 4 rejects for filters. The
value changes when the transfer milestone makes a real one true.

### Dataset document

`GET /2025-1/catalog/datasets/urn:dataset:sample` returns the same dataset node
with `@context` prepended. It is self-contained: the data service travels with
it.

### Error document

```json
{
  "@context": ["https://w3id.org/dspace/2025/1/context.jsonld"],
  "@type": "CatalogError",
  "code": "404",
  "reason": ["Dataset not found"]
}
```

`reason` is an array because the context declares it `@container: @set`. The
writer takes the DSP type name as a parameter, since `ContractNegotiationError`
and `TransferError` are the same document with a different `@type`.

| Condition | Status |
|---|---|
| body is not JSON, or `@type` is not `CatalogRequestMessage`, or `@context` lacks the DSP IRI | 400 |
| the request carries `filter` | 400 |
| the requested dataset does not exist | 404 |

`CAT:01-03` checks only `@type`. `code` and `reason` follow the shape the
negotiation error schema establishes, so the three protocols stay consistent.

### Request validation

```go
if !slices.Contains(msg.Context, ContextURL) { /* 400 */ }
if msg.Type != "CatalogRequestMessage"       { /* 400 */ }
if len(msg.Filter) > 0                       { /* 400 */ }
```

## Gate

The gate currently passes when at least one whitelisted test is seen and none
failed. Once `CAT` is whitelisted that is unsafe: a suite that produces two of
its three results — a connection dropped midway, an upstream change that renames
a test — reports green.

```go
// before
var whitelist = []string{"MET"}

// after
var expected = map[string]int{"MET": 1, "CAT": 3}
```

`Report.OK()` becomes: every suite in `expected` produced exactly its expected
count, and none of those results failed. A count mismatch is reported with the
suite name and both numbers, because "CAT produced 2 of 3 expected results" is
a different failure from a test failing, and the fix differs too.

`evaluate` keeps taking the expectation as a parameter, as it already takes the
prefix list. The package-level `expected` map is the production value that
`main` passes in; tests supply their own. Without that, adding `CAT` to the
package-level map would retroactively invalidate the captured output from the
first milestone, in which every `CAT` test fails — the fixtures would have to be
regenerated to keep unrelated tests compiling.

This change lands **before** `CAT` enters the map. It is a correctness fix to
the gate, and shipping it together with the change it protects against would
make the gate's own regression untestable.

## TCK harness

```properties
# test/tck/config.properties
#
# Key format: <TEST METHOD NAME UPPERCASED>_<FIELD NAME UPPERCASED>, resolved by
# the TCK's @ConfigParam injection. When a key is unset the TCK generates a fresh
# random UUID per run, which is why CAT_01_03_DATASETID is deliberately absent —
# that test requires an identifier the connector does not have.
CAT_01_01_DATASETID=urn:dataset:tck-catalog
CAT_01_02_DATASETID=urn:dataset:tck-request
```

```yaml
# test/tck/dsbox.yaml
participant_id: urn:participant:dsbox-test
datasets:
  - id: urn:dataset:tck-catalog
  - id: urn:dataset:tck-request
```

Two identifiers rather than one shared identifier, so that `CAT:01-01` proves
the catalog lists datasets and `CAT:01-02` proves lookup selects among them.

The comment explaining the key format is load-bearing. Without it the next
reader has no way to derive these names, and the values look arbitrary.

## Testing

| Layer | Cases |
|---|---|
| Configuration | missing `participant_id`; duplicate dataset id; id without a scheme; id containing `/`; absent `datasets` |
| Documents | catalog with zero, one, and two datasets; dataset lookup hit and miss; every node carries `@type`; derived identifiers match the rules above |
| Error documents | shape, `reason` is an array, type name is parameterized |
| Handlers (`httptest`) | catalog success; dataset success; unknown dataset → 404 `CatalogError`; `filter` present → 400; malformed body → 400 |
| Gate | a suite short of its expected count fails; a suite over its expected count fails; expected counts met passes; the first milestone's captured output still evaluates correctly under `{"MET": 1}` |
| TCK | `make tck` green with `CAT` in the gate |

## Documentation

- `README.md`: the per-suite status table gains catalog, and the honest total
  becomes 4 of 59
- `config.example.yaml`: `participant_id` and a sample dataset, so a fresh clone
  serves a non-empty catalog
- `DECISIONS.md`: §22 with the five decisions above; §8's scope clarified to say
  that advertised datasets are configuration, not runtime state

## Done criteria

1. `make tck` passes with `CAT` in the gate's expected map, and the same run is
   green in GitHub Actions
2. `go test ./...` passes and covers every case in the testing table
3. A short TCK run — fewer results than expected for a whitelisted suite —
   fails the gate, proven by a test
4. `README.md` states 4 of 59 and marks contract negotiation and transfer as not
   implemented
5. A fresh clone with `config.example.yaml` serves a catalog containing the
   sample dataset
6. `DECISIONS.md` §22 records the five decisions with their trade-offs

## Risks

| Risk | Mitigation |
|---|---|
| Relative dataset identifiers may not survive JSON-LD expansion in the TCK | Identifiers are required to be absolute IRIs, which removes the dependency on a document base |
| The `@ConfigParam` key format was derived from bytecode, not documentation | The first TCK run confirms it: if the requested path does not carry the configured identifier, the format is wrong and visible immediately |
| Type-scoped contexts drop keys silently, so a malformed document still parses | Tests assert `@type` on every emitted node |
| `format` is a placeholder that could be mistaken for a capability | The value is namespaced and obviously not a transfer format; the README marks transfer as not implemented |

## What this unlocks

Contract negotiation. It inherits the advertised offers, and it is the first
protocol with a state machine — which is where `DECISIONS.md` §8's SQLite
decision finally has something to store, and where the open migration question
must be answered.
