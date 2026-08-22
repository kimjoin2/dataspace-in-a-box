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
`dev_mode` (`internal/config/config.go:170-171`) relaxes only the `https`
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

## From the policy cross-check (2026-08)

Found while checking whether a use-count constraint could be enforced, not by
building it. All three predate that work. They are recorded together because
they compose: each is reachable by any roster participant, and two of the three
end in bytes.

Severity is high, not critical. Every path below needs a roster credential —
the operator has to have put your public key in `roster.json` and re-signed it,
and impersonating someone else needs their private key. This is not open to the
internet. It is, however, exactly the boundary a dataspace exists not to
collapse: two participants share a roster precisely because they are strangers.

**An authenticated request may act on an exchange it is not party to.**
`issuerFrom` (`internal/dsp/auth_middleware.go:22`) recovers the verified
counterparty of every DSP request, and exactly one handler compares it to the
row it acts on: `handleData` (`internal/dsp/data_handler.go:77`). Everything
else resolves by path id alone. On the transfer side that is
`transferHandler.lookup` (`internal/dsp/transfer_handler.go:558`), feeding
`applyTransition` (`:459` — start, completion, suspension, termination) and
`handleGetTransfer` (`:414`). On the negotiation side it is seven resolvers,
not one: `negotiationHandler.lookup` (`internal/dsp/negotiation_handler.go:372`)
serves only `handleReRequest` and `handleVerification`, while `handleEvent`
(`:180`), `handleTermination` (`:288`), `handleGetNegotiation` (`:339`),
`handleOffers` and `handleAgreement`
(`internal/dsp/negotiation_consumer_handler.go:120`, `:231`) each do their own
two-table lookup. Suspending or terminating someone else's transfer answers
`200`; so does driving their negotiation to ACCEPTED or TERMINATED.

Closing it needs no schema change. All four exchange tables already carry
`counterparty_id`, populated from the verified issuer
(`internal/store/store.go:284-291`), and the field is already trusted for
outbound addressing. What it needs is the comparison, in seven places rather
than the obvious two, and a decision on the refusal status — `403`, per §25.1's
standing rule that a DSP rejection is never `404` because the counterparty's
client aborts on one, and matching the "this transfer is not yours" this
connector already answers at the data endpoint.

**`agreements` cannot say whose agreement it is, and that ends in bytes.** The
table (`internal/store/store.go:157`) holds `consumer_pid`, empty for imported
agreements by design, and no participant id. `handleTransferRequest` therefore
looks an agreement up by id alone and explains why it must
(`internal/dsp/transfer_handler.go:123-129`). A roster participant that knows
another's agreement id opens a transfer under it, becomes that transfer's
counterparty at `:167`, and then passes `handleData`'s ownership check. The
default transfer sequence starts the transfer without any further message
(`:219`), so the sequence is two requests and a `GET`.

Id secrecy is the only guard, and it is weaker than it looks: `:141` answers a
wrong guess with a distinguishable `400`, and imported agreement ids are
operator-chosen rather than random — this repository's own fixtures use
`urn:uuid:tck-tp-01-01` (`test/tck/dsbox.yaml`) and `urn:uuid:example-agreement`
(`config.example.yaml`). Closing it needs an owner column set from the verified
issuer, plus a way to name one on import. Note the constraint that shape must
satisfy: `test/tck/run.sh` seeds twelve agreements with no owner, so an empty
owner has to keep meaning "not known" and stay permitted, or all thirty TP and
TP_C results fail. That makes the fix partial by construction — imports without
a named owner stay exactly as open as they are today.

**A participant can forge an agreement against itself and then read the data.**
The worst of the three, and the one an owner column does not help with.
`handleAgreement` (`internal/dsp/negotiation_consumer_handler.go:266-277`)
writes an agreement row straight from the message body — id and target both
taken verbatim — with no check that the target is a dataset this connector
would serve. It computes `wrongTarget` at `:291` and uses it only to choose the
reaction, after the row is already written, so even a rejecting policy leaves
the row behind. Since `POST /negotiations/initiate` is reachable by any roster
participant and discloses this connector's own consumer pid to the address the
caller supplied, the whole sequence is: initiate a negotiation naming yourself
as provider, read the consumer pid out of the request that arrives, post a
forged agreement to it, then open a transfer citing that agreement and pull.

Every ownership check above passes on that path, because the forger is the
honest owner of what it forged. Closing it is a different fix: refuse to record
a consumer-role agreement whose target is not a dataset this connector
requested, and refuse a provider-role transfer request citing an agreement this
connector holds as a consumer — serving data as provider under a contract where
this connector is the consumer is role confusion regardless.

**Three stale claims to correct alongside any of the above.** `DECISIONS.md`
§23.11 predicted all of this was "properly closed by enforcing §10's
connector-to-connector JWT on this listener, not by patching the handlers one
field at a time"; the JWT shipped (§27) and narrowed the attacker set without
closing anything, and §24.2 inherits the same posture by reference. §25.2 says
`agreements` has "exactly two writers" — `internal/store/store.go:78` already
says three. And `internal/dsp/negotiation_consumer_handler.go:47-51` and
`internal/dsp/transfer_consumer_handler.go:41-45` still describe the two
initiate hooks as "open to anonymous callers", which is false with
`require_auth` on and understates who can reach them.
