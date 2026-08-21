# Design: connector-to-connector authentication

Every DSP endpoint this connector serves is anonymous today. `jwt` and
`roster` match nothing in the source. Anyone who can reach the listener can
browse the catalog, negotiate a contract, and start a transfer. This
milestone closes that, and it goes first because
`docs/milestone-sequence.md` argues the data plane must not ship before it:
Phase B makes `STARTED` the state that authorizes a pull, and a counterparty
that reaches `STARTED` anonymously is authorizing itself.

`DECISIONS.md` §9 and §10 already decided the shape — `did:web` identities
with Ed25519 keys, a static operator-signed roster, self-signed JWTs with a
five-minute expiry verified against roster public keys. This spec implements
that decision rather than reopening it, and records exactly which parts of §9
it defers and why.

## Scope

In: an Ed25519 signing key per connector; a roster file mapping participant
identifiers to public keys; a minted `Authorization: Bearer <jwt>` on every
outbound DSP call; verification of that header on every inbound DSP request;
a `dsops` command that generates a key and mints a token, because without it
neither the harness nor the demo can be run by anyone.

Out, deliberately, each recorded below with its reasoning: `did:web`
resolution, the operator's signature over the roster, credential-based access
policy, and DCP/VC — the last already deferred to v2 by §10.

## What the TCK forces, and what it permits

The harness is the regression gate for this milestone, so its constraints are
requirements.

`DspSystemLauncher` reads `dataspacetck.dsp.connector.http.headers.authorization`
and registers it as an interceptor on every request the TCK makes:

```sh
javap -p -c -classpath tckx org.eclipse.dataspacetck.dsp.system.DspSystemLauncher |
  grep -E 'headers.authorization|registerAuthorizationInterceptor'
```

So the suite can present a credential, and all 65 tests must stay green with
authentication switched on. Two consequences follow.

**The credential is one static string for the whole run.** It cannot be
refreshed mid-suite. Measured against §10's five-minute expiry, that is
comfortable: the suite takes **54 seconds** end to end. But the token must be
minted *after* the connector is up and the agreements are seeded, not before
`docker compose` — a cold image build ahead of the suite would otherwise spend
the expiry before the first request. `run.sh` is already at that point when it
seeds.

**The TCK is a participant.** To present a valid token it needs an identity
in the roster and a key to sign with. The harness therefore generates a
keypair for the TCK's participant id, writes it into the roster alongside the
connector's own, and mints the token with it. That is not a special case in
the connector — the connector only ever verifies — it is the harness playing
the part of a real counterparty, which is what it already does for every
other protocol concern.

**One string covers every endpoint the TCK calls**, including
`/negotiations/initiate` and `/transfers/initiate`. Those are operator hooks
rather than counterparty protocol, so a management credential would fit their
meaning better — but the TCK has exactly one Authorization value and uses it
for everything. Ruling: the initiate endpoints accept the same connector
credential. They are on the public listener and currently unauthenticated,
which lets any anonymous caller make this connector open outbound connections
to an address of their choosing; requiring a roster identity is strictly
better than that, and splitting the credential kinds can wait for a real
management-API milestone.

## The credential

A JWT, signed EdDSA over Ed25519, with four claims:

| Claim | Value |
|---|---|
| `iss` | the sender's participant identifier |
| `aud` | the recipient's participant identifier |
| `iat` | issue time |
| `exp` | `iat` + five minutes (§10) |

No `sub`: issuer and subject are the same party here, and a claim that always
duplicates another is one more thing to keep in step. No `jti`: it would only
earn its place with replay detection, which needs storage and a cleanup path,
and is not in this milestone. Replay within the five-minute window is
therefore possible and is named as an accepted trade-off below.

### Written by hand, not by a library

`CLAUDE.md`'s rule is that the default answer to a dependency is the standard
library, and `crypto/ed25519` plus `encoding/base64` plus `encoding/json` is
the whole of what a JWT needs. This follows the precedent §22.5 set for
JSON-LD: a fixed, small format validated by direct checks rather than a
general processor.

Hand-written verification is also where the sharp edges live, so the
implementation takes the two rules that matter and tests them as behavior:

- **The algorithm is not negotiable.** The header's `alg` is compared against
  `EdDSA` and anything else is rejected outright. The header never selects a
  key or an algorithm — that is the alg-confusion family of bugs, and the
  defense is to not read the header for that purpose at all.
- **Nothing is trusted before the signature verifies.** Claims are parsed only
  after the signature check passes against the key the roster gives for the
  issuer.

## The roster

A JSON file, path in config:

```json
{
  "participants": [
    {"id": "urn:participant:alice", "public_key": "<base64url Ed25519 public key>"}
  ]
}
```

Loaded once at startup and held in memory. A roster that is missing,
unparseable, or empty is a startup failure, not a warning: a connector that
starts with no roster can verify nobody, and the failure mode of "started
fine, refuses everyone" is harder to diagnose than refusing to start.

No base URLs in the roster. Verification needs a key and nothing else, and
the addresses this connector talks to already arrive in the messages that
name them.

### What §9 says that this does not do

**`did:web` resolution is deferred.** §9 makes participants `did:web`
identifiers; resolving one means fetching a DID document over HTTPS and
reading a verification method out of it. The roster already maps identifier to
key, so resolution would add a network dependency to the authentication path
and change nothing about who is trusted. Identifiers stay opaque strings, and
`config.ParticipantID`'s own comment already anticipates that only the value
changes when `did:web` arrives.

**The operator's signature over the roster is deferred.** §9 makes that
signature the trust anchor, and it is what lets a roster be distributed over
an untrusted channel. This milestone reads the roster from a local file whose
integrity is already assumed — the same assumption `config.yaml` carries, on
the same disk, in the same deployment. That is a genuine narrowing of §9 and
it is written down rather than quietly dropped: **a roster fetched from
anywhere other than local disk is not safe under this milestone**, and adding
the signature is the prerequisite for distributing one.

## Enforcement

A middleware on the DSP mux, wrapping every route except
`GET /.well-known/dspace-version`. The version endpoint stays open because it
is how a counterparty discovers what to speak before it has any context, and
it discloses nothing but a protocol version.

Failure is `401` with `WWW-Authenticate: Bearer`, mirroring
`internal/mgmt/router.go`'s existing middleware — RFC 9110 §15.5.2 makes the
challenge a MUST, and this listener now has exactly one protection space too.

`401`, not `400`, and this is the one place that does not follow the
protocol's "structural rejections are 400" rule. That rule exists because the
TCK's assertion helper throws on `404`; it says nothing about `401`, and a
missing credential is precisely what `401` means. The TCK will never see one
if the harness is configured correctly, which is itself the point: a `401` in
a TCK run means the harness lost its credential, and that should look
different from a protocol error.

The rejection body says a credential was missing or invalid and nothing more.
It does not distinguish "no header", "expired", "unknown issuer", or "bad
signature" to the caller — those distinctions go to the log, where the
operator can see them and an anonymous prober cannot.

## Outbound

Every call in `negotiation_client.go`, `transfer_client.go`, and
`callback.go`'s `pushCallback` mints a fresh token. Minting is cheap — one
Ed25519 signature — so there is no cache and no refresh logic to get wrong.

The `aud` is the counterparty's participant identifier. This connector knows
it for a negotiation it initiated (the initiate call supplies `providerId`)
and for a transfer it initiated (same). For a callback pushed to a
counterparty that opened the exchange, it is **not currently stored** — the
negotiation and transfer tables keep a callback address, not a participant
id. Ruling: this milestone adds the counterparty's participant id to both
provider-role tables, populated from the authenticated request that created
the row. That is the honest source, since the row is created by an
authenticated request from exactly that party, and it avoids inventing a
lookup from callback address to identity.

## Configuration

Three new keys:

```yaml
participant_key: /etc/dsbox/participant.key   # Ed25519 private key, PEM
roster: /etc/dsbox/roster.json
require_auth: true                            # default true
```

`require_auth` exists for one reason and it is not convenience: the migration
from a running unauthenticated connector to an authenticated one is otherwise
a flag day for every counterparty at once. It defaults to `true`, and setting
it to `false` logs a warning on every startup naming the endpoints left open.
Turning it off is permitted **only** when `dev_mode: true` — an operator who
has not told the connector it is a development instance may not also tell it
to accept anyone, and `config.validate` rejects the combination rather than
letting it load.

## Testing

Unit tests for the token: a round trip; a token signed by a key not in the
roster is rejected; an expired token is rejected; a token whose `aud` names a
different participant is rejected; a token whose header says `alg: none` or
`alg: HS256` is rejected without ever consulting the key; a token whose
payload is edited after signing is rejected.

Middleware tests: every DSP route refuses an absent, malformed, and expired
credential with `401` and a challenge header, and admits a valid one. The
version endpoint admits an anonymous request.

The alg-confusion case gets a mutation check: make the verifier read the
algorithm from the header instead of pinning it, and confirm the `HS256` test
fails. A test that passes either way is not testing that property.

End to end, the TCK is the evidence that authentication does not break the
protocol: 64 of 65 with the harness presenting a minted token, unchanged from
today. It is evidence about the *inbound* half only — the TCK's mock endpoints
accept whatever this connector sends without inspecting the header, so a bug
in minting, in the `aud`, or in the signing key would leave the suite green.
Outbound correctness rests entirely on the unit tests above, which is the
reason they check the claims rather than only that a token was attached. A run where the token is deliberately omitted must fail loudly rather
than pass — worth checking once by hand, because a middleware accidentally
wired to nothing would otherwise look identical to success.

## Accepted trade-offs

**Replay inside the five-minute window.** No `jti`, no nonce, no seen-token
store. An attacker who captures a token can reuse it until it expires.
"Closing this needs storage and expiry sweeping" was this section's original
guess at the fix; DECISIONS.md section 28 (2026-08) found it does not work —
the official TCK itself presents one token for a whole suite run, so a
single-use check would reject conformant behavior, not just an attacker. The
window stays bounded only by §10's own choice.

**No revocation faster than roster redistribution.** §9 already accepted this;
removing a participant means editing and redistributing the file.

**A local roster only.** Stated above under §9's deferrals, and the reason it
must not be forgotten: the signature that would make a roster portable is not
here yet.

## Done criteria

- Every DSP route except the version endpoint refuses an unauthenticated
  request with `401` and a challenge.
- Every outbound DSP call carries a freshly minted token.
- The TCK reports 64 of 65 with the harness authenticating, and 0 results
  outside the gate.
- A run with the harness's credential removed fails, and visibly.
- `dsops` can generate a keypair and mint a token, and the harness uses it
  rather than a fixture checked into the repository.
