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

**A callback push retries on any non-2xx, including the ones that will never
succeed.** `attemptPush` treats every status at or above 300 the same, so a
`400` or a `403` — a permanent answer from a consumer that will say it again —
costs the full 5.5-second backoff schedule and a goroutine for its duration,
before being dropped anyway. Short-circuiting those is a small win with a
sharp edge: `404` must keep retrying, because it is exactly what the TCK's
async-listener registration race produces (`DECISIONS.md` §23.7), and no
other status code's behavior during that window has been observed. Worth
doing when there is a real TCK run to verify it against, not on reasoning
alone.

## From the contract negotiation (consumer) milestone (2026-08)

**Two package-level comments where one canonical package doc already
exists.** `internal/dsp/negotiation_client.go:1` and
`internal/dsp/negotiation.go:1-5` each open with a comment attached to the
`package dsp` clause, though `internal/dsp/version.go:1` holds the canonical
package doc — `go doc` concatenates them in file order, which is not what
either was written for. `negotiation.go` at least says where the real one
lives; `negotiation_client.go` does not. Fix both (detach them from the
package clause, or fold them into a file-purpose comment above it) or
neither; fixing one is the worst of the three states.

**Two test overrides of package vars still assign-and-restore, the pattern
`callbackRetryBackoffs` was moved out of.** `validateOutgoingCallback`
(`negotiation_handler_test.go:117`, and three `defer` restores) and
`terminateAfterOfferDelay` (`:233`) are both read on push goroutines that can
outlive the test that set them, which is structurally the same race the three
`callbackRetryBackoffs` restores were. They are clean today — `go test -race
-count=6 -shuffle=on ./internal/dsp/` passes, because every test that
overrides them waits for the push it triggered to land — and they predate the
consumer milestone. Now that CI runs `-race`, though, they are the most
likely source of a future flake on an unrelated PR. The fix is the one
`callbackRetryBackoffs` got: set once in `TestMain`, never restore.

**`TestSendAcceptedEvent_*` and `TestSendVerification_*` do not assert the
request path.** Both assert the body and the return value, and both build the
URL from a path template with the provider pid formatted into it — the thing
most likely to be wrong. Their siblings `TestSendCounterRequest_*` and
`TestSendConsumerTermination_*` both check `r.URL.Path` against the expected
literal. The two new `TestReactTo*_PicksUpAProviderPID*` tests now cover
those two paths incidentally, which is not the same as the direct sibling
coverage the other two have.

**No unknown-id-404 regression test for `handleTermination`'s dispatch
path.** `handleEvent` and `handleGetNegotiation` each have one
(`TestHandleEvent_UnknownIDIs404` and its catalog-side twin), in the same
literal shape. `handleTermination` gained the identical two-table dispatch in
this milestone and has only the found-in-consumer-table case
(`TestHandleTermination_DispatchesToConsumerBranch`). Nothing currently pins
that a pid in neither table still produces a JSON `404` rather than falling
through.

**`handleProviderAcceptedEvent` and `handleConsumerFinalizedEvent` declare an
identical anonymous decode struct.** Three fields (`@context`, `@type`,
`eventType`), byte-for-byte the same in both. Extracting a named
`eventEnvelope` next to `envelope` would remove the duplication, but with
exactly two call sites in one file it would also be an abstraction introduced
before a third case exists to shape it. Worth doing if a third event handler
ever appears — not before.

**Constraint-triggered terminations carry no `reason`, only `code: "1"`.**
`buildConsumerTerminationMessage` sets no `Reason`, so a counterparty whose
offer was rejected for carrying a constraint this connector cannot enforce
(§24.6) is told only that the negotiation ended. §14's case for rejecting
rather than ignoring is that "explicit rejection is honest"; a bare code is
explicit but not informative, and `TerminationMessage.Reason` already exists
(`omitempty`) to carry the sentence. Deferred rather than done because adding
a field to a message the TCK validates is a wire-shape change, and this is
worth doing when there is a real TCK run available to verify it against.

**Two `dsbox` instances on `127.0.0.1` cannot negotiate with each other.**
`dev_mode` (`internal/config/config.go:170-171`) relaxes only the `https`
requirement on `public_url`; it does not reach `isDisallowedCallbackIP`
(`internal/dsp/callback.go:175-178`), so `POST /negotiations/initiate` with
`connectorAddress: http://127.0.0.1:8090` is rejected `400`. Pre-existing —
§23.6 chose that guard's reach deliberately, and widening it is a design
decision rather than a cleanup, which is why this is recorded and not fixed
here. It still deserves attention: for a project promising "clone, run,
working in ten minutes", two instances on localhost is the first thing a
reader tries, and the failure gives them a `400` with no hint of why.

**Before the transfer-process milestone: split
`internal/dsp/negotiation_handler.go`.** 803 lines, with a 1,463-line test
file beside it. The seam already exists and needs no design work: routing and
the provider role stay, and a new `negotiation_consumer_handler.go` takes
`handleInitiate`, `startNegotiation`, `handleOffers`, `reactToOffer`,
`resolveProviderPID`, `handleAgreement`, `reactToAgreement`,
`handleConsumerFinalizedEvent`, and `handleConsumerTermination`. Do it as a
pure move, on its own, *before* that milestone's diff lands — deliberately
not done in the CN_C fix wave, because folding an 800-line move into the same
commit range that fixed three data races would have obscured both.

**For the transfer milestone: the consumer never checks that an inbound
agreement's `target` is the dataset it asked for.** `handleAgreement`
correlates by consumer pid alone, and `reactToAgreement` resolves policy from
the stored `DatasetID`, never from the message — so a provider could agree to
a different target than the one requested and this connector would verify it.
Harmless today only because nothing downstream reads the agreement. Transfer
process is exactly where an agreement stops being inert, so this closes there
or not at all. Same family as §24.6: do not adopt terms you did not ask for.

## From the transfer process (provider, Phase A) milestone (2026-08)

**That entry above is still open.** Phase A reads an agreement only to answer
"does a row with this id exist", so a transfer still cannot notice that the
agreement it cites covers a dataset nobody asked for. It becomes reachable in
Phase B, where `agreements.dataset_id` decides which bytes get served.

**`POST /agreements` accepts any non-empty `datasetId`, including one this
connector does not advertise.** The TCK fixture seeds
`urn:dataset:tck-transfer` and `test/tck/dsbox.yaml` advertises it, but the
import would have succeeded either way — nothing cross-checks the id against
`config.Datasets`. Harmless in Phase A, since nothing reads `dataset_id` yet.
Phase B serves bytes by resolving that id to a configured dataset, so it has
to decide there whether an unknown dataset is a `400` at import time or a
failure at pull time. Importing a contract for something this connector cannot
serve is the same defect family as §25.1, one level down.

**A terminal step on a timer and a real data plane cannot both be right.**
`transfer_policies` sequences are driven by `transferStepDelay` alone, so
`[STARTED, COMPLETED]` completes 200 ms after starting. Harmless in Phase A,
which moves no bytes. In Phase B `STARTED` is what authorizes a pull, so the
same configuration would cut access before a consumer could fetch anything.
Phase B has to either bound autonomous completion by something the data plane
knows (bytes served, a pull observed, an idle window) or document plainly that
a terminal step and a real `source_file` are mutually exclusive. Recorded in
`DECISIONS.md` §25.7's trade-off as well, because it is a consequence of that
decision rather than a defect in its implementation.

**The agreement guard's residual race is recorded only in a comment.** In
`negotiation_handler.go`'s `outcome.pushAgreement` branch, the negotiation is
re-read before the agreement row is inserted, so an agreement is not recorded
for a negotiation that was terminated while the push was still retrying. A
termination arriving *between* that re-read and the `INSERT` still leaves a
stale agreement row behind: the negotiation ends `TERMINATED` and the
agreements table says a contract exists. Knowingly accepted when the guard was
written — closing it needs one transaction spanning the state write and the
agreement insert, which is a larger change than that call site should make —
but a defect tracked only in a code comment is not tracked. The window is
small and the consequence is bounded today (a transfer could be started under
an agreement whose negotiation died), and it grows in Phase B, where that row
is what authorizes serving bytes.

**The 200 ms `transferStepDelay` has a margin nobody has measured.** The first
real `TP` run needed zero retries — all eight `callback endpoint rejected
push` lines in the connector log were negotiation pushes hitting the
pre-existing §23.7 registration race, and not one was a transfer push. So the
pause is comfortably sufficient on this machine and completely unbounded
anywhere else: a single observation of "never needed the retry" says nothing
about how much slack there was. The way to find out is to shorten it
deliberately until pushes start being refused, which is worth doing the next
time there is a reason to run the TCK repeatedly.
