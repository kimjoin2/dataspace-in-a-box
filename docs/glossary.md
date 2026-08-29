# Glossary

You arrive here knowing the Dataspace Protocol. This file is for the other
vocabulary — the ordinary English words this repository uses with a narrower
meaning of its own.

Those are the dangerous ones. A coined term you do not recognise sends you
looking; a plain word you think you understand does not. "The gate" reads as
ordinary English and names a specific program's verdict.

`DECISIONS.md` §0 calls documentation quality part of the gap this project
treats as the product. A reader who cannot read the documents is inside that
gap, so this file is on that mission rather than beside it.

**Every entry names where its meaning is fixed**, so you can check it rather
than trust it. Where a meaning is *not* fixed anywhere, the entry says so —
that is worth knowing too, and it is an invitation to fix it.

---

## How the work is built and judged

**the harness** — a rig that stands a connector up and runs something
end-to-end against it: `test/tck/run.sh` with its compose file, and
`demo/run.sh` with its own. Neither is a Go test fixture; no `harness`
identifier exists in the Go source.

*Fixed by:* `CLAUDE.md`'s command list against `Makefile` — `make tck` runs the
harness, then the gate.

*Read with care.* The word also gets used for the upstream TCK program itself,
for the pair of harnesses together, and once for the connector container. When
it matters, say which.

**the gate** — the thing that *judges* a run, as against the harness that
*runs* it. Primarily `cmd/tckgate`, and by extension the per-suite expected
counts it holds.

*Fixed by:* `cmd/tckgate/main.go`'s opening line — "Command tckgate decides
whether a TCK run passes the compliance gate" — and the boundary stated in
`test/tck/run.sh`: "judging the output is the gate's job, not this script's."

*Read with care.* "Gate" is also used loosely for the set of commands required
before a merge, and for runtime authorization checks inside the connector. Both
are ordinary English and neither is `cmd/tckgate`.

**the suite** — one of the TCK's named test groups: `MET`, `CAT`, `CN`, `CN_C`,
`TP`, `TP_C`. A suite is what the TCK runs; the gate is what decides whether
its results are acceptable.

**the milestone** — a unit of work large enough to get its own design spec and
implementation plan under `docs/superpowers/`, and a numbered `DECISIONS.md`
section once it lands. *Fixed by:* `docs/milestone-sequence.md`.

## Words used in security arguments

**the primitive** — a capability an attacker can use, as against a mitigation
of one. The distinction is the repository's central security move: removing a
primitive beats narrowing it.

*Fixed by:* `DECISIONS.md` §35 — "Putting them there is not a mitigation of the
impersonation primitive. It is its removal. The primitive requires an untrusted
caller."

**the exit** — the point at which an attack yields something durable: bytes
that leave, or a credential that can be replayed. Closing an exit completely is
distinguished from narrowing it. *Fixed by:* `DECISIONS.md` §31 ("closes the
forged path's byte exit completely") and `SECURITY.md`.

**the composition** — separately-filed gaps that combine into one exploit no
single entry describes. *Fixed by:* `docs/goal-gap-analysis.md`'s section of
that name, which exists because this repository's per-milestone honesty does
not compose by itself.

**derive** — to take a value from a trusted source by construction, rather than
accept a caller's value and check it. After §38 the initiate hooks derive the
address they dial from the operator-signed roster.

*Fixed by:* `DECISIONS.md` §38.4, "The roster's address is used, not compared"
— "the approved string and the dialed string are the same string by
construction."

*Read with care.* In §24 and §29 the same word names a *rejected* option — a
rule "derived from the content of what it receives". The polarity is opposite;
the section decides which is meant.

**fail-closed** — refusing *on* unauthenticated input. Explicitly distinguished
from acting *on* unauthenticated claims, which this connector does not do.

*Fixed by:* `internal/auth/roster.go`'s `LoadRoster` comment — "Rejecting on
unauthenticated input is fail-closed, which is a different thing from acting on
unauthenticated claims" — with the other half of the line in
`internal/auth/token.go`'s `Verify`.

**provenance** — how a stored identity came to be known: a verified issuer, or
an operator's assertion. Not the same as trustworthiness; a provenance can be
weak and still be recorded.

*Not fixed anywhere.* The nearest statement is in
`docs/milestone-sequence.md` — "a provider-role counterparty is a verified
issuer while a consumer-role one stays an operator's assertion."

**absence is not a check that fails** — a disabled check is a nil predicate: a
third state, neither passing nor refusing. Its two hazards are named in
opposite words at different call sites, and both are the same convention:
absent is not silently *false* (which would refuse everyone), and absent is not
silently *true* (which would let everyone through).

*Fixed by:* `internal/dsp/auth_middleware.go`'s `rosterGuard.usable`, a single
expression, and `DECISIONS.md` §38.3.

**welded** — a constant and the one test fixture that bounds it, where widening
either requires moving the other. *Fixed by:* `internal/auth/token.go`'s
comment on the leeway constant.

**the ratchet** — this connector's stored memory of the highest roster version
it has run, which is what makes a rollback detectable. *Fixed by:*
`DECISIONS.md` §36.

**the boundary** — §25.3's rule bounding the management API. It is about
*writing*: a route that writes nothing has not moved it. *Fixed by:*
`DECISIONS.md` §25.3, and enforced in `internal/dsp/catalog_client.go`'s
handler comment.

*Read with care.* "Boundary" is also used for unrelated things — a trust
boundary, a process boundary, the edge of a validity window. Only the
management-API sense is a term of art.

## Words for what was deliberately left undone

**the residual** — the part of a hole a milestone did not close, written down
rather than left to be found.

*Not fixed anywhere.* "The residual" means whichever one its own paragraph is
about; more than one is open at a time. `SECURITY.md` and `docs/follow-ups.md`
each carry their own.

**out of band** — a value the protocols do not carry, so a human has to move
it. The roster signer's public key is the standing example; `format` is
another.

*Fixed by:* `config.example.yaml`'s roster section, which calls the signer key
distribution "still an out-of-band problem", and cited as the definition by
`docs/goal-gap-analysis.md`.

**served only** — this connector implements the serving half of a DSP protocol
and never the requesting half. *Fixed by:* the paragraph under `README.md`'s
protocol table, which is the table's only legend.

**Trade-off accepted** — the heading that closes a decision in `DECISIONS.md`.
It marks the place a cost is admitted rather than argued away, and its presence
is what makes an omission a decision instead of a gap.

## Words for how these documents are written

The rules these name live in [`../CONTRIBUTING.md`](../CONTRIBUTING.md).

**load-bearing** — something that looks decorative but whose removal breaks a
measurable thing. A struct tag, the order of two statements, a field's position
in a literal.

*Not fixed anywhere*, which is awkward for the most-used term in the
repository. What exists is a contrastive frame — "load-bearing rather than
cosmetic", "load-bearing rather than tidy". Read it as a claim that something
was measured, and look for the measurement.

**dated artifact** — a document that records what was true on its date:
`docs/goal-gap-analysis.md` and everything under `docs/superpowers/`. Its
complement is the *living document*, `DECISIONS.md`, corrected in place with
the amendment marked.

**annotate** — to append a dated bracket beside a statement that has stopped
being true, leaving the original sentence standing. What you do to a dated
artifact instead of editing it.

**by kind, not by census** — write what class of thing exists, never how many.
A count is false the day someone adds the next one.

---

## Protocol terms that are narrower here

These are the specification's words. This connector implements less than the
specification allows, and the narrowing is deliberate in each case.

**participant** — in the specification, a party in a dataspace. Here, an entry
in the operator-signed roster: `internal/auth/roster.go` defines a `Roster` as
"the set of participants whose signatures this connector accepts". A party this
connector has never been told about is not a participant to it.

**dataset** — here, an entry in the configuration file, of which only the
identifier and a few fields are yours to set. The connector synthesizes the
offer, the distribution and the data service around it. *Fixed by:*
`internal/config/config.go` and `internal/dsp/catalog.go`.

**constraint** — the specification allows ODRL constraints generally. This
connector evaluates exactly two shapes, unrestricted use and a validity period,
and any other constraint parses and is then rejected. Never accepted while
unenforced. *Fixed by:* `CLAUDE.md`'s policy rule and `DECISIONS.md` §14.
