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
`dev_mode` (`internal/config/config.go:401-402`) relaxes only the `https`
requirement on `public_url`; it does not reach `isDisallowedCallbackIP`
(`internal/dsp/callback.go:175-178`), so `POST /negotiations/initiate` with
`connectorAddress: http://127.0.0.1:8090` is rejected `400`. Pre-existing —
§23.6 chose that guard's reach deliberately, and widening it is a design
decision rather than a cleanup, which is why this is recorded and not fixed
here. It still deserves attention: for a project promising "clone, run,
working in ten minutes", two instances on localhost is the first thing a
reader tries, and the failure gives them a `400` with no hint of why.

## From the transfer process (provider, Phase A) milestone (2026-08)

**The 200 ms `transferStepDelay`'s margin is measured for later steps and
still unmeasured for the first.** The margin question was answered the hard
way on 2026-08-19: the *first* push had no pause at all, and under load — the
demo's image builds competing for the machine — a start pushed 22 ms after the
acknowledgment drew a `409` from a counterparty that had not finished
recording that acknowledgment, then `404` as it moved on. `TP:02-01` failed
twice in a row and passed again on a quiet machine. The first step now takes
the same pause as the others, which removes that race.

What is still unmeasured is how much slack 200 ms actually has. The way to
find out is to shorten it deliberately until pushes start being refused. It is
worth doing, and it is now cheaper than it was: `make demo` and `make tck` can
be run together to put the machine under exactly the load that exposed this.

## From the transfer range/resumption milestone (2026-08)

**An orphaned `.partial-<consumerPID>` file is never cleaned up.** A
transfer that is interrupted and then terminates instead of restarting
leaves that file on disk forever — the deterministic name this milestone
introduced for resumption makes what was already a smaller, random-named
leak risk into a larger, predictable one. Solving it needs a retention or
garbage-collection policy this project has none of yet: an obvious rule
("delete a partial file once its transfer reaches a terminal state") needs
the partial-file cleanup to happen somewhere that already knows the
transfer terminated, which today is a different code path
(`handleTransferTermination`/`handleTransferCompletion`) than the one writing the file
(`pullTransferData`), and wiring the two together is a design decision, not
a cleanup.

**Updated by the data-path milestone (2026-08): the size bound on this leak
is gone.** When this entry was written, a pull inherited the callback
client's ten-second overall timeout, so an orphaned partial was at most what
ten seconds of transfer produced — a leak measured in megabytes on any
ordinary link. That timeout was removed so a large transfer could finish
(`DECISIONS.md` §33), and an interrupted pull now leaves behind whatever
arrived before the counterparty went quiet, bounded only by
`max_download_bytes` — 8 GiB by default, per orphan. Nothing about the
cleanup problem changed; what changed is the cost of not solving it, which
moves this from a tidiness item to the one entry on this page an operator
running real data should read first. `DECISIONS.md` §33's *Trade-off
accepted* records the same conclusion from the other side.

## From the exchange-authorization milestone (2026-08)

The policy cross-check's three entries are gone: `DECISIONS.md` §32 closed
them, and the three stale claims recorded alongside them are corrected in
place. The two consequences §32.3 deferred are gone too — the initiate-hook
milestone closed both, and `DECISIONS.md` §35 is where they are answered. What
survives is the part of the third cross-check entry whose reasoning is worth
more than the finding was.

**A forged consumer-role agreement row survives, and four things follow from
that.** §32.4 refuses to serve data as provider under an agreement this
connector holds as consumer, which closes the byte exit completely — but it
closes it at consumption, not at intake, so `handleAgreement` still writes the
row. It cannot detect the forgery: the message is exactly what an honest
provider sends in a negotiation the peer legitimately owns.

**Who can write one narrowed with §35, and the rest of this entry should be
read in that light.** `handleAgreement` now refuses a message whose verified
issuer is not the row's counterparty (§35.3), and that counterparty can only
be set by the operator, naming a participant the roster lists (§35.1, §35.2).
So the writer is no longer any roster participant that learned one of this
connector's consumer pids — it is the participant the operator asked this
connector to negotiate with, which is why `SECURITY.md` describes it that way.
What remains:

- **Id squatting.** A row written first under an id its rightful owner meant to
  import makes that id permanently unimportable — `CreateAgreement` refuses
  duplicates and §25.3 guarantees no delete path.
- **An existence oracle that leaves litter.** The duplicate refusal
  distinguishes "that id is taken" from "that id is free", and a miss writes a
  row on the way to finding out.
- **The audit surface cannot tell them apart.** A forged row is
  indistinguishable from a real one in `GET /agreements`, which is the only
  view an operator has. §32.5's `counterpartyId` helps for imports and
  negotiated agreements and not here: a forger's row names the forger honestly.
- **`handleTransferInitiate`'s agreement gate is still a sanity check, and
  the work that was supposed to change that has landed.** This bullet used to
  say the gate was decorative because it was satisfied by an id the caller
  minted, and that the verified-`providerId` work §32.3 deferred was what
  would change it. That work shipped in §35 and did not: the caller is now
  the operator on the management listener rather than whoever wrote the row,
  and the gate still asks only whether this connector holds an agreement with
  that id, never whether the operator is entitled to it. It compares nothing
  against the row's own `counterpartyId`. What would change it is the id-space
  separation this entry's closing paragraph names, not anything §35 was
  going to do.

Closing these needs the consumer-role agreement id space separated from the
provider-role one — a second table, or an `(agreement_id, origin)` key — which
reopens the "one table, one rule" argument in `store.Agreement`'s doc comment.
That is a design decision with its own spec, not a cleanup, which is why it is
recorded here instead of attempted. Severity is lower than the entries this
replaces: none of these ends in bytes.
