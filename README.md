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
| Version metadata | `MET` | gated in CI |
| Catalog | `CAT` | gated in CI |
| Contract negotiation | `CN` (provider role) | gated in CI, 14 of 15 (`CN:02-07` is a tracked, named gap — see `docs/follow-ups.md`) |
| Contract negotiation | `CN_C` (consumer role) | gated in CI, 16 of 16 |
| Transfer process | `TP` (provider role) | gated in CI, 15 of 15 |
| Transfer process | `TP_C` (consumer role) | gated in CI, 15 of 15 |

Every suite the TCK runs is now in the gate's whitelist.

Current TCK pass rate: **64 of 65 tests total** (`MET` 1 of 1, `CAT` 3 of 3,
`CN` 14 of 15 — `CN:02-07` fails by design, tracked rather than hidden —
`CN_C` 16 of 16, `TP` 15 of 15, `TP_C` 15 of 15). All 65 are required by the
CI gate. The single failure is `CN:02-07`, which fails not because a protocol
is missing but because no connector-side mechanism in this milestone produces
the behavior it requires.

This connector now moves data. A dataset with a `source_file` is served over
HTTP-PULL to a counterparty holding a started transfer, and a consumer that
receives a `dataAddress` fetches it and writes it down. The data endpoint sits
behind the same participant credential as everything else and adds three
checks: the transfer must exist, be `STARTED`, and belong to the participant
asking. A `dataAddress` is an address rather than a capability — possessing
one grants nothing.

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
makes carries one (DECISIONS.md sections 9 and 10). The suite above runs with
that on; removing the harness's credential fails 63 of the 65.

Two limits are worth stating plainly rather than leaving to be discovered.
`did:web` resolution exists (`dsops resolve <did:web:...>`) but is not part
of authenticating a request — the roster is still what every request checks
against, resolution is only how an operator builds or checks a roster entry,
on purpose (see `DECISIONS.md` section 9 and the design spec: putting
resolution on the request path would add a network dependency to
authentication and change nothing about who ends up trusted). And a captured
credential can be replayed until it expires, five minutes after it was
minted.

    make demo   # two connectors, one negotiated agreement, one file moved
    make tck    # the compliance gate: 64 of 65, 0 outside it

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

**The roster is signed, but its own distribution is still a bootstrap
problem.** `roster.json` now carries the operator's signature, verified
against `roster_signer` at load — an unsigned or forged roster is a startup
failure. What that does not solve: how `roster_signer` itself, or a first
copy of the roster, reaches every connector that must trust it. That is a
governance question `DECISIONS.md` section 9 leaves to "diffed in git", not
one a signature scheme answers by itself.

**Transfers are small and one-shot.** The write timeout bounds how large a
file can finish; there are no range requests and no resumption, so a failed
pull refetches from zero.

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
