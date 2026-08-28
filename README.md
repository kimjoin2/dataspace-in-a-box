# dataspace-in-a-box

A minimum operational dataspace. The goal: one binary, one config file, one
SQLite file — clone, run, and have something working in about ten minutes.

This is an independent implementation of the
[Dataspace Protocol 2025-1](https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/),
not a wrapper around an existing connector. Compliance is verified by the
official [DSP TCK](https://github.com/eclipse-dataspacetck/dsp-tck) running in
CI, so the claim is a build artifact rather than a sales conversation.

## Status: it moves files, and not much else yet

The repository has been public since its first commit, so this section has
always said what was true at the time. What is true now: two connectors can
authenticate, negotiate an agreement that can carry a real term — a validity
period — and move a real file that the data plane stops serving once that
term expires, and `make demo` does exactly that end to end, on a roster that
now carries the operator's own signature rather than being trusted only
because it sits on the right disk. What is not yet true is below the table —
the gaps are named rather than left to be found.

| DSP protocol | TCK suite | Status |
|---|---|---|
| Version metadata | `MET` (served only) | gated in CI |
| Catalog | `CAT` | gated in CI |
| Contract negotiation | `CN` (provider role) | gated in CI, 15 of 15 |
| Contract negotiation | `CN_C` (consumer role) | gated in CI, 16 of 16 |
| Transfer process | `TP` (provider role) | gated in CI, 15 of 15 |
| Transfer process | `TP_C` (consumer role) | gated in CI, 15 of 15 |

Every suite the TCK runs is now in the gate's whitelist.

The catalog row lost its *served only* marking, and the reason is worth
stating rather than leaving to the table's shape. This connector now asks a
counterparty for its catalog as well as answering one: `GET /catalog` on the
management listener resolves a roster participant to an address, sends a
catalog request, and reports back the negotiable pairs it found
(`DECISIONS.md` section 38). Version metadata keeps the marking and means it —
this connector serves a version document and requests none, so those rows say
different things and the table should not be read as symmetric. The `CAT`
suite covers only the half that was there before, because the TCK plays the
consumer in it; the client's evidence is `go test` and `make demo`.

Current TCK pass rate: **65 of 65 tests total** (`MET` 1 of 1, `CAT` 3 of 3,
`CN` 15 of 15, `CN_C` 16 of 16, `TP` 15 of 15, `TP_C` 15 of 15). All 65 are
required by the CI gate, and none are exempted.

This connector now moves data. A dataset with a `source_file` is served over
HTTP-PULL to a counterparty holding a started transfer, and a consumer that
receives a `dataAddress` fetches it and writes it down. The data endpoint sits
behind the same participant credential as everything else and adds three
checks: the transfer must exist, be `STARTED`, and belong to the participant
asking — the third of which is no longer a rule this one endpoint invented for
itself, see below. A `dataAddress` is an address rather than a capability —
possessing one grants nothing.

The TCK cannot verify any of that. No test in either transfer suite sends,
receives, or asserts a byte, so a green suite is not evidence that data moves.
That evidence is `make demo`, which stands two connectors up with distinct
identities and a shared roster, has them authenticate, negotiate an agreement,
run a transfer, and move a real file — then diffs what arrived against what
was sent and exits non-zero if they differ.

A protocol counts as done only when its TCK suite is added to the gate's
whitelist, so this table cannot drift ahead of reality.

Connectors now authenticate to each other. Every DSP endpoint except the
version document requires a JWT signed EdDSA over Ed25519 by a participant in
this connector's roster and addressed to it, and every call this connector
makes carries one (DECISIONS.md sections 9 and 10). Both halves are bounded by
the roster's own expiry: past it every route behind that credential check
answers 409 without reading the credential — the version document, which is
deliberately outside the check, goes on answering — and the connector attaches
nothing and sends nothing rather than signing with a key whose roster it no
longer trusts (DECISIONS.md section 36). The suite above runs with that on.
Removing the harness's credential failed 63 of the 65 when that was measured,
at the milestone that added the check — a figure this section does not
re-measure and which no longer describes today's harness, because the same
string is now both the credential the TCK presents and the connector's
management token (DECISIONS.md section 35.4).

And an authenticated caller is now held to the exchanges it is actually party
to. That used to be one endpoint's check and is now a property of the
connector (`DECISIONS.md` section 32): a message about a negotiation or a
transfer is refused `403` unless it comes from the participant that exchange is
with, an agreement records who it is with and a transfer request citing it is
checked against that, and this connector will not serve data as provider under
an agreement it holds as consumer.

What it does not close is recorded there rather than hidden. An agreement with
no recorded owner stays open to any roster participant that knows its id, and
that is not only the imports that name nobody. An agreement concluded while
`require_auth` was off has no owner and can never be given one: authentication
being off is what makes the identity absent, so there is nothing to recover —
and that flag exists for the migration from anonymous to authenticated, which
is the upgrade path this repository documents. Agreements negotiated before
this work have the same empty owner; those are recoverable in principle and
deliberately not recovered, because this connector has no update path by
design. Messages arriving about this connector's *consumer-role* exchanges
used to be exempt from all of this, because the only identity available for
them was one an operator typed rather than one this connector verified. They
are checked now; what made that possible is `DECISIONS.md` section 35, and
the paragraph headed "Starting an exchange is an operator action" further
down says how.

Two limits are worth stating plainly rather than leaving to be discovered.
`did:web` resolution exists (`dsops resolve <did:web:...>`) but is not part
of authenticating a request — the roster is still what every request checks
against, resolution is only how an operator builds or checks a roster entry,
on purpose (see `DECISIONS.md` section 9 and the design spec: putting
resolution on the request path would add a network dependency to
authentication and change nothing about who ends up trusted). And a captured
credential can be replayed until it expires: five minutes after it was
minted, plus the minute of leeway a verifier allows so that clocks need not
agree exactly — and longer than that against a verifier whose clock lags the
minter's, though it stops climbing at the hour beyond which a verifier
refuses a credential outright, plus that same minute of leeway
(`DECISIONS.md` section 37).

    make demo   # two connectors, one negotiated agreement, one file moved
    make tck    # the compliance gate: 65 of 65, 0 outside it

There is no release yet, and gaps are worth knowing before anyone mistakes
this for finished.

**Contracts carry exactly one kind of term.** A dataset's `validity_until`
now becomes a real ODRL constraint on its offer and agreement — not only a
negotiation-time gate — and the data plane checks it on every pull, not only
once at `AGREED`: a transfer that already reached `STARTED` still gets cut
off once the window closes. `DECISIONS.md` section 14 fixes this at exactly
two evaluated shapes, unrestricted use and a validity period; any other
constraint still parses and is rejected, the rule `CLAUDE.md` states without
exception — a constraint which is not enforced is not accepted. Usage
purposes, spatial restrictions, and counts are not evaluated and are not a
present feature.

**The roster is signed and expires, but its own distribution is still a
bootstrap problem.** `roster.json` carries the operator's signature, verified
against `roster_signer` at load, plus a revision and an expiry that the same
signature covers — an unsigned or forged roster is a startup failure, so is
one missing either field, and so is one already past its expiry. The expiry
is what bounds revocation: past it a superseded roster stops verifying
anywhere, including on a connector nobody restarted, and this connector
refuses every request that needs a credential rather than acting on a
document it no longer trusts. It does not go quiet: the version document,
which sits outside that check, still says what protocol this connector
speaks. The revision is narrower than it sounds — it stops *this* connector
being handed an older roster than one it has already run, and it is exchanged
with nobody. What none of that solves: how `roster_signer` itself, or a first
copy of the roster, reaches every connector that must trust it. That is a
governance question `DECISIONS.md` section 9 leaves to "diffed in git", not
one a signature scheme answers by itself. Replacing an expired roster is the
same problem arriving on a deadline, and every connector in the dataspace
shares the deadline (`DECISIONS.md` section 36.5).

**A resumed transfer trusts a size check, not a content check.** A pull
that gets interrupted resumes from where it left off on the next restart —
the provider answers `Range: bytes=N-` with `206` and the remaining bytes,
or `416` if its file is no longer at least that long, which is
one half of the integrity check. The other half is length: a counterparty
that states a `Content-Length` or a complete `Content-Range` has that value
recorded on the first attempt and compared on every later one, so a
replacement of a different size is refused rather than appended to. A
replacement of exactly the same size is still not caught, and a counterparty
that states no length at all is checked only by the byte ceiling. There is
no digest. An orphaned partial download from a transfer that never restarts
is not cleaned up either.

**A transfer is bounded by progress and by size, not by the clock.** Both
sides give up only after `data_idle_timeout` passes with no bytes moving, so
elapsed time no longer decides what fits. Size is bounded separately, and
unconditionally: `max_download_bytes` caps every pull, cutting a counterparty
that states a `Content-Length` at exactly the same ceiling as one that streams
chunked, so a dataset larger than it does not transfer until an operator
raises it. Two things it is *uniquely*, which is what makes it worth setting
deliberately — it is the only bound at all when a counterparty states no
length, and the only backstop against one that dribbles slowly enough never
to go idle. What still has no measurement is size: `make demo` moves kilobytes
and the TCK moves no bytes, so nothing here proves how large a transfer this
can actually carry.

**Starting an exchange is an operator action, and only an operator can do
it.** The hooks that tell this connector to negotiate or transfer as consumer
used to sit on the public DSP listener, where any roster participant could
call them — and each took the counterparty's name out of the request body,
made it the audience of a credential this connector signs, and sent that
credential to an address the same caller chose. Both now sit on the management
listener behind its token, alongside `GET /agreements` and `GET /transfers`,
and, with authentication on, both refuse a `providerId` this connector's
roster does not list. That is what turns the counterparty on a consumer-role
exchange into an identity rather than a string, so a message arriving about
one is checked against it the way a provider-role message already was.
`DECISIONS.md` section 35 records it. What it left open — that being in this
connector's roster was not the same as being the participant at the address an
initiate call named, so an operator who pointed one at the wrong connector
still handed it a signed credential — is closed by `DECISIONS.md` section 38:
a roster entry may now carry the address its participant is reached at, and
where it does, that is the address an initiate call dials. The operator names
a participant; the signed registry decides where that participant is.

**A transfer now leaves a record, and an operator can read it.**
`GET /transfers` lists every transfer this connector holds in both roles,
read-only and behind the same management token as `GET /agreements`, so the
question "did the data actually arrive?" has somewhere to be asked. A failed
pull and a successful one are no longer the same row: a consumer transfer
carries the bytes received, where the file was published, when it completed,
and — when it did not — the reason it stopped, as a sentence rather than a
code. The four are written together, so a row cannot read as both completed
and failed. The provider side is louder too: `handleData` now logs who
collected the data, which transfer and dataset it came from, and how many
bytes left — the identity the connector already had and previously used only
to turn the wrong caller away. `DECISIONS.md` section 34 records all of it,
including what it costs.

Two things that record does not do. An empty `dataPath` does not prove
nothing was ever fetched — the row describes the latest attempt, so a failed
re-pull blanks it while the file an earlier attempt published is still on
disk. And there is still no way to *act* on what it shows: an operator who
sees a failed pull has no endpoint to retry it with, which is
[`docs/goal-gap-analysis.md`](docs/goal-gap-analysis.md)'s P2 and is not
closed here. Nor is the retention rule for the partial files an abandoned
pull leaves behind, which is the first entry an operator moving real data
should read in [`docs/follow-ups.md`](docs/follow-ups.md).

Everything else known and unfixed is in [`docs/follow-ups.md`](docs/follow-ups.md),
with the reasoning for each, and the order the remaining milestones should be
built in is in [`docs/milestone-sequence.md`](docs/milestone-sequence.md).

## Why this exists

Open-source dataspace implementations tend to be construction kits rather than
runnable systems — powerful, extensible, and reliably requiring outside help to
assemble. That gap is usually filled by paid services. This project treats
closing it as the point: neutral code, honest documentation, and a compliance
claim anyone can verify.

The design decisions and the trade-offs accepted for each are recorded in
[`DECISIONS.md`](DECISIONS.md). Read it before proposing an architectural
change.

## Scope

In: the four core DSP protocols, `did:web` identity with an operator-signed
participant roster, a management API, and an embedded web UI.

Out: a plugin system, a general policy engine, Decentralized Claims Protocol,
and TLS termination. Each omission is deliberate and explained in
`DECISIONS.md`.

## License

Fair Source, not OSI open source. Licensed under
[FSL-1.1-ALv2](LICENSE.md): free for internal use, modification, research, and
production operation — the only restriction is on reselling this as a competing
commercial product. Each released version converts to Apache 2.0 two years
after its release.

Copyright 2026 b7g.
