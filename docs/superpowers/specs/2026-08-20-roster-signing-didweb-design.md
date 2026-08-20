# Design: roster signing and did:web resolution

`DECISIONS.md` §9 decided both pieces already — `did:web` identities plus an
operator-signed static roster — and built neither. The connector-auth
milestone's spec named exactly this gap and narrowed it on purpose: "a
roster fetched from anywhere other than local disk is not safe under this
milestone, and adding the signature is the prerequisite for distributing
one." Today `roster.json` is trusted exactly as far as the disk it sits on,
and `participant_id` is an opaque string nothing resolves. This milestone
builds the two pieces §9 named and the auth milestone deferred.

## Scope

In: a signature over the roster, verified at load, with an unusable result
treated as a startup failure — the existing rule for every other roster
defect. A `did:web` resolver, real enough to fetch a DID document over HTTP
and extract a usable Ed25519 key from it, exposed as an operator tool.

Out: putting resolution in the live authentication path. The auth spec's
reasoning stands and this milestone does not reopen it: the roster already
maps identifier to key, so resolving on every request would add a network
dependency to authentication and change nothing about who ends up trusted.
Resolution is for building and checking a roster entry, not for answering
"is this request authenticated". Out too: revocation faster than roster
redistribution (§9 already accepted this) and DCP/VC (§10, v2).

## Roster signing

**Who signs, and what the connector checks it against.** Not any
participant's key — the roster is the registry itself, not an entry in it,
so signing it with a participant's own key would let that participant vouch
for their own trustworthiness. A new, separate value: `roster_signer` in
`config.yaml`, a bare Ed25519 public key (base64url, like every other public
key value this project already handles inline), required whenever
authentication is on. It sits beside `participant_key` and `roster` on the
same disk, under the same "config.yaml's integrity is already assumed"
reasoning the auth spec gave for the roster file itself — this is the config
file learning one more fact an operator already has to get right, not a new
trust boundary.

**What gets signed, without a JSON-canonicalization library.**
`roster.json` gains a `signature` field beside `participants`. The bytes
signed are `json.Marshal` of the parsed `[]rosterEntry` slice — not the raw
file bytes. Go's `encoding/json` marshals struct fields in declaration
order, so this is deterministic for a fixed struct, without a canonical-JSON
spec: the signer computes it once, the verifier recomputes the identical
bytes from what it parsed, and reformatting the file (whitespace, key
order in the source JSON, since `signature` and `participants` are read
into named struct fields either way) does not break the signature. Two
private keys are already generated the identical way in this project
(`dsops keygen`), so signing needs no new key format either.

**`dsops roster sign -roster <path> -key <path>` prints the signature; it
does not rewrite the file.** `dsops`'s own package doc already states the
principle this follows: "a command that rewrote \[the roster\] would put a
tool between the operator and a file they are meant to read." Signing is a
mechanical operation with no judgment in it, unlike deciding who belongs in
the roster, but the output stays consistent with `keygen` and `token`'s
existing shape — print, and the operator pastes it in — rather than carving
an exception into a principle that was stated once for a reason.

**`LoadRoster` gains a required parameter and one more failure mode.**
`LoadRoster(path string, signer ed25519.PublicKey) (Roster, error)`. A
missing `signature` field, one that is not valid base64url, or one that does
not verify against `signer` and the re-marshaled `participants` bytes, is a
load failure — same family as every existing roster defect (empty,
unparseable, a bad key), same consequence: the connector does not start.
`cmd/dsbox/main.go` decodes `cfg.RosterSigner` once and passes it through.

## did:web resolution

**The method, read off the public did:web specification.**
`did:web:<domain>[:<port>][:<path>...]`. No path segments: domain becomes
`https://<domain>/.well-known/did.json`. With path segments: each is
percent-decoded and joined, and the document lives at
`https://<domain>/<path.../>did.json` — no `.well-known`. A port is written
`%3A<port>` immediately after the domain, decoded back to `:<port>` before
the request — the method spec documents this explicitly, for exactly the
"resolve something running on localhost" case this milestone's own testing
needs.

**Key representation: JWK, not multibase.** A did:web document's
`verificationMethod` entries can carry a key as `publicKeyMultibase`
(base58btc, `z`-prefixed, a two-byte Ed25519 multicodec header) or as
`publicKeyJwk` (RFC 7517 JWK, RFC 8037 OKP for the Ed25519 case:
`{"kty":"OKP","crv":"Ed25519","x":"<base64url>"}`). This project's dependency
rule is the standard library by default; multibase decoding means a base58
decoder this project would have to write for a format that exists only to
save a few bytes over JWK. `x` is already base64url, already the encoding
`encoding/base64` handles everywhere else in this codebase. This milestone
resolves `publicKeyJwk` only, and treats a document with no matching entry —
including one that carries only `publicKeyMultibase` — as a resolution
failure with that reason, not a silent skip.

**Split into a pure step and a network step, so the pure one needs no
server.** `didWebURL(id string) (string, error)` is string transformation
only and is table-tested directly. `resolveAt(ctx, url, client)
(ed25519.PublicKey, error)` fetches and parses, and is tested against
`httptest.NewServer`, the same tool `negotiation_consumer_handler_test.go`
already uses for outbound calls. `ResolveDIDWeb(id string) (ed25519.PublicKey,
error)` composes the two with a real `http.Client` carrying a short timeout —
this is an operator running a one-shot command, not a request in a hot path,
so a slow resolution should time out and say so rather than hang a script.

**`dsops resolve <did:web:id>` prints the resolved key.** Same shape as
`keygen`'s output — a bare base64url Ed25519 public key — so an operator
builds a roster entry for a counterparty's key exactly the same way whether
it came from `keygen` (their own) or `resolve` (someone else's), and pastes
either into `roster.json` by hand. `dsops` still does not manage the roster
file.

## `dev_mode` extends to permit `http://` resolution, mirroring §13's own
carve-out for `public_url`

Resolution is HTTPS by the method spec, and that stays the default. But this
milestone's own testing, and `make demo`, need to resolve something running
on a local, unencrypted HTTP server — exactly the situation `dev_mode`
already exists for. `dev_mode: true` permits `didWebURL` to be built as
`http://` instead of `https://`, the same relaxation §13 already grants
`public_url`, and for the same stated reason: "local demos and the TCK
harness run without a proxy." Production resolution stays HTTPS-only; this
adds one more thing `dev_mode` says out loud that it is relaxing, not a new
kind of relaxation.

`make demo` uses this to resolve for real: each connector serves its own
`did.json` (a static file, no new endpoint logic — `net/http.FileServer` in
the demo's own compose setup, not `dsbox` itself, since only the
demo needs to publish one and `dsbox` has no participants of its own to
publish a document for), and `run.sh` builds the roster by resolving the
counterparty's `did:web:` id with `dsops resolve` rather than by copying
`keygen`'s output across the shell script by hand — the same key material,
obtained the way an operator federating with a stranger actually would.

## What this still does not close

**Not portable yet, in the sense that matters most.** The signature protects
`roster.json` in transit once someone has a copy; it does not answer how a
new participant first reaches an operator's config, or how the *signer's*
own public key — `roster_signer` — is itself distributed and trusted. That
is the same bootstrap problem one level up, and no signature scheme resolves
it by itself; it is a governance question DECISIONS.md §9 left to "diffed in
git" and this milestone does not reopen.

**No key rotation path.** A `did:web` document that changes its key does not
propagate anywhere on its own — the roster is still a static, manually
refreshed cache of whatever `resolve` returned at the time an operator ran
it.

## Testing

Unit tests: roster signing and verification (a valid signature loads; a
missing one, a corrupted one, and one that verifies against the wrong bytes
each fail); `didWebURL` against the method spec's own forms (bare domain,
with a port, with path segments, `dev_mode` on and off); `resolveAt` against
an `httptest.Server` returning a well-formed document, a document with no
matching `verificationMethod`, a document with only `publicKeyMultibase`,
and malformed JSON. `make tck` and `make demo` are the regression gate and
the end-to-end evidence respectively — neither the TCK nor a JWT the wire
already carries changes shape, so no suite this connector is already gated
on is expected to move.
