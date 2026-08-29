# Did decoding `distribution` regress the catalog client? A measurement

**REGRESSION FOUND.** Four of twenty-five realistic catalog documents decoded
before commit `d81281c` and do not decode after it. All four fail for the same
reason and it is not the entries: `remoteDataset.Distribution []json.RawMessage`
makes the *array* strict, and `distribution` is the one field a JSON-LD producer
most often emits as something other than an array.

This report measures rather than argues. Every row below is the output of
running the document through the connector's own decode path.

---

## 1. Method

**Trees compared.** Two clean checkouts, taken with `git archive` from the repo
itself:

| tree | commit |
|---|---|
| before | `d81281c^` (`830b438`, "docs: replace the ten-minute claim with what CI runs") |
| after | `d81281c` ("feat: the catalog advertises the format it serves") |

Nothing was hand-reverted. The "before" column is the parent commit's actual
source.

**Decode path.** A throwaway `zz_probe_test.go` was dropped into
`internal/dsp/` of each tree. It replays each document through exactly what
`fetchCatalog` does with a response body, in the same order:

```go
var c remoteCatalog
decErr := json.NewDecoder(io.LimitReader(bytes.NewReader(data), maxCatalogResponseBytes)).Decode(&c)
// then fetchCatalog's own refusal:
if c.ParticipantID == "" { /* not a catalog */ }
pairs, skipped := c.pairs()
```

The pairs are read back through `json.Marshal`, so the probe file compiles
unchanged against both trees — it never names `Distribution`, `format()` or
`datasetOffer.Format`, and the `"format"` key simply appears in the "after"
output on its own.

Note for anyone reading the word "strict": the decoder does **not** call
`DisallowUnknownFields`. Strictness here is type strictness only. Unknown keys
are free, which is why the "extra unknown keys" document below is a non-event
and why the failures are all shape failures.

**Grounding.** Twelve of the twenty-five documents are grounded in an artefact,
thirteen are invented. The grounding artefacts are:

- `catalog/dataset-schema.json` and `catalog/catalog-schema.json`, extracted
  from the pinned TCK image
  `eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa40486714d441057bc6063653a1feebb95444f721974147cb0dd7416ad`
  (`docker create` + `docker cp /app/tck-runtime.jar`, then `unzip`).
- The DSP 2025-1 JSON-LD context, fetched live from
  `https://w3id.org/dspace/2025/1/context.jsonld` (which redirects to
  `eclipse-dataspace-protocol-base.github.io/.../dspace.jsonld`).
- Eclipse EDC's emitted catalog shape, from the project's own discussions
  (`eclipse-edc/Connector` discussions 4147, 5583, 3697): EDC writes
  `dcat:distribution` entries whose `dct:format` is an `{"@id": ...}` object,
  not a string.
- This repository's own encoder structs, for the document `dsbox` itself emits.

**What the schema actually says.** From the pinned image, `dataset-schema.json`:

```json
"distribution": { "type": "array", "items": {"$ref": "#/definitions/Distribution"}, "minItems": 1 },
"required": [ "hasPolicy", "distribution" ]
```

and `Distribution` requires `accessService` and `format`, with
`"format": {"type": "string"}` and `accessService` a `oneOf` of a bare string or
a `DataService` object. So the commit message's premise is accurate: the schema
does declare the array and does require it.

**What the context actually says.** In the DSP context, `distribution` carries
`"@container": "@set"` — but only inside the *type-scoped* contexts of
`Catalog` and `Dataset`:

```json
"Dataset": { "@id": "dcat:Dataset", "@context": {
    "distribution": { "@id": "dcat:distribution", "@container": "@set" },
    "hasPolicy":    { "@id": "odrl:hasPolicy",    "@container": "@set" } } }
```

This is load-bearing for the finding. A JSON-LD compactor emits an array for a
single value only where `@container: @set` is in scope, and that scope is
entered by the node carrying `@type: "Dataset"`. A dataset node that omits
`@type` — or one compacted against a context that is not this one — collapses a
lone distribution to a bare object. `catalog.go`'s own header comment already
states this mechanism ("A node without `@type` therefore loses those keys
silently during expansion"), and `remoteCatalog` deliberately does not decode
`@type`, so it cannot tell the two cases apart.

---

## 2. The table

`pairs` is `pairs()`'s output marshalled to JSON. `-` means the document was
refused before `pairs()` ran.

| # | document | grounded? | decodes before | decodes after | pairs before | pairs after |
|---|---|---|---|---|---|---|
| D01 | dsbox's own emitted catalog | grounded | yes | yes | 1 pair, no format | 1 pair, `HTTP-PULL` |
| D02 | TCK dataset-schema minimum | grounded | yes | yes | 1 pair, no format | 1 pair, `HttpData-PULL` |
| D03 | EDC prefixed keys (`dcat:`/`dct:`) | grounded | yes | yes | `[]` | `[]` |
| **D04** | **single-object `distribution`, two offers** | **grounded** | **yes** | **NO** | **2 pairs** | **—** |
| **D05** | **dataset without `@type`, lone distribution** | **grounded** | **yes** | **NO** | **1 pair** | **—** |
| D06 | distribution entries are bare IRIs | invented | yes | yes | 1 pair | 1 pair, no format |
| **D07** | **`distribution` is a bare IRI string** | **invented** | **yes** | **NO** | **1 pair** | **—** |
| D08 | `format` as an `{"@id": ...}` object | grounded | yes | yes | 1 pair | 1 pair, no format |
| D09 | `format` absent | invented | yes | yes | 1 pair | 1 pair, no format |
| D10 | `dct:format` key instead of `format` | grounded | yes | yes | 1 pair | 1 pair, no format |
| D11 | DCAT metadata, many unknown keys | grounded | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D12 | `null` hole in the distribution array | invented | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D13 | `"distribution": null` | invented | yes | yes | 1 pair | 1 pair, no format |
| D14 | federated broker, nested `catalog[]` | grounded | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D15 | format only on the third distribution | invented | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D16 | expanded-ish `[{"@value": ...}]` values | grounded | yes | yes | 1 pair | 1 pair, no format |
| D17 | single-object `hasPolicy` (control) | grounded | NO | NO | — | — |
| D18 | single-object `dataset` (control) | grounded | NO | NO | — | — |
| D19 | `format` as a number | invented | yes | yes | 1 pair | 1 pair, no format |
| D20 | `format` as `null` | invented | yes | yes | 1 pair | 1 pair, no format |
| **D21** | **two datasets, first has single-object `distribution`** | **invented** | **yes** | **NO** | **2 pairs** | **—** |
| D22 | unreadable entry then a readable one | invented | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D23 | object format then a string format | invented | yes | yes | 1 pair | 1 pair, `HTTP-PULL` |
| D24 | empty distribution array | invented | yes | yes | 1 pair | 1 pair, no format |
| D25 | two datasets, different formats | invented | yes | yes | 2 pairs | 2 pairs, differing |

Totals: 23 of 25 decoded before, 19 of 25 decode after. Four regressions, two
controls that fail identically in both trees, zero documents that newly decode.

The error message is the same for all four regressions, modulo the JSON type:

```
json: cannot unmarshal object into Go struct field remoteDataset.dataset.distribution of type []json.RawMessage
json: cannot unmarshal string into Go struct field remoteDataset.dataset.distribution of type []json.RawMessage
```

### The new failure surface, enumerated

A separate probe fed every JSON shape `distribution` can take through both
trees. This is the complete boundary, not a sample:

| `distribution` is | before | after |
|---|---|---|
| absent | decodes | decodes |
| `null` | decodes | decodes |
| `[]` | decodes | decodes |
| `[{...}]` | decodes | decodes |
| `["urn:d"]` | decodes | decodes |
| `[null]` | decodes | decodes |
| `[1]` | decodes | decodes |
| `[[]]` | decodes | decodes |
| `{...}` | decodes | **REFUSED** |
| `{}` | decodes | **REFUSED** |
| `"urn:d"` | decodes | **REFUSED** |
| `""` | decodes | **REFUSED** |
| `1` | decodes | **REFUSED** |
| `true` | decodes | **REFUSED** |
| `false` | decodes | **REFUSED** |

Seven of fifteen shapes are newly refused. The rule is exact: **`distribution`
present, and neither an array nor `null`, now costs the entire catalog.** Before
the change, `distribution` was not decoded at all, so every shape was tolerated.

---

## 3. Judging the four failures

**Is failing correct or harmful?** The TCK's schema forbids all four shapes, so
"the schema forbids that shape" is true of every one of them. That is not the
end of the question, because the same is true of a `null` `dataset` list and
section 38.6 still decided to admit it. What decides is the cost, and the cost
here is asymmetric in a way the other strict fields' is not.

**D04 and D05 are the harmful pair, and they are the grounded ones.**

- D05 is produced by a mechanism this repository has already written down. The
  DSP context scopes `@container: @set` for `distribution` under
  `@type: "Dataset"`. Omit the `@type` and a lone distribution compacts to a
  bare object. `catalog.go`'s own comment describes exactly this loss. The
  connector does not decode `@type`, so the field whose absence causes the
  shape is the field it declines to look at.
- D04 is the same shape reached from the commonest arithmetic in a catalog: a
  dataset usually has exactly one distribution and may have more than one
  offer. One item compacts to a bare object; two stay an array. So `hasPolicy`
  survives as an array while `distribution` collapses beside it — the document
  is not uniformly "single-object style", and a reader who assumes the two
  fields fail together will not predict this.
- D07 (a bare IRI reference) is invented and the weaker case, but note the
  asymmetry it exposes: `["urn:d"]` is admitted and costs one entry's format
  (D06), while `"urn:d"` is refused and costs the whole catalog. The connector
  tolerates the reference-by-IRI idiom only when it happens to be plural.

**D21 is the one that should decide the design question.** Two datasets: the
first has a single-object `distribution`, the second is impeccable. Before, both
datasets yielded pairs. After, neither does — the document is refused whole. So
the failure is not scoped to the dataset that caused it, let alone to that
dataset's format. A counterparty advertising ten datasets of which one has a
collapsed distribution now serves an operator nothing at all.

**D17 and D18 are not regressions and are listed only as controls.** They fail
identically before and after with the identical error. Section 38.5's decision
about single-object `dataset` and `hasPolicy` is untouched by this commit.

**The argument the commit makes does not reach the array.** The commit message
says the entries are read at arm's length "so that an entry this connector
cannot read costs that entry's format and nothing else". That is measured true
(section 4). But the same sentence pairs it with strictness on the array, and
those two halves rest on incompatible views of what `format` is worth:

- At the entry level the commit rules that an unreadable format is *not* an
  error. `format()` returns `""`, `pairs()` still emits the pair, and
  `datasetOffer.Format` is `omitempty` precisely so absence reads as absence.
  `TestADatasetWithNoReadableFormatIsStillNegotiable` pins this.
- At the array level an unreadable `distribution` is fatal to the whole
  document, including the `datasetId`/`offerId` pairs, which do not come from
  `distribution` at all and were served fine one commit ago.

Section 38.5's own justification for strictness was that tolerance produces a
value an operator would act on — "a `null` policy list [decodes] to a phantom
offer with an empty `@id`, which is a value the operator would paste into an
initiate call". That argument does not transfer. Tolerating a single-object
`distribution` cannot fabricate a phantom identifier; the worst it can produce
is a format, and the commit has already decided that a missing format is
survivable and visibly absent. The severity that justified strictness on
`dataset` and `hasPolicy` is absent here, while the blast radius is larger —
one field's shape now voids fields it has nothing to do with.

**Nothing in the suite catches this.** `go test ./...` on the after tree is
green, all nine packages. `TestRemoteCatalogRefusesShapesTheSchemaForbids`
enumerates a single-object `dataset`, a single-object `hasPolicy` and a scalar
`dataset` — it was not extended with a single-object `distribution` case. The
three tests the commit adds all exercise entry tolerance. The array strictness
that the commit message argues for is the one property it asserts nowhere, so
the cost measured here was never in front of a test.

**A cheap fix exists that keeps the commit's stated goal.** Typing the field
`Distribution json.RawMessage` (one value, not a slice) and having `format()`
try `[]json.RawMessage` first and a single `json.RawMessage` second would read
the format from both shapes, keep every entry at arm's length, and restore the
four documents — without tolerating anything on `dataset` or `hasPolicy`, where
section 38.5's phantom-identifier argument does apply. That is a suggestion, not
a measurement; the measurements are above.

---

## 4. Does the arm's-length claim hold?

**Yes for entries, with two caveats.** A probe fed `remoteDataset.format()`
entry configurations directly. Results:

| entries | `format()` |
|---|---|
| `[{"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `["urn:distribution:0", {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"format":{"@id":"S3-PUSH"}}, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"format":{"@id":"S3-PUSH"}}]` | `""` |
| `[{"format":42}, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"format":42}]` | `""` |
| `[{"format":null}]` | `""` |
| `[{"format":["HTTP-PULL"]}, {"format":"S3-PUSH"}]` | `S3-PUSH` |
| `[null, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[[{"format":"X"}], {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[7, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[true, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"format":""}, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"accessService":"urn:s"}, {"format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"dct:format":"HttpData-PULL"}]` | `""` |
| `[{"format":"HTTP-PULL","accessService":99}]` | `HTTP-PULL` |
| `[{"format":"HTTP-PULL","format":42}, {"format":"S3-PUSH"}]` | `S3-PUSH` |
| `[{"format":"HTTP-PULL","format":"S3-PUSH"}]` | `S3-PUSH` |
| `[{"Format":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"FORMAT":"HTTP-PULL"}]` | `HTTP-PULL` |
| `[{"format":"AmazonS3-PUSH"}, {"format":"HTTP-PULL"}]` | `AmazonS3-PUSH` |
| `["urn:a", 42, {"format":{"@id":"X"}}]` | `""` |
| `[]` | `""` |

Answering the three shapes asked about directly: `format` as an object yields
no format, `format` as a number yields no format, `format` as `null` yields no
format. In every mixed case the readable entry's format is reported and the
unreadable entry costs exactly itself. **An unreadable entry never produced a
wrong format, only none.** The structural reason is that `var dist
remoteDistribution` is declared *inside* the loop, so a partially populated
value from a failed `json.Unmarshal` — `encoding/json` populates what it can
before returning an error, the property section 38.6 is built on — is discarded
with the iteration rather than leaking into the next one. Had that declaration
been hoisted out of the loop, a failed entry could have supplied the next
entry's answer. It is not hoisted.

**Caveat one: `format` is matched case-insensitively.** `{"Format":"HTTP-PULL"}`
and `{"FORMAT":"HTTP-PULL"}` both yield `HTTP-PULL`. That is `encoding/json`'s
default field matching, and it means a key that is not the DSP term `format` —
JSON-LD keys are case-sensitive, so `Format` is a different term or none — is
read as though it were. This is a wrong value rather than none, though it needs
a counterparty emitting a mis-cased key to reach.

**Caveat two, and the larger one: `format()` returns the first readable format,
with no filter on whether this connector can use it.** Given
`[{"format":"AmazonS3-PUSH"}, {"format":"HTTP-PULL"}]` it returns
`AmazonS3-PUSH`. The entry is perfectly readable, so nothing here is a decode
failure — but the lookup then advertises that to the operator, and
`demo/run.sh` feeds it straight into `POST /transfers/initiate`, a transport
this connector does not implement, while an `HTTP-PULL` distribution sat in the
same array. A dataset with several distributions is the normal case for a
provider offering more than one transport, and array order is not a preference
ranking. The claim under test is about unreadable entries and it holds; this is
the adjacent risk the same walk creates.

---

## 5. The `sed` extraction in `demo/run.sh` and `docs/quickstart.md`

**It picks the wrong dataset's format, and it does so by construction.**

Both files extract with:

```sh
format=$(printf '%s' "$catalog" | sed -n 's/.*"format":"\([^"]*\)".*/\1/p' | head -1)
```

The real handler was driven end-to-end — `catalogLookupHandler.handleCatalogLookup`
against an `httptest` provider — to get the exact bytes rather than a
reconstruction. With `urn:dataset:sample` advertising `HTTP-PULL` and
`urn:dataset:sample-resume` advertising `S3-PUSH`, the lookup emits:

```json
{"participantId":"urn:participant:provider","connectorAddress":"http://127.0.0.1:60630","datasets":[{"id":"urn:dataset:sample","offerId":"urn:dataset:sample#offer","format":"HTTP-PULL"},{"id":"urn:dataset:sample-resume","offerId":"urn:dataset:sample-resume#offer","format":"S3-PUSH"}]}
```

Running the extraction against those exact bytes yields:

```
S3-PUSH
```

Not `HTTP-PULL`. Three things are going on and each is worth naming separately:

1. **The pattern is not anchored to a dataset.** It matches `"format":"..."`
   anywhere in the document. The offer extraction two lines above it *is*
   anchored — `s/.*"id":"urn:dataset:sample","offerId":"\([^"]*\)".*/\1/p`
   correctly returns `urn:dataset:sample#offer` — so the script demonstrates
   the anchoring technique it did not apply to the format.
2. **`.*` is greedy, so it takes the LAST format, not the first.** The intuition
   that an unanchored pattern at least yields the first match is wrong here.
   The leading `.*` consumes as much as it can, so the captured group is the
   final `"format":"..."` on the line.
3. **`head -1` does nothing.** `writeJSON` emits compact single-line JSON, so
   `sed` produces exactly one output line whatever the document contains. The
   de-duplication the author reached for is not what selects the value; greed
   is.

`demo/run.sh` then uses that one `$format` for **both** transfers — the
`urn:dataset:sample` transfer and the `urn:dataset:sample-resume` transfer both
interpolate the same variable. So under differing formats the sample transfer
would be initiated with the resume dataset's format.

**The second scenario is worse, because the guard does not fire.** When the
dataset actually being transferred advertises a format this connector cannot
read — say EDC's `{"@id": "HttpData-PULL"}` object — the field is omitted from
its row, exactly as designed:

```json
{"participantId":"urn:participant:provider","connectorAddress":"http://127.0.0.1:60632","datasets":[{"id":"urn:dataset:sample","offerId":"urn:dataset:sample#offer"},{"id":"urn:dataset:sample-resume","offerId":"urn:dataset:sample-resume#offer","format":"S3-PUSH"}]}
```

The extraction still yields `S3-PUSH`, harvested from the other dataset. So
`[ -z "$format" ]` does not fire, the script does not stop, and the operator is
never sent down the by-hand path. `docs/quickstart.md` states in prose that
"when it does not, the lookup reports the pair without a format and the value
has to be supplied by hand" — its own snippet falsifies that sentence, because
any other dataset in the response supplies a value first.

**Why the demo is green today.** Both demo datasets are served by the same
`dsbox` provider, so both advertise `HTTP-PULL` and the last match equals the
first. The defect is latent, and it becomes live the moment a provider
advertises two transports or a counterparty other than `dsbox` is on the other
end. Anchoring the pattern to the dataset, the way the offer line already is,
is the whole fix.

---

## 6. Reproduction

```sh
mkdir -p /tmp/probe && cd /tmp/probe
git -C <repo> archive 'd81281c^' | tar -x -C before/
git -C <repo> archive  d81281c   | tar -x -C after/
cp zz_probe_test.go before/internal/dsp/ ; cp zz_probe_test.go after/internal/dsp/
# in before/ and then in after/:
CORPUS_DIR=./corpus PROBE_OUT=./before.json go test ./internal/dsp -run TestProbeCorpus -count=1
```

The probe files are throwaway and are not proposed for the repository. What is
worth keeping from them is one test case: a single-object `distribution` added
to `TestRemoteCatalogRefusesShapesTheSchemaForbids`, which would at least make
the new refusal a decision rather than a side effect.

---

## 7. The corpus

Each document is one line, which is how the connector sees it on the wire and
what section 5's `sed` depends on. `participantId` is `urn:participant:provider`
throughout so that `fetchCatalog`'s non-catalog refusal never fires and the
decode is the only thing under test.

### D01 — grounded

This repo's own emitted catalog, from the encoder structs.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"http://provider:8080/2025-1/catalog","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:sample","@type":"Dataset","hasPolicy":[{"@id":"urn:dataset:sample#offer","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":{"@id":"http://provider:8080/2025-1","@type":"DataService","endpointURL":"http://provider:8080/2025-1"}}]}]}
```

### D02 — grounded

The TCK `dataset-schema.json` minimum: `accessService` as a bare string, which
its own `oneOf` allows; `format` a plain string.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:tck","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:tck","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:tck","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"accessService":"urn:service:tck","format":"HttpData-PULL"}]}]}
```

### D03 — grounded

Eclipse EDC shape: `dcat:`/`dct:`/`odrl:` prefixed keys and `dct:format` as an
`@id` object rather than a string. Nothing decodes because no key matches;
the lookup reports an empty dataset list rather than failing.

```json
{"@context":{"dcat":"http://www.w3.org/ns/dcat#","dct":"http://purl.org/dc/terms/","odrl":"http://www.w3.org/ns/odrl/2/","dspace":"https://w3id.org/dspace/2025/1/"},"@id":"urn:catalog:edc","@type":"dcat:Catalog","participantId":"urn:participant:provider","dcat:dataset":[{"@id":"urn:dataset:edc","@type":"dcat:Dataset","odrl:hasPolicy":[{"@id":"urn:offer:edc","@type":"odrl:Offer"}],"dcat:distribution":[{"@type":"dcat:Distribution","dct:format":{"@id":"HttpData-PULL"},"dcat:accessService":{"@id":"urn:service:edc","@type":"dcat:DataService","dcat:endpointURL":"http://edc:8080/api/dsp"}}]}]}
```

### D04 — grounded — REGRESSION

One distribution, several offers: a compactor that does not apply the DSP
type-scoped `@container: @set` collapses the single distribution to a bare
object while `hasPolicy`, having two entries, stays an array.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:x","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:a","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:a1","@type":"Offer","permission":[{"action":"use"}]},{"@id":"urn:offer:a2","@type":"Offer","permission":[{"action":"use"}]}],"distribution":{"@type":"Distribution","format":"HTTP-PULL","accessService":{"@id":"urn:service:a","@type":"DataService","endpointURL":"http://a:8080/2025-1"}}}]}
```

### D05 — grounded — REGRESSION

The dataset node carries no `@type`, so the DSP context's type-scoped
`@container: @set` for `distribution` never applies and a single distribution
compacts to a bare object.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:y","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:b","hasPolicy":[{"@id":"urn:offer:b"}],"distribution":{"format":"HTTP-PULL","accessService":"urn:service:b"}}]}
```

### D06 — invented

Distributions referenced by IRI rather than embedded; the array is an array,
the entries are strings.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:z","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:c","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:c","@type":"Offer","permission":[{"action":"use"}]}],"distribution":["urn:distribution:c1","urn:distribution:c2"]}]}
```

### D07 — invented — REGRESSION

A single distribution referenced by IRI, collapsed to a bare string.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:w","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:d","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:d","@type":"Offer","permission":[{"action":"use"}]}],"distribution":"urn:distribution:d"}]}
```

### D08 — grounded

Compacted keys but EDC's format value: an `@id` object where the TCK schema
says string.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:fo","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:e","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:e","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":{"@id":"HttpData-PULL"},"accessService":{"@id":"urn:service:e","@type":"DataService","endpointURL":"http://e:8080/2025-1"}}]}]}
```

### D09 — invented

A distribution with an `accessService` and no `format` at all; the schema
requires one, real documents omit it.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:fa","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:f","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:f","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","accessService":{"@id":"urn:service:f","@type":"DataService","endpointURL":"http://f:8080/2025-1"}}]}]}
```

### D10 — grounded

Otherwise compacted, but the format key stays prefixed as `dct:format`, which
is what a partially compacted EDC document looks like.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:dct","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:g","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:g","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","dct:format":"HttpData-PULL","accessService":{"@id":"urn:service:g","@type":"DataService","endpointURL":"http://g:8080/2025-1"}}]}]}
```

### D11 — grounded

DCAT metadata a real publisher carries: title, description, issued, keyword,
byteSize, mediaType, plus a distribution-level `hasPolicy`. Demonstrates that
unknown keys cost nothing, since the decoder does not disallow them.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:meta","@type":"Catalog","participantId":"urn:participant:provider","title":"Provider catalogue","homepage":"https://provider.example/","service":[{"@id":"urn:service:h","@type":"DataService","endpointURL":"http://h:8080/2025-1"}],"dataset":[{"@id":"urn:dataset:h","@type":"Dataset","title":"Sample readings","description":"Half-hourly readings.","issued":"2026-01-04T00:00:00Z","keyword":["energy","readings"],"hasPolicy":[{"@id":"urn:offer:h","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","mediaType":"text/csv","byteSize":20481,"hasPolicy":[{"@id":"urn:offer:h-dist","@type":"Offer","permission":[{"action":"use"}]}],"accessService":{"@id":"urn:service:h","@type":"DataService","endpointURL":"http://h:8080/2025-1"}}]}]}
```

### D12 — invented

A `null` hole in the distribution array ahead of a readable entry.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:nul","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:i","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:i","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[null,{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:i"}]}]}
```

### D13 — invented

`distribution` present but `null`, which a serializer that keeps empty keys
produces.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:dn","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:j","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:j","@type":"Offer","permission":[{"action":"use"}]}],"distribution":null}]}
```

### D14 — grounded

`catalog-schema.json` permits a `catalog[]` of sub-catalogs, which is how a
federated broker advertises; the sub-catalog's dataset has a single-object
distribution. The sub-catalog stays raw, so its bad shape costs nothing —
which is precisely the treatment `distribution` no longer gets.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:broker","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:k","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:k","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:k"}]}],"catalog":[{"@id":"urn:catalog:member","@type":"Catalog","participantId":"urn:participant:member","dataset":[{"@id":"urn:dataset:k2","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:k2","@type":"Offer","permission":[{"action":"use"}]}],"distribution":{"@type":"Distribution","format":"S3-PULL","accessService":"urn:service:k2"}}]}]}
```

### D15 — invented

Three distributions where only the third carries a readable format.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:late","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:l","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:l","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","accessService":"urn:service:l1"},{"@type":"Distribution","format":"","accessService":"urn:service:l2"},{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:l3"}]}]}
```

### D16 — grounded

Expansion leaves values as `@value` arrays; keys stay compact but `format` is
`[{"@value": ...}]` and `@type` is an array.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:exp","@type":["Catalog"],"participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:m","@type":["Dataset"],"hasPolicy":[{"@id":"urn:offer:m","@type":["Offer"]}],"distribution":[{"@type":["Distribution"],"format":[{"@value":"HTTP-PULL"}],"accessService":[{"@id":"urn:service:m"}]}]}]}
```

### D17 — grounded — control, fails identically before and after

The pre-existing single-object failure the record already names, on
`hasPolicy`.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:hp","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:n","@type":"Dataset","hasPolicy":{"@id":"urn:offer:n","@type":"Offer","permission":[{"action":"use"}]},"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:n"}]}]}
```

### D18 — grounded — control, fails identically before and after

The same pre-existing failure on `dataset`.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:ds","@type":"Catalog","participantId":"urn:participant:provider","dataset":{"@id":"urn:dataset:o","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:o","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:o"}]}}
```

### D19 — invented

`format` as a number, the shape a loosely typed publisher produces.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:num","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:p","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:p","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":42,"accessService":"urn:service:p"}]}]}
```

### D20 — invented

`format` explicitly `null`.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:fn","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:q","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:q","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":null,"accessService":"urn:service:q"}]}]}
```

### D21 — invented — REGRESSION

Two datasets: the first has a single-object distribution, the second is
impeccable. Measures whether one bad dataset costs the whole document. It does.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:mix","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:r1","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:r1","@type":"Offer","permission":[{"action":"use"}]}],"distribution":{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:r1"}},{"@id":"urn:dataset:r2","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:r2","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:r2"}]}]}
```

### D22 — invented

Question 4: an unreadable entry (bare IRI string) ahead of one carrying a
format.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:arm","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:s","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:s","@type":"Offer","permission":[{"action":"use"}]}],"distribution":["urn:distribution:s0",{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:s"}]}]}
```

### D23 — invented

Question 4: an object format ahead of a string one; measures whether the
unreadable entry can produce a wrong value. It cannot.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:arm2","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:t","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:t","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":{"@id":"S3-PUSH"},"accessService":"urn:service:t1"},{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:t2"}]}]}
```

### D24 — invented

`distribution` present but empty, which the schema's `minItems` forbids and a
publisher with nothing to distribute still emits.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:emp","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:u","@type":"Dataset","hasPolicy":[{"@id":"urn:offer:u","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[]}]}
```

### D25 — invented

Question 5: two datasets advertising different formats.

```json
{"@context":["https://w3id.org/dspace/2025/1/context.jsonld"],"@id":"urn:catalog:two","@type":"Catalog","participantId":"urn:participant:provider","dataset":[{"@id":"urn:dataset:sample","@type":"Dataset","hasPolicy":[{"@id":"urn:dataset:sample#offer","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"HTTP-PULL","accessService":"urn:service:v1"}]},{"@id":"urn:dataset:sample-resume","@type":"Dataset","hasPolicy":[{"@id":"urn:dataset:sample-resume#offer","@type":"Offer","permission":[{"action":"use"}]}],"distribution":[{"@type":"Distribution","format":"S3-PUSH","accessService":"urn:service:v2"}]}]}
```
