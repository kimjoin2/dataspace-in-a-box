# Follow-ups

Known imprecisions and small gaps, each judged not worth blocking a milestone
on. They are recorded here rather than left in a reviewer's head, so that the
next person to touch the area can close them in passing.

Delete an entry when it is fixed. An entry that keeps getting deferred is
telling you something — either it does not matter, or it needs its own decision.

## From the catalog milestone (2026-08)

**`whitelist` terminology outlived the whitelist.** The gate now holds an
`expected` map of per-suite result counts, not a list of prefixes.
`cmd/tckgate/main_test.go` still names a test
`TestFailureOutsideTheWhitelistIsIgnored`, and `CLAUDE.md`'s "Build order"
section still describes the gate as a per-suite whitelist without mentioning
that the count is exact. Neither is wrong, only imprecise — an exact-count gate
is a whitelist carrying counts. Worth correcting the next time either file is
edited for another reason.

**`DECISIONS.md` §22.4 omits a carve-out the code implements.** The decision
reads as though any catalog request carrying `filter` is rejected. The
implementation treats an explicit JSON `null` as the absence of a filter and
serves the request normally, which is correct. The decision text should say so.

**The catalog's own `@id` is not routed.** A catalog document identifies itself
as `{public_url}/2025-1/catalog`, but nothing serves that path, so a `GET`
returns `net/http`'s plain-text `404 page not found` — the same non-JSON 404
this milestone fixed for dataset requests. DSP does not require the identifier
to be dereferenceable, so this is not a compliance gap. It is still a
plain-text body on a protocol endpoint's namespace, which is what the project
decided not to emit.

**`internal/dsp/version_test.go` cites evidence that is no longer in the
repository.** A comment credits `tck-output.txt` for the values the TCK
accepted. That file is deliberately untracked (`.gitignore` explains why, and
CI uploads it as an artifact). The same evidence survives in
`cmd/tckgate/testdata/passing.txt`, which the comment should point at instead.

**`TestEmptyDatasetIDIsAnError` does not isolate the rule it names.** Deleting
the explicit `id == ""` check in `validateDatasetID` leaves the test passing,
because `url.Parse("")` already fails `IsAbs()`. The behavior is enforced
either way; the test just does not prove which check enforces it. Asserting on
the error message would fix that.

## From the contract negotiation (provider) milestone (2026-08)

**`CN:02-07` has no implemented trigger.** Every autonomous termination this
milestone implements is checked once, at accept-time: either the initial
request's offer matches an expired dataset, or an `ACCEPTED` event arrives
for one. `CN:02-07`'s sequence reaches a clean `AGREED` — meaning the offer
matched and passed the validity check — and only terminates, unprompted,
after `VERIFIED`. A check performed once at accept-time cannot explain a
rejection surfacing later on a negotiation that already passed it. Tracked as
a named gate exemption (`cmd/tckgate/main.go`'s `exempt` map) rather than
silently dropped. Full reasoning:
`docs/superpowers/specs/2026-08-11-contract-negotiation-provider-design.md`,
"`CN:02-07` does not fit this account". Closing this means finding DSP's
actual intended trigger for an unprompted post-verification termination —
not yet determined from the public TCK sources this project is permitted to
use.
