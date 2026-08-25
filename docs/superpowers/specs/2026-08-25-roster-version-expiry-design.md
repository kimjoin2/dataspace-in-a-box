# The roster as a versioned, expiring artifact

Ordered item 3 of `docs/goal-gap-analysis.md`. This milestone gives the
roster a revision number and an expiry, both inside the operator's signature,
and makes the expiry something the connector enforces while it runs rather
than only when it starts.

Three cross-checks ran against two earlier revisions of this design. Both
were rejected, and section 12 records what they measured, because the
defects they found are the reason several decisions below look the way they
do.

## 1. What is broken

Two facts, both in `internal/auth`.

**A superseded roster verifies forever.** `rosterDocument` is
`{participants, signature}` (`internal/auth/roster.go:45-52`) and the
signature covers `canonicalRosterBytes(doc.Participants)` alone
(`roster.go:84`). Nothing in the document says which revision it is or when
it stops being true, so a roster that still lists a removed participant
verifies as well as the one that replaced it, and rolling back to it cannot
be detected.

**The connector never looks again.** `auth.LoadRoster` is called once, at
`cmd/dsbox/main.go:84`, and `Roster`'s own doc comment says so: "Loaded once
at startup and never mutated, so nothing needs a lock and there is no reload
path to get wrong." `DECISIONS.md` section 9 accepted that as the cost of a
static registry and recorded the consequence as "revocation is only as fast
as propagation". The code is weaker than that sentence: propagation finishing
changes nothing for a connector that does not restart.

The second fact is why a version and an expiry checked only at load would buy
nothing at runtime. That was revision 1's defect.

## 2. Scope

**In.** `version` and `expires_at` inside the signed payload; expiry enforced
at load and on every request, on both listeners and on everything this
connector sends; a monotonic version remembered in the store.

**Out, each for a reason that gets written down.**

*Clock leeway, `nbf`, and a maximum token lifetime.* Their content is already
decided and belongs in its own milestone: add leeway to the `exp` comparison,
do not add `nbf`, and bound `exp - iat`. The reason for not adding `nbf` is
that it would newly refuse pairs that work today — without it, a token from
an issuer whose clock runs ahead is accepted, so `nbf` tightens rather than
loosens, and what it was reaching for is the lifetime bound. `Verify` does
not bound `exp` at all today; `credentialTTL` lives at the minting site
(`internal/dsp/router.go:190`) and the harness already mints past it with
`-ttl 30m`, so section 10's five minutes is a convention rather than
something the verifier enforces. The split is because the two halves share no
code and no decision, and because their evidence is opposite: both harnesses
exercise the roster half simply by coming up, and neither can exercise a
clock difference, since every container shares one host clock.

*Binding `connectorAddress` to a roster entry.* Three places in the
repository assign this to the roster milestone —
`docs/goal-gap-analysis.md:255-256`, `DECISIONS.md:2849-2851`, and
`docs/superpowers/specs/2026-08-24-initiate-hook-authorization-design.md:690-693`.
It moves to ordered item 4, discovery, and all three are corrected. The
argument is that this milestone changes what the roster document *is* — it
gains a revision and a lifetime — while an address changes what an *entry*
means, and an address is only checkable once something resolves and contacts
it, which is what item 4 builds.

## 3. What the signature covers

```
{ "participants": [ ... ],
  "version": 3,
  "expires_at": "2027-01-01T00:00:00Z",
  "signature": "..." }
```

`canonicalRosterBytes` serializes participants, version, and expiry together.
The re-marshal argument in `DECISIONS.md` section 27.2 is unchanged: the
signer and `LoadRoster` both call the same function on their own parsed
value, so reformatting the file cannot change what is checked.

**`expires_at` stays a JSON string**, parsed only for comparison.
`canonicalRosterBytes` discards `json.Marshal`'s error, and its doc comment
justifies that by "Every field of `rosterEntry` is a plain string, so
`json.Marshal` cannot fail here". A `time.Time` field makes that sentence
false and the discarded error reachable — `time.Time.MarshalJSON` fails for a
year outside its representable range. The string keeps the existing reasoning
true.

**`version` must be at least one.** An absent `version` decodes to zero, so
requiring one turns Go's own zero value into the rejection and needs no
pointer. Version zero is unreachable forever after this, which costs nothing
because no roster carries a version today.

**Both fields are required.** A roster missing either is refused. Note what
this does *not* rest on: it is not that an attacker could strip the field to
evade the version check. Because canonicalization re-marshals the parsed
document, a stripped `version` is inside the signed bytes as zero and the
signature already fails. Requiring the fields is about the operator's own
files, not an attacker's.

**The required-field checks run after the signature verifies**, where
`LoadRoster` already puts its per-participant checks (`roster.go:90-96`,
after `ed25519.Verify` at `:84`). Putting them first would split one class of
check across the signature boundary, and it would make a forged roster report
that its version is missing — sending an operator to edit an attacker's file
instead of discovering it is a forgery. The good message at the right moment
comes from section 8 instead: `dsops roster sign` refuses to sign an
incomplete roster, so a signed roster missing a field is not something the
tooling can produce. Reaching `LoadRoster` with one means the file was
hand-edited or forged, and "signature does not verify" is then the true
answer.

## 4. Expiry, at load and on every request

At load, an expired roster is a startup failure, the same grade as a
signature that does not verify. `LoadRoster`'s doc comment already argues the
general case: a connector that starts with an unusable roster can verify
nobody, and "started fine, refuses everyone" is a much harder symptom to
trace than a refusal to start.

**On every request is the part that gives this milestone a runtime effect.**
The file is still read once. But a connector that has not restarted stops
serving at `expires_at`, so section 9's "only as fast as propagation" gains
an upper bound instead of being false.

The check has to reach three places, and reaching only the first was
revision 2's defect:

- **The DSP listener.** `requireParticipant` refuses. This is the only place
  it is wrapped (`internal/dsp/router.go:169`).
- **The management listener's initiate hooks.** `requireParticipant` never
  runs there — section 35 moved `POST /negotiations/initiate` and
  `POST /transfers/initiate` to a listener guarded by a shared secret
  instead. Measured on a throwaway implementation of revision 2: with a
  roster an hour expired, an initiate call returned 200, authorized its
  `providerId` against the expired roster through `knownParticipant`, and
  dispatched an outbound message signed with this connector's real key. A
  connector that has stopped answering must not still be starting exchanges
  and signing.
- **Everything this connector sends.** `mintOutboundCredential` is the single
  choke point: five call sites, all in package `dsp`, and the data pull is
  one of them (`transfer_consumer_handler.go:550`). Its contract changes from
  returning a header value to reporting whether the message may be sent at
  all, because every call site today reads "if non-empty, attach it;
  otherwise proceed unsigned", and proceeding unsigned under an expired
  roster would send messages the counterparty refuses rather than sending
  none.

  The empty-audience case keeps exactly its present behaviour. The two
  outcomes are distinguished rather than merged: no audience means proceed
  without a credential, as it does today; an expired roster means do not
  send.

**The predicate stays separate from `knownParticipant`.** That predicate
answers whether the roster lists a participant, and this one answers whether
the roster is still good. Folding them would make a test unable to say which
of the two refused, and section 35 introduced `knownParticipant` precisely
because a nil predicate says "this check is absent" rather than "this check
said no". The same nil-means-absent convention applies here: with
authentication off there is no roster, so there is no expiry, and a zero
`auth.Roster` must not read as expired — its zero expiry would otherwise
break `require_auth: false` on a deployment that has no roster at all.

**Parsed once, at load.** The `Roster` holds the parsed instant. Parsing per
request would put a string parse on the hot path and, worse, would let a
malformed expiry produce a silent per-request refusal on a connector whose
boot log said nothing.

## 5. What an expired connector says, and logs

**Not 401.** The counterparty's credential is perfect and the fault is
entirely local; answering "a valid participant credential is required" sends
their operator hunting their own credential across an organizational
boundary, where they cannot read this connector's log. The refusal says the
roster has expired. That is not reconnaissance: it discloses a fact about
this connector's own configuration, not about the caller or about who else is
in the roster.

**`GET /health` reflects it.** Today it answers `{"status":"ok"}`
unconditionally (`internal/mgmt/router.go:32-35`), so a connector that can
serve nothing stays in rotation. It is the one route deliberately outside the
token check, and a readiness probe that cannot see this is not reporting
readiness.

**One warning, not one per request.** `refuse` logs `slog.Warn` per call, and
after expiry that is every inbound request forever.
`cmd/dsbox/main.go:88-92` states the principle and its reason for the
authentication-off warning: "One line at startup, not one per request... a
per-request warning would bury the rest of the log under it." The expiry
refusal follows it — the connector says so once, when it first refuses.

**The boot log carries the roster's version and expiry.** Nothing today
reports which roster a running connector holds, so an operator whose git says
version four has no way to learn that a connector is still on three. This is
the smallest thing that closes it.

## 6. Monotonicity, and making the wiring undeletable

`auth.LoadRoster` stays stateless and the returned `Roster` carries its
version. The store records the highest version it has seen; a lower version
is refused at startup, and an equal or higher one updates the record. Equal
must be accepted, or an ordinary restart with an unchanged roster fails to
boot.

The reason `internal/auth` does not reach the store itself is not a layering
inversion. Section 35 says of its own `mgmt`/`dsp` rule that it was "a
layering choice rather than cycle avoidance", and `auth` and `store` are both
leaves that import nothing else in this repository. The real basis is
`internal/auth/token.go:80-82`, which takes a function rather than a roster
type "so this package never imports one and can be tested without a file".

**The call site is the part that needs guarding, and extraction does not
guard it.** A throwaway implementation of revision 2 measured this: deleting
the call that records the version left `go build`, `go vet`,
`go test -race`, `make tck`, and `make demo` all green. Go does not error on
an unreferenced function and `go vet` does not report one, so moving the
logic into a testable function tests the logic and leaves the call deletable.
`drainPulls` — the precedent revision 2 cited — has the same hole open today.

So the guard is a test that reads `cmd/dsbox/main.go` and asserts the call is
there. That technique is already this repository's answer to exactly this
problem: `internal/dsp/auth_middleware_test.go` and
`internal/mgmt/route_coverage_test.go` both parse `router.go` rather than
keeping a list, for the same reason — a hand-kept list of what should be
wired is the thing that goes stale.

This is the store's first schema change that is not a column addition.
`migrate`'s doc comment currently says "Every migration in this file is a
column addition, which is the only schema change SQLite performs cheaply and
the only one this connector has needed", and `store.go:308-311` says there is
"no migration framework and no version table". Both are edited: the new table
records the highest roster version this connector has loaded and says nothing
about the schema's own revision, and `CREATE TABLE IF NOT EXISTS` needs no
check-and-add helper because it is already idempotent.

## 7. The harness, and what it cannot verify

Four heredocs write a roster, two in each script, because `dsops roster sign`
prints a signature rather than writing the file: `demo/run.sh:38-45` and
`:49-57`, `test/tck/run.sh:58-65` and `:67-75`. The three signed fields must
carry the same values in both copies of a script, so the expiry is computed
once into a variable before the first heredoc — calling `date` inside each
would split them across a second boundary. Formatting need not match, because
canonicalization re-marshals.

`date` is not portable for this: GNU takes `-d`, macOS takes `-v`. A two-way
fallback, written once per script.

**State plainly what the harnesses do not cover.** A throwaway
implementation of this design was mutation-tested, and `make tck` and
`make demo` stayed green under every mutation: deleting the per-request
expiry check, deleting the version record, flipping the monotonic
comparison, dropping the version requirement, removing version and expiry
from the signature, and skipping the signing tool's validation. `go test` is
the only gate that carries this milestone, which is a fact the sequencing
document should record rather than one to discover later.

The version-regression refusal in particular is unreachable from either
harness, for two different reasons: the TCK connector mounts no volume for
`data_dir`, so its database dies with the container, and `demo/run.sh:22`
removes the generated directory at the start of every run even though the
demo consumer does bind-mount `data_dir` (`demo/compose.yaml:49`).

## 8. `dsops roster sign` validates

Today it checks only that participants is non-empty
(`internal/auth/roster.go:127-129`), so after this change it would print a
signature and exit zero for a roster missing the new fields. The operator
sees success and the connector refuses to boot days later, which is the worst
available ordering.

It applies the same required-field rules as `LoadRoster`, minus the expiry
comparison: signing a roster that expires next week is exactly what an
operator does. The CLI surface does not change and it still writes nothing,
so section 27.3's print-don't-write principle is intact — validating is not
managing. The behaviour change is recorded as one.

## 9. What becomes false

This milestone edits a signed artifact's shape, a startup contract, and a
recorded trade-off, so its documentation surface is large. The corrections
below are the load-bearing ones; the implementation plan carries the full
list, and each edit names the code fact it was checked against.

- `internal/auth/roster.go` — `rosterDocument`'s shape, the `Signature`
  field's description of what it covers, `canonicalRosterBytes`'s
  cannot-fail argument, `LoadRoster`'s enumeration of what makes a roster
  unusable, and `Roster`'s "a key here is trusted, and anything else is not"
  (a key here is trusted until the expiry, and not after).
- `DECISIONS.md` section 9's trade-off — "revocation is only as fast as
  propagation" now has an upper bound, with the cost that bound carries.
- `config.example.yaml:75-76` shows the roster JSON inline and is the only
  onboarding document that does; `:82-86` says an unusable roster fails "at
  boot, not by refusing every request later", and refusing every request
  later is now deliberate.
- `internal/store/store.go` — the two statements that every migration is a
  column addition and that there is no version table.
- The three `connectorAddress` assignments named in section 2.
- `docs/goal-gap-analysis.md`'s P3 revocation bullet, which proposed this
  fix, and its ordered item 3, which still bundles the clock work section 2
  splits out.
- `README.md`'s description of the roster and of what the DSP listener
  requires.

## 10. Trade-offs accepted

**The whole fleet stops at one instant.** Every connector shares one
`expires_at`. Recovery needs a signature from `roster_signer`, a key section
27.1 deliberately places outside every participant, plus redistribution and a
restart. There is no grace period: a grace period is a second expiry that the
signature does not cover, so its length would vary per deployment and an
attacker holding an old roster would pick the most generous one. The
mitigation is warning, not grace — the boot log, a warning as expiry
approaches, and `/health`.

**A fresh store is fail-open on version.** With nothing recorded, any version
is accepted. Expiry is what bounds that window, and this is the reason the
two mechanisms ship together rather than separately.

**Monotonicity is this connector's memory, not a dataspace property.** No
version is exchanged between participants, so during a rollout one connector
can be ahead of another and neither can tell.

**A signer rotation that restarts numbering is refused forever.** This
compounds the absent key-rotation path section 27 already admits.

**Startup now depends on the wall clock.** A boot before NTP converges can
refuse an unexpired roster, and `run()` has no retry, so under a restart
policy that becomes a crash loop. This is the clock problem arriving in the
milestone that defers the clock work, and it is stated rather than hidden.

**Everything here is inert under `require_auth: false`.** No roster, no
version recorded, no expiry enforced. That matches the router's existing rule
and belongs in `SECURITY.md`'s paragraph on that flag.

## 11. What can verify this

`go test -race` carries the milestone, as section 7 records. The mutations
that must be killed by a named test. Every row but the last was run against a
throwaway implementation and survived both harnesses; the last is here
because breaking it breaks every ordinary restart, which no harness
performs and which no measurement would therefore have surfaced:

| Mutation | Why a named test must fail |
|---|---|
| Delete the per-request expiry check | Both harnesses use an unexpired roster, so nothing else observes it |
| Delete the call that records the version | Measured to survive all five gates; only a source-parsing test reaches it |
| Flip the monotonic comparison | No harness opens the same store twice |
| Drop the version requirement | An absent version decodes to zero and is otherwise indistinguishable |
| Remove version and expiry from the signed bytes | Both harnesses sign and load the same file, so a narrower signature still round-trips |
| Skip the signing tool's validation | The tool's output is only ever consumed by a correct roster in the harnesses |
| Refuse an equal version | Would break every ordinary restart, which no harness performs |

## 12. What the cross-checks measured

Two revisions were rejected before this one, and the record matters because
each defect explains a decision above.

**Revision 1 had no runtime effect.** It checked version and expiry at load
only. Since the roster is read once and never re-read, a connector that does
not restart would never have noticed either. Section 4 is the answer.

**Revision 2 reached only the DSP listener.** An implementation of it
answered 200 to an initiate call under a roster expired an hour earlier, and
signed an outbound message with the connector's real key. Section 4's second
and third bullets are the answer.

**Revision 2's remedy for an untested call site did not work.** It proposed
extracting the wiring into a testable function; measured, deleting the call
still passed all five gates. Section 6 is the answer.

**Six factual claims in revision 1 were wrong**, including the count of
roster-writing heredocs (four, not three, because signing prints rather than
writes), the claim that `config.example.yaml` does not document the roster
shape (it does, and the example becomes invalid), and the layering argument,
which cited a rule section 35 describes as stylistic. **Revision 2 added two
of its own**: it named two `connectorAddress` assignments where there are
three, and said neither harness mounts `data_dir` when the demo consumer
does.
