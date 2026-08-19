# Design: policy constraints (validity-period)

`DECISIONS.md` §14 already fixed v1's scope: evaluate exactly two policy
shapes — unrestricted use, and a validity-period constraint. Any other
constraint parses and then the negotiation is rejected. §23.4 and §24.6
record the gap this milestone closes: the validity period this connector
already enforces (`config.Dataset.ValidityUntil`, checked by `isValid` at
negotiation time) has never once appeared on the wire, and nothing —
negotiation or data plane — re-checks it after `AGREED`. A negotiated
agreement today always carries `Permission{Action: "use"}` and nothing else,
regardless of what the dataset's config says.

This milestone makes the wire honest and adds the enforcement point that
does not exist yet: the data plane, on every access, not only negotiation,
once.

## Scope

In: the provider expresses a dataset's `ValidityUntil` as a real ODRL
constraint on the Offer (catalog and negotiation) and the Agreement; the
consumer recognizes exactly that one constraint shape as enforceable and
accepts it instead of rejecting on sight; the data plane refuses a pull once
the window has closed, even for a transfer already `STARTED`.

Out: any other constraint shape (purpose, spatial, count/quota, …) — §14
already decided these are parsed and rejected, not built. Out too: consumer-
side local enforcement of an accepted constraint — this connector as
consumer never serves the bytes, so nothing downstream needs to act on a
constraint it merely agreed to.

## Wire shape

```json
{"action": "use", "constraint": [{"leftOperand": "dateTime", "operator": "lteq", "rightOperand": "2027-01-01T00:00:00Z"}]}
```

Bare, unprefixed terms (`dateTime`, `lteq`), matching this project's existing
`useAction = "use"` convention: the DSP `@context` resolves them through the
imported ODRL vocabulary without a prefix, the same way `"use"` already does.
A dataset with no `ValidityUntil` still emits `Permission{Action: "use"}` with
no `constraint` key at all — byte-identical to every wire shape this project
already produces and every CN suite the TCK runs today.

## Provider: emit the term it already enforces

One helper, `buildPermission(validityUntil *time.Time) []Permission`, replaces
every inline `Permission{Action: useAction}` this connector writes about its
*own* datasets:

- `buildDataset` (catalog Offer) — gains the dataset's `ValidityUntil`.
- `newNegotiationOffer`, provider call site only (`buildOfferMessage`) — the
  two consumer-role call sites (`buildConsumerRequestMessage`,
  `buildCounterRequestMessage`) keep passing `nil`: those offers echo a
  *remote* provider's dataset, which is never something this connector's own
  config has an opinion about.
- `buildAgreementMessage` — looked up by `n.DatasetID` via
  `findConfiguredDataset`, the same accessor `isValid` already uses.

`buildAgreementMessage`'s signature changes from
`(n store.Negotiation, publicURL, participantID string)` to
`(cfg config.Config, n store.Negotiation)`: it already needs a config lookup
now, and `publicURL`/`participantID` are just `cfg` fields. One caller
(`negotiation_handler.go`), already holding `h.cfg`.

## Consumer: accept the one shape it can name

`carriesConstraint` (any constraint at all → reject) is replaced by
`hasUnenforceableConstraint`: for each rule in `permission`, an empty
`Constraint` is fine; a non-empty one is fine only if it is *exactly* the
validity-period shape — one element, `leftOperand: "dateTime"`,
`operator: "lteq"`, `rightOperand` parses as RFC 3339. Anything else
(multiple constraints, an unrecognized operand, a malformed timestamp, the
existing test fixtures' `spatial`/`eq`/`"EU"`) is unenforceable and still
causes rejection.

`decideOfferReaction`/`decideAgreementReaction` keep their exact signatures —
only what the bool argument means changes, from "a constraint is present" to
"an unenforceable constraint is present". Both call sites
(`negotiation_consumer_handler.go`) swap `carriesConstraint(...)` for
`hasUnenforceableConstraint(...)`.

## Data plane: enforce it, not just at negotiation

`handleData` (`data_handler.go`) currently resolves
`AgreementID → DatasetID → config.Dataset` once, for `SourceFile`, in
`sourceFileFor`. That becomes a single `resolveDataset(agreementID) (config.Dataset, bool)`
used for both the existing source-file check and a new one, inserted after
the `STARTED`-state check and before the source-file check:

```go
if ds.ValidityUntil != nil && !time.Now().Before(*ds.ValidityUntil) {
    writeError(w, TransferErrorType, http.StatusConflict,
        "the access window for this transfer's dataset has closed")
    return
}
```

`409`, matching the two sibling checks it sits beside (wrong state, no
`source_file`) — the same family: a currently-true precondition failed, not a
structural or ownership problem. This is the one check in this milestone
that was not already happening in some other form: a `STARTED` transfer today
serves bytes forever, with no re-check of anything after the state
transition. This closes exactly that gap.

## Regression risk: `cn-expired` starts carrying a constraint

`test/tck/dsbox.yaml`'s `urn:dataset:cn-expired` (used by `CN:02-01`,
`CN:02-05`, `CN:02-06`) already sets `validity_until` in the past. Once the
provider emits `ValidityUntil` as a wire constraint, any Offer this connector
pushes for that dataset carries one for the first time — relevant to
`CN:02-01`, whose `outcomeOfferThenTerminate` path pushes an Offer before
terminating. `docs/milestone-sequence.md`'s finding that "the CN suites
negotiate unconstrained offers" was true when written and needs re-checking
against this change specifically, with `make tck`, before this milestone is
called done. `cn-match`/`cn-mismatch` carry no `validity_until` and are
unaffected.

## Testing

Unit tests for `buildPermission` (nil → bare, set → the one wire shape),
`hasUnenforceableConstraint` (empty, the recognized shape, each rejected
shape including the existing `spatial` fixtures), the three provider
builders now attaching the constraint end to end, and `handleData`'s new
check (before window closes: served; after: `409`; no `ValidityUntil`
configured: unaffected). `make tck` is the regression gate per the risk
above, not a source of new evidence — no CN or TP suite asserts constraint
content.
