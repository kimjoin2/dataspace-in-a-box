# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting on this repository:
**Security → Report a vulnerability**. That channel is enabled and is the
only one this project offers, so please do not open a public issue or a pull
request for a security problem, except as the escalation below describes.

There is no service-level commitment behind this file. This is a small
project with one maintainer; what it promises is that reports arrive
privately and get answered, not that they are answered within a fixed
window. If a report gets no reply at all, opening a public issue that says a
private report is waiting — with no detail in it — is a fair escalation.

Useful in a report: what an attacker gains, the smallest request sequence
that demonstrates it, and the commit you tested. A working reproduction
against `make demo` is worth more than a description, because that harness is
where a fix can be shown to work.

## What is in scope

The connector and its tooling: `cmd/`, `internal/`, the demo and TCK
harnesses, and the configuration surface they read.

Explicitly out of scope, because each is a recorded decision rather than an
oversight:

- **No TLS.** The connector speaks plain HTTP behind a reverse proxy by
  design (`DECISIONS.md` §13). Reports that it does not terminate TLS are not
  findings; reports that it leaks something a proxy cannot fix are.
- **The management listener bound to a public address.** Splitting the ports
  and binding the management one to localhost is `DECISIONS.md` §12; moving it
  is a deliberate operator act. Note what that act now exposes: `DECISIONS.md`
  §35 put the initiate hooks on this listener, so a publicly bound management
  port is where the credential-minting endpoints are reachable, behind a
  shared secret rather than a participant credential. That is the trade the
  move made — the primitive described below stops having an untrusted caller
  only for as long as this listener has none.
- **`require_auth: false`, while it is set.** This turns
  connector-to-connector authentication off. It exists for the migration from
  anonymous to authenticated and is permitted only alongside `dev_mode: true`,
  so anything reachable *while* it is set is expected. What is **not** out of
  scope is what survives turning it back on: `DECISIONS.md` §32 records that
  every agreement concluded while the flag was false keeps an empty
  counterparty forever — `issuerFrom` returned nothing, so no later migration
  can reconstruct it — and stays open to any roster participant that knows its
  id. That residual is a known gap, not an expected consequence, and a report
  finding it worse than §32 describes is in scope.

  **What the flag reaches got smaller**, and this bullet says so rather than
  reading broader than it is. The hooks that ask this connector to start an
  exchange as consumer are on the management listener behind `mgmt_token`
  (`DECISIONS.md` §35.1), which `require_auth` does not touch — so turning
  authentication off no longer opens them to anyone.

  **The roster's expiry is inert while the flag is false**, and that is worth
  saying explicitly because §36 otherwise reads as a bound that always
  applies. With authentication off there is no roster to load, so no revision
  is recorded, no expiry is enforced, and `/health` never reports one. Every
  refusal §36 adds is absent rather than passing, which is the same convention
  the router already uses for the roster check itself. A deployment that
  leaves this flag false gets none of §36's guarantee, and turning it back on
  is what starts the clock.

  **What it still switches off is every comparison on the DSP listener**, and
  §35 adds a residual of its own there. `refuseIfNotParty` permits while the
  flag is false, and the roster check on an initiate call is *absent* rather
  than false, so a consumer-role row written during that window carries a
  counterparty this connector never verified. It is not empty: both hooks
  reject a missing `providerId` outright, so the row carries whatever string
  the operator typed. Turning the flag back on therefore **adopts** that
  string as the authorization anchor rather than refusing the exchange.
  `refuseIfNotParty` does nothing but compare an inbound issuer against what
  the row stores, so a typed name that matches what the peer presents keeps
  working — the ordinary case — and one that does not fails closed. The risk
  runs in the adopting direction: an id nothing ever checked becomes the thing
  later messages are checked against, and nothing marks it as unverified. A
  report showing that worse than this describes is in scope.
- **Constraints this connector refuses to evaluate.** Policy evaluation is
  deliberately limited to two shapes (`DECISIONS.md` §14). A constraint that
  is not enforced is rejected rather than ignored, which is the intended
  behavior, not a bypass.
- **An expired connector saying so to anyone who asks.** `DECISIONS.md` §36
  makes the roster expire, and a connector past that instant reports it
  wherever it can be asked: `409` on the DSP routes behind the credential
  check, `409` from the initiate hooks (behind `mgmt_token`, so to the
  operator only), and `503` with `{"status":"roster expired"}` on `/health`,
  which is unauthenticated and reachable by anyone who can reach the
  management listener. The version document is open to that same caller and
  is unchanged by the expiry: it discloses a protocol version and nothing
  about the roster. Because every connector in a dataspace shares one
  `expires_at`, that is a fact about the dataspace's governance and not only
  about this connector, and §36.7 accepts it deliberately: the alternative is
  a refusal that misdescribes itself, and an expired roster is not a secret
  an attacker can act on — it names no participant and it opens nothing. A
  report showing that it discloses more than this describes, or that the
  refusal can be induced rather than merely observed, is in scope.

## Known unfixed issues are published, on purpose

`docs/follow-ups.md` and `DECISIONS.md` describe unfixed security gaps in
enough detail to act on. **The sharpest one currently open is the forged
consumer-role agreement row** `docs/follow-ups.md` records. The counterparty
of a negotiation this connector's operator started chooses the agreement's
`@id` verbatim and `handleAgreement` writes it down; `CreateAgreement` refuses
duplicates and `DECISIONS.md` §25.3 guarantees no delete path, so that
participant can permanently squat an id its rightful owner meant to import —
and `GET /agreements`, the only audit view an operator has, cannot tell the
row from a real one.

It is sharpest for a narrow reason worth stating, because it is not the most
alarming thing this file has ever named. It is the only item left that an
outsider can trigger alone, with no mistake by the operator, using a message
that is exactly what an honest counterparty sends. The alternatives are not
that. `DECISIONS.md` §35.2 leaves a real gap — being in the roster is not the
same as being the participant at `connectorAddress`, so an operator who points
an initiate call at the wrong connector still hands a credential this
connector signed to whoever is there — but the smallest sequence that
demonstrates it begins with the operator being wrong rather than with a
request anyone makes. And rate limiting is absent on the public listener,
which costs this connector work rather than handing anyone anything that
lasts; `docs/goal-gap-analysis.md` notes it is filed nowhere else.

**What this section used to name is closed**, and saying so is part of the
practice below. The `initiate` hooks accepting an unvalidated `providerId` and
making it the audience of a credential this connector signs is fixed by
`DECISIONS.md` §35 — the hooks moved to the management listener, so the
composition `docs/goal-gap-analysis.md` built from that, the absence of replay
defense (§28), and the absence of rate limiting no longer has an untrusted
caller to start it. The item above ends in a database row, where that one
ended in a signed credential handed to whoever asked for it.

This is a deliberate trade-off and it deserves stating plainly rather than
being left to be inferred:

- **Why publish.** There is no release, no tag, and no deployment this
  project is aware of beyond its own harnesses. That is weaker than "no
  users": the pitch is clone-and-run in ten minutes and `dsp_addr` defaults to
  `0.0.0.0:8080`, so a reader may already be running `main` on a reachable
  machine without this project knowing. What tips it is the other side —
  anyone evaluating this connector needs to know what it does not yet defend
  against *before* they deploy it, and that argument holds only while the
  population is small enough that reaching evaluators matters more than not
  reaching attackers.
- **What changes at the first release.** Cutting a tag creates users and
  starts a licence clock (`DECISIONS.md` §16), and this practice stops making
  sense the moment either exists. From the first release onward, an unfixed
  issue of this severity should be embargoed until a fix ships, not
  documented in the open.

`docs/follow-ups.md` is the maintained list — it carries the rule "delete an
entry when it is fixed" — so check it first. `docs/goal-gap-analysis.md` is a
dated measurement and says of itself that it goes stale. Whoever closes the
forged-row gap should correct this section too, the way §35 corrected it: name
what is sharpest *after* the fix, and do not leave this section describing a
gap that is shut.

So: reporting something already described in those files tells the project
nothing it does not know. Reporting something that is **not** in them, or
showing that a documented gap is worse than it is written to be, is
valuable — the second kind is how the composition above was found.

## Supported versions

None yet. There are no releases and no tags, so the only thing that can be
fixed is `main`. Once releases exist, this section states which ones receive
fixes; until then, treat every published gap as present in whatever you
build.
