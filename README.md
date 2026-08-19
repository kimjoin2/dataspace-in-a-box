# dataspace-in-a-box

A minimum operational dataspace. The goal: one binary, one config file, one
SQLite file — clone, run, and have something working in about ten minutes.

This is an independent implementation of the
[Dataspace Protocol 2025-1](https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/),
not a wrapper around an existing connector. Compliance is verified by the
official [DSP TCK](https://github.com/eclipse-dataspacetck/dsp-tck) running in
CI, so the claim is a build artifact rather than a sales conversation.

## Status: nothing works yet

The repository is public from its first commit, which means you are looking at
it before it does anything. Here is the honest state.

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

The current milestone serves the transfer process protocol in both roles.
It is the **control plane only**: this connector runs a transfer's lifecycle —
requested, started, suspended, completed, terminated — from either side, and
moves no data at all. That is not a gap the `TP`/`TP_C` suites would have
caught, because they do not move data either; no test in either sends,
receives, or asserts a single byte. A green transfer suite is therefore not
evidence that this connector can transfer data, and nothing here should be
read as claiming otherwise.

A protocol counts as done only when its TCK suite is added to the gate's
whitelist, so this table cannot drift ahead of reality.

Connectors now authenticate to each other. Every DSP endpoint except the
version document requires a JWT signed EdDSA over Ed25519 by a participant in
this connector's roster and addressed to it, and every call this connector
makes carries one (DECISIONS.md sections 9 and 10). The suite above runs with
that on; removing the harness's credential fails 63 of the 65.

Three limits are worth stating plainly rather than leaving to be discovered.
`did:web` resolution is not implemented — the roster is the resolution
mechanism, and participant identifiers are opaque strings. The operator's
signature over the roster is not implemented either, so the roster is trusted
exactly as far as the config file beside it is: it must come from local disk,
never from an untrusted channel. And a captured credential can be replayed
until it expires, five minutes after it was minted.

There is no release yet, and nothing here is ready to run.

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
