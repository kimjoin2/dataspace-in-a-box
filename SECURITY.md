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
  is a deliberate operator act.
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
- **Constraints this connector refuses to evaluate.** Policy evaluation is
  deliberately limited to two shapes (`DECISIONS.md` §14). A constraint that
  is not enforced is rejected rather than ignored, which is the intended
  behavior, not a bypass.

## Known unfixed issues are published, on purpose

`docs/follow-ups.md` and `DECISIONS.md` describe unfixed security gaps in
enough detail to act on, including the sharpest one currently open — the
`initiate` hooks accept an unvalidated `providerId` and make it the audience
of a credential this connector signs. `docs/goal-gap-analysis.md` records how
that composes with the absence of replay defense (`DECISIONS.md` §28) and of
rate limiting.

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
initiate-hook gap should correct this section too.

So: reporting something already described in those files tells the project
nothing it does not know. Reporting something that is **not** in them, or
showing that a documented gap is worse than it is written to be, is
valuable — the second kind is how the composition above was found.

## Supported versions

None yet. There are no releases and no tags, so the only thing that can be
fixed is `main`. Once releases exist, this section states which ones receive
fixes; until then, treat every published gap as present in whatever you
build.
