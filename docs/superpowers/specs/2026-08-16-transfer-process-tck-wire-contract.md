<!-- Companion to 2026-08-16-transfer-process-tck-requirements.md. That document
surveys what the TCK's transfer suites verify, read from the schemas and the test
inventory. This one records what the wire contract actually is, read out of the
compiled TCK by decompiling the pinned runtime image.

Why it exists: the transfer-process implementation plan marked two values as
unconfirmed and expected to settle them by running the TCK at several minutes per
iteration. They were settled from the artifact instead. Every path, key, and shape
below is a compile-time constant of the pinned build
(eclipsedataspacetck/dsp-tck-runtime@sha256:45cfafa4...), so re-derive before
trusting any of it across a TCK upgrade — the last section says how, and it takes
seconds.

Its claims are labelled bytecode-confirmed, execution-verified, or inferred. Do not
promote an inference by reading it twice. -->

# DSP 2025-1 TCK — Transfer Process, Provider Role: Settled Facts

**Source:** static analysis (`javap`) of the pinned TCK jar, plus targeted execution of the TCK's own `MessageSerializer` / `JsonLdFunctions` / bundled schemas / Awaitility out of that jar. Three of the seven findings were then re-derived independently by a second agent that tried to break them. **No agent ran the full TCK against a live connector.** Every claim below is one of: *bytecode-confirmed*, *execution-verified*, or *inferred* — labelled as such. Do not upgrade an inference by reading it twice.

---

## 1. What is now settled

### 1.1 Config keys for `@ConfigParam` fields — `TP_01_01_AGREEMENTID`

**Instruction.** Put plain uppercase lines in the `-config` properties file:

```
TP_01_01_AGREEMENTID=<an agreement id your connector accepts>
TP_01_01_FORMAT=HTTP-PULL
```

**Rule (bytecode-confirmed, independently re-derived and agreed):**

```
key = testMethod.getName().toUpperCase() + "_" + field.getName().toUpperCase()
```

Nothing else. No camelCase→snake splitting, no class name, no package, no prefix. `agreementId` → `AGREEMENTID` as **one token**, not `AGREEMENT_ID`. Sole derivation site: `InstanceInjector.getKey(Field)`; the StringConcat recipe constant is literally `\u0001_\u0001`. Sole call site: `SystemBootstrapExtension.beforeEach(...)` → the key is scoped to the **individual `@Test` method**; there is no class-level or global fallback key.

**Confidence: highest in this document.** Two agents reached it by different routes; the refuter additionally proved there is *no* competing path — `InstanceInjector` is referenced by exactly one class, `getRequiredTestMethod` by exactly one class, and no literal `AGREEMENTID` / `DATASETID` string exists anywhere in the jar (so no bundled constant can shadow the computed key).

**Consequences you must act on:**

- **You need one line per test method, not one per class.** Provider methods are `tp_01_01..05`, `tp_02_01..05`, `tp_03_01..06` — **16 keys**. Setting only `TP_01_01_AGREEMENTID` leaves the other 15 on random UUIDs.
- **A missing key fails silently.** `@ConfigParam.required()` defaults to `false`; `AbstractTransferProcessProviderTest`'s constructor pre-seeds `agreementId = UUID.randomUUID()` and `format = "HTTP-PULL"`. Injection just returns. **A green test is not evidence the key was read** — you must see your value on the wire.
- **A blank value counts as absent.** `ConfigFunctions.exists()` is `s != null && !s.trim().isEmpty()`, so `TP_01_01_AGREEMENTID=` falls through to env, then to the random-UUID default. (Refuter-only finding; the original agent missed it.)
- **Exact case matters.** Lookup is `getConfigurationParameter(KEY).orElse(propertyOrEnv(KEY, null))`; `System.getProperty` is exact-match. `tp_01_01_agreementId` will not match via the properties path. As an env var the name is identical (`TP_01_01_AGREEMENTID`) since the key contains no dots and is already uppercase.
- The properties file reaches JUnit **only** as System properties: `DspTckSuite` does `Properties.load(Reader)` → `TckRuntime$Builder.properties()` (`putAll`, no key rewriting) → `TckRuntime.execute()` pushes them through `System::setProperty`. The refuter dumped `createLauncherDiscoveryRequest()` to its terminal `build()/areturn` and confirmed there is **no** `configurationParameters(...)` call, and the jar bundles no `junit-platform.properties`. This closes the original agent's open caveat.

**Cross-check that the rule is general, not a coincidence:** `cn_c_01_02` + `datasetId` → `CN_C_01_02_DATASETID`; provider-side CN is `cn_01_01`-style → `CN_01_02_DATASETID`; TP consumer is `tp_c_01_01` → `TP_C_01_01_AGREEMENTID`.

*Evidence pointer:* `javap -p -c org.eclipse.dataspacetck.core.system.InstanceInjector` (offsets 4–26 of `getKey`), BootstrapMethods arg `#319 = Utf8 \u0001_\u0001`.

---

### 1.2 Inbound paths — what the TCK calls on your connector

**Instruction.** Serve exactly these six, all under whatever base you configure:

| Verb | Path | TCK method |
|---|---|---|
| POST | `{base}/transfers/request` | `HttpProviderTransferProcessClient.transferRequest` |
| GET | `{base}/transfers/{providerPid}` | `AbstractHttpTransferProcessClientImpl.getTransferProcess` |
| POST | `{base}/transfers/{providerPid}/start` | `startTransfer` |
| POST | `{base}/transfers/{providerPid}/completion` | `completeTransfer` |
| POST | `{base}/transfers/{providerPid}/suspension` | `suspendTransfer` |
| POST | `{base}/transfers/{providerPid}/termination` | `terminateTransfer` |

**Confidence: highest.** Bytecode-confirmed and independently re-derived. The refuter bounded completeness rather than asserting it: `grep -rla "transfers"` over the whole jar returns exactly six classes, of which only two are outbound clients. **The list is closed — there is no seventh endpoint hidden in a helper.**

- **`{id}` is the providerPid you returned in the ACK to `POST /transfers/request`.** Bytecode-confirmed, independently agreed. The TCK's parameter is named `counterPartyPid` and is filled from `transferProcess.getCorrelationId()`, which `ConsumerTransferProcessManagerImpl.transferRequested()` sets from `dspace:providerPid` parsed out of your ACK. The TCK-side `getId()` is the *consumer* pid and appears only in message bodies, never in a URL. `TransferProcess` has **no** `setCallbackAddress` and exactly one `setCorrelationId`, so neither value can drift mid-run.
- **The `/2025-1` prefix is yours, not the TCK's.** There is no version-prefix constant in the transfer client; `2025-1` appears in the jar only in `Metadata01Test`'s assertion, `TckConnector`'s mock metadata, and JSON-LD context filenames. All six calls share one config value: `dataspacetck.dsp.connector.http.url`. Set it to `https://host/2025-1` and the TCK concatenates raw.
- **Concatenation is raw** (StringConcat recipe `\u0001\u0001`, no separator, `AbstractConfiguration.getPropertyAsString` does no trimming). **Configure the base without a trailing slash** or you get `//transfers/request`.
- `dataspacetck.dsp.connector.http.base.url` is a *different* key, used only by `HttpMetadataClient` for `/.well-known/dspace-version`. Do not put the version prefix there.
- **`dataspacetck.dsp.connector.http.headers.authorization`** installs a global OkHttp `Authorization` interceptor applied to all six calls (refuter-only finding).
- All six are actually exercised — `TransferProcessProvider02Test` / `03Test` call every send plus `thenVerifyProviderState` (the GET) 13 and 16 times respectively. None is dead code you can skip.

---

### 1.3 Outbound push — where your connector POSTs its own messages

**Instruction.**

```
POST {callbackAddress}/transfers/{consumerPid}/start
POST {callbackAddress}/transfers/{consumerPid}/completion
POST {callbackAddress}/transfers/{consumerPid}/suspension
POST {callbackAddress}/transfers/{consumerPid}/termination
```

`{callbackAddress}` is **verbatim** the `dspace:callbackAddress` string from the TransferRequestMessage the TCK sent you (TCK default `http://localhost:8083`, no path, no trailing slash). Append and change nothing.

**Confidence: high, bytecode-confirmed, independently agreed** — but read the next three points, because the bytecode is more specific than the template implies.

1. **The `{id}` segment is not validated at all.** The registered key is the regex `/transfers/[^/]+/start`; `FallibleDspHandler` implements only `apply(Map headers, InputStream body)`, and `ProtocolHandler`'s 3-arg default method explicitly drops the path argument. **The handler cannot see the path.** Correlation is done from the JSON body: `parseId` reads `.../consumerPid` as the lookup key and `.../providerPid` as the correlation id, then `findById(consumerPid)`. **Getting the body's `consumerPid` right is the thing that matters; the path segment is cosmetic.** Use `consumerPid` anyway — it is the TCK's own convention and costs nothing. Note this means the falsifiable version of this claim ("the TCK rejects a wrong pid in the path") is **confirmed FALSE**.
2. **The match is a full regex match against the raw request path.** `DefaultCallbackEndpoint.lookupHandler` does `Pattern.compile(key).matcher(path).matches()` on `HttpExchange.getRequestURI().getPath()`, with only a trailing slash stripped, and the HTTP context is `createContext("/")`. So if `dataspacetck.callback.address` were ever set with a path prefix, the TCK would advertise `http://host/tck`, you would correctly POST to `/tck/transfers/.../start`, and the TCK's own regex would 404. Trailing slash tolerated; query string tolerated (`getPath()` excludes it).
3. **Each expectation is single-shot.** `expectStartMessage` registers the handler and **deregisters it inside the handler on first match**. The suspend→restart flow in `tp_01_04` only works because that test declares a second `expectStartMessage` stage. An extra or retried push gets HTTP `404` with body `No handler registered on this endpoint`.

**Refuter additions the finder missed — these are debugging-relevant:**

- **A schema-invalid body does not produce a clean 4xx.** `MessageSerializer.validateMessage` throws `java.lang.AssertionError`; `FallibleDspHandler`'s exception table catches `java/lang/Exception` only. The Error propagates out of `HttpHandler.handle`, so you see an **aborted exchange / no response**, not an error status. If a push yields "no response at all", suspect your body schema, not your routing.
- **Termination takes a different handler path.** `expectTerminationMessage` uses `registerHandler` (not `registerProtocolHandler`), wrapped in `DefaultCallbackEndpoint$DelegatingProtocolHandler`, which hardcodes `200` and has **no** try/catch and **no** `ErrorType` mapping. Start/completion/suspension go through `FallibleDspHandler` (200 on success; `ErrorType` → 400/401/404/500/409).
- **Dispatch is path-only across a queue of endpoints with `findFirst()` over a HashMap.** Since the pid is ignored, two concurrently-live expectations could cross-deliver. **Do not run TCK transfer test classes in parallel against one connector.**
- `DspSystemLauncher` **is** in this jar (the finder wrongly guessed it was a separate artifact). Its only config keys are `dataspacetck.dsp.{default.wait, thread.pool, local.connector, connector.http.url, connector.http.base.url, connector.http.headers.authorization, connector.negotiation.initiate.url, connector.transfer.initiate.url, connector.agent.id}` — **nothing touches the callback address or path**, and `DefaultCallbackEndpoint` is the only implementation of `CallbackEndpoint` in the jar. This closes the finder's caveat without a TCK run.

**Message body requirements:** `transfer/transfer-start-message-schema.json` requires `@context`, `@type`, `providerPid`, `consumerPid`. `dataAddress` is optional. Namespace `https://w3id.org/dspace/2025/1/`. Send `@type` as the **plain string** `"TransferStartMessage"` — validator lookup is `VALIDATORS.get(jsonObject.getString("@type"))` on the **raw, unexpanded** value, so a prefixed `"dspace:TransferStartMessage"` silently skips validation, and an array-valued `@type` would hit `getString` on a non-`JsonString`.

HTTP method is never checked by the dispatcher. Send POST regardless.

---

### 1.4 Your ACK to `POST /transfers/request`

**Instruction.** Return **201 or 200** with a full JSON-LD TransferProcess body:

```json
{
  "@context": ["https://w3id.org/dspace/2025/1/context.jsonld"],
  "@type": "TransferProcess",
  "providerPid": "urn:uuid:...",
  "consumerPid": "<echo the consumerPid from the request>",
  "state": "REQUESTED"
}
```

**Confidence: bytecode-confirmed for the status-code policy; execution-verified for the body shape** (finding `state-assertions` ran the TCK's own `MessageSerializer` and `JsonLdFunctions` on sample bodies). **Not independently refuted** — single agent per finding, but the two findings that touch it agree with each other in every detail.

- **Any 2xx passes.** `HttpFunctions.postJson(url, body, expectError=false)` — 200 and 201 are equally fine.
- **`204` / empty body fails.** It passes `isSuccessful()` and then dies in `MessageSerializer` with `Cannot deserialize json: …`. Return a body.
- **`@context` MUST be a JSON array of strings** containing `"https://w3id.org/dspace/2025/1/context.jsonld"`. A bare-string `@context` fails — **execution-verified**: `Invalid message: [string found, array expected, string found, array expected]`. *This is the single most likely thing to get wrong.*
- **`@type` MUST be the plain term `"TransferProcess"`.** Execution-verified: `"dspace:TransferProcess"` skips schema validation and then breaks expansion → `AssertionError: Property 'https://w3id.org/dspace/2025/1/state' was not found`.
- **`state` must be a bare enum string** — `REQUESTED|STARTED|TERMINATED|COMPLETED|SUSPENDED`. `"dspace:STARTED"` fails the enum.
- Required by schema: `@context`, `@type`, `providerPid`, `consumerPid`, `state`. Extra properties and an `@id` are tolerated (`additionalProperties` is not false).
- **Only `providerPid` is actually read from the ACK.** `stringIdProperty("…/providerPid", …)` — it must expand to `@id` form (it does, given the right `@context` + `@type`). A missing one → `Property '…providerPid' was not found`. `consumerPid` and `state` are schema-required but not consumed here.
- **The providerPid you return becomes the `{id}` in all five subsequent requests.** Make sure it routes.
- **A 4xx here gets retried 3× with 200 ms → 400 ms backoff** before failing. Do not read three identical POSTs in your logs as a TCK bug, and do not make your error path non-idempotent. A 404 short-circuits the retry and throws immediately.

---

### 1.5 State observation — the GET is real and unforgiving

**Instruction.** `GET {dataspacetck.dsp.connector.http.url}/transfers/{providerPid}` must return a valid TransferProcess body, **with the same shape rules as §1.4**, correct at the *first* poll.

**Confidence: bytecode-confirmed plus a runtime probe** (Awaitility poll cadence and abort-on-throw measured: 6 polls in 634 ms; threw after 1 call). Not independently refuted.

- The TCK asserts state **both** ways, in pairs: `thenWaitForState(X)` (no HTTP — polls the TCK's own in-memory state, which only changes when *you* push a message to its callback endpoint) immediately followed by `thenVerifyProviderState(X)` (the GET).
- **No `Accept` header is set.** Only the optional `Authorization` interceptor.
- **A bad status aborts on the first poll.** A 404 → `AssertionError("Unexpected 404 received for request: …")`; any non-2xx → `AssertionError("Unexpected response code: …")`. These are `Error`s thrown inside the Awaitility condition, and Awaitility's default exception ignorer is `t -> false` — **it ignores nothing**. There is no retry-until-ready. Your endpoint must be correct immediately, not eventually.
- Comparison: `stringIdProperty("https://w3id.org/dspace/2025/1/state", body)` `.equals("https://w3id.org/dspace/2025/1/" + State.name())`. Execution-verified expansions:
  `…/REQUESTED`, `…/STARTED`, `…/COMPLETED`, `…/SUSPENDED`, `…/TERMINATED`.
- `providerPid` is also read (for a debug log) *before* the state check and throws if absent.
- **Timeout: `dataspacetck.dsp.default.wait`, default 15 seconds**, applied to both the latch wait and the Awaitility budget. Poll interval is Awaitility's default 100 ms → up to ~150 GETs over 15 s if every response is valid but not yet matching.
- **`tp_03_01/02/04/05/06` assert the state is UNCHANGED after you reject a message.** Your GET must keep serving correctly (e.g. still `REQUESTED` after you 4xx a `TransferCompletionMessage`, still `TERMINATED` after you 4xx a `TransferStartMessage`).

---

### 1.6 Rejecting a message

**Instruction.** Return **any status in `[400, 500)` except 404**. A body is not read, but emit a `TransferError` anyway.

**Confidence: bytecode-confirmed, single agent.**

- All 12 negative paths (`tp_03_01..06` + `tp_c_03_01..06`) funnel through `HttpFunctions.postJson(..., expectError=true)`. **400 and 409 both pass.** 2xx, 3xx, 5xx and 404 all raise `AssertionError`.
- **404 is fatal even on paths that expect an error.** In `postJson`, the 404 check sits at the *target* of the `expectError` short-circuit jump — it is tested **before** `expectError` is consulted. (In `getJson` the same check *is* guarded by `expectError`; the asymmetry is real.)
- **No body is required on rejection.** `completeTransfer/suspendTransfer/startTransfer/terminateTransfer` call `postJson`, log a debug line, and call `Response.close()` — the stream is never handed to `MessageSerializer`. An empty 400 with no Content-Type passes today.
- **There is no `TransferError` validator in this build.** `AbstractTransferProcessTest.setUp()` registers exactly three: `TransferRequestMessage`, `TransferStartMessage`, `TransferProcess`. No `transfer/transfer-error-schema.json` exists in the jar. (Contrast: the CN suite registers eight, including `ContractNegotiationError`.)
- **Emit a full `TransferError` anyway.** It costs nothing and the "no body required" guarantee is fragile — the sibling calls `transferRequest` and `getTransferProcess` already parse unconditionally. The TCK's own shape (from `TransferFunctions.createTransferErrorResponse` + `withStateTransition`, which uses `code="409"` / `ErrorType.CONFLICT` → HTTP 409):
  ```json
  {"@context": [...], "@type": "TransferError", "providerPid": "...", "consumerPid": "...",
   "code": "409", "reason": [{"message": "..."}]}
  ```
  This is what the TCK *sends*, not what it *asserts of you*. Do not read it as "409 is required".

---

### 1.7 `format` and `dataAddress`

**Instruction.** Accept any non-empty `format` string. **Emit no `dataAddress`** on your `TransferStartMessage`.

**Confidence: bytecode-confirmed, single agent.**

- **Nothing in the provider-role path ever reads a format back.** `getFormat` exists in three classes only: the `TransferProcess` accessor, `ProviderTransferProcessPipelineImpl` (outbound only), and `ConsumerActions` (consumer-role suite). The ACK schema has no `format` property; `thenVerifyProviderState` reads only `state`.
- **But `format` is a `@ConfigParam`** (`TP_01_01_FORMAT`), so a runner can override `"HTTP-PULL"`. Accept whatever arrives; do not whitelist. Defaulting to HTTP-PULL when the key is absent is safe.
- **The TCK sends no `dataAddress` on the TransferRequestMessage** — all 16 provider methods call the 2-arg `sendTransferRequest(agreementId, format)`, which delegates with `null`.
- **Nothing requires a `dataAddress` on your TransferStartMessage**, at three independent layers: schema (`required: [@context, @type, providerPid, consumerPid]`); predicate (every provider test uses the 1-arg `handleStart(Map)` whose default supplies a constant-true predicate — `lambda$handleStart$0` is literally `iconst_1; ireturn`); null-handling (`mapProperty(..., optional=true)` → `toDataAddress(null)` returns null → bare `putfield`).
- **Emitting one is strictly worse.** It gets schema-checked against `data-address-schema.json` (requires `@type: "DataAddress"` **and** `endpointType`), and `endpointType` is then read via `stringIdProperty` (expects `@id`, not `@value`). Extra failure modes for zero assertion benefit.

---

## 2. What is still open

Every item here still costs a TCK cycle. None of them is a disagreement between the finder and the refuter — **on all three re-derived findings the refuter agreed**. These are gaps that bytecode cannot close.

| # | Open question | Why it can't be closed statically | What to watch for |
|---|---|---|---|
| 1 | **The literal `callbackAddress` value your harness advertises.** | `dataspacetck.callback.address` is config. If it ever carries a path component, the TCK's own full-regex match breaks and your correct push 404s. | Log the raw `dspace:callbackAddress` string from the first TransferRequestMessage you receive, then append exactly `/transfers/{consumerPid}/start` to that literal. |
| 2 | **Whether the config key was actually read.** | Missing/blank key fails **silently** (`required=false`); the constructor default (random UUID) wins. A passing test proves nothing. | Set `TP_01_01_AGREEMENTID=SENTINEL-abc123` and confirm that exact string arrives as `dspace:agreementId` on the wire. This is the single highest-value first run. |
| 3 | **`toUpperCase()` is the no-arg default-locale overload.** | Under a Turkish/Azeri default locale, `agreementId` uppercases to `AGREEMENTİD` (U+0130) and would not match. | Confirm host locale or run with `-Duser.language=en`. Practically irrelevant, but it is the only construction in which the stated key is wrong. |
| 4 | **The jar's version/coordinates were never recorded.** | Every path and schema here is a compile-time constant of *this* build. | Before trusting anything across a TCK upgrade, re-run `javap -p -v … AbstractHttpTransferProcessClientImpl \| grep "= String"`. Seconds, not minutes. |
| 5 | **`dataspacetck.dsp.connector.transfer.initiate.url` is a required config key nobody traced.** | Its name and the consumer-side client suggest it is the out-of-band hook for making the CUT act as *consumer*, irrelevant to provider role. | If a provider-only run dies at startup demanding it, that's config validation, not a protocol path. Set it to a dummy. |
| 6 | **Trailing-slash behavior on the base URL.** | Concat is raw (`\u0001\u0001`); nobody ran it. | If you see `//transfers/request` in your access log, that's the cause. Configure without a trailing slash. |
| 7 | **Whether an `AssertionError` really aborts the exchange with no response** on a schema-invalid push. | Refuter read `FallibleDspHandler`'s exception table (`java/lang/Exception` only) and reasoned an `Error` escapes. Not observed. | A push that produces **no response at all** (vs. a 4xx) is the signature. Suspect your body schema before your routing. |
| 8 | **`@type` as a JSON-LD array.** | `JsonObject.getString` on a non-`JsonString` is a `ClassCastException` per the Jakarta JSON-P contract; that is spec-derived, not observed, and it sits outside the only try/catch. | Just emit `@type` as a single string and the question never arises. |
| 9 | **`dataspacetck.dsp.local.connector`.** | If a harness sets it true, `ProviderTransferProcessMockImpl` replaces `NoOpProviderTransferProcessMock` and `ProviderActions` goes live. | Both agents verified the no-op is wired when it's false. Even if true, `ProviderActions` posts to the TCK's own callback and uses the same four path templates — so it cannot change the answers above. |
| 10 | **Contexts other than the bundled one.** | `MessageSerializer` pre-registers only `…/2025/1/context.jsonld` and `…/2025/1/odrl-profile.jsonld`. A different `@context` URL triggers a network fetch; untested. | Use the bundled URL. |
| 11 | **Retry timing** (3 attempts, 200 ms doubling). | Read off `iconst_3` / `ldc2_w 200l`, not measured. | Triple identical POSTs after a 4xx is expected, not a bug. |
| 12 | **Whether anything, anywhere, reads `consumerPid`/`state` from your ACK.** | Verified only for `ProviderTransferProcessPipelineImpl.lambda$sendTransferRequest$0` (the only provider-path caller). | Irrelevant if you emit them, which the schema forces anyway. |

**Log strings to grep for, mapped to cause:**

| Log line | Cause |
|---|---|
| `Unexpected 404 received for request: <url>` | Your route doesn't exist, or the pid doesn't match what you returned in the ACK. **No retry** — fails instantly. Also fatal on paths that expect an error. |
| `Unexpected response code: <code>` | Non-2xx on a success path, or 2xx/3xx/5xx on a rejection path. |
| `Invalid message: [string found, array expected, …]` | Your `@context` is a bare string, not an array. |
| `Invalid message: [… does not have a value in the enumeration …]` | Your `state` is prefixed (`dspace:STARTED`) instead of bare. |
| `Property 'https://w3id.org/dspace/2025/1/state' was not found` | Your `@type` is `"dspace:TransferProcess"` instead of `"TransferProcess"` — schema silently skipped, then expansion fails. |
| `Property 'https://w3id.org/dspace/2025/1/providerPid' was not found` | Missing providerPid in ACK or GET response. |
| `Cannot deserialize json: …` | Empty body (e.g. you returned 204). |
| `Invalid JsonLd Document, expecting a @type attribute` | Top-level `@type` missing entirely. |
| `Timeout waiting for for provider transfer process state to be X` | (double "for" is real) The GET never returned matching state within 15 s. |
| `Timeout waiting for state to transition to X` | Your push never arrived or never matched a registered handler. |
| HTTP 404 + `No handler registered on this endpoint` **from the TCK** | You pushed to an unregistered path, or pushed a second time after single-shot deregistration. |
| No response at all on a push | Likely a schema `AssertionError` escaping `FallibleDspHandler`. |

---

## 3. Where the current plan is wrong

**Stated plainly: none of the four plan assumptions is contradicted by the evidence. All four are correct.** The risk is not that the plan is wrong — it's that three of the four are *under-specified* in ways that will each burn a multi-minute cycle.

| Plan assumption | Verdict | What the plan is missing |
|---|---|---|
| `POST {version}/transfers/request`, `GET {version}/transfers/{id}`, `POST {version}/transfers/{id}/{start,completion,suspension,termination}` | **CORRECT and complete.** Independently confirmed by two agents; the list is provably closed (only six classes in the jar contain the literal `transfers`, only two of them outbound). | The `{version}` prefix **does not come from the TCK** — there is no version constant in the transfer client. It comes entirely from `dataspacetck.dsp.connector.http.url`. Set that to `https://host/2025-1`, **without a trailing slash** (concat is raw). Do **not** put the prefix in `dataspacetck.dsp.connector.http.base.url` — that key is only for `/.well-known/dspace-version`. |
| `{id}` = the connector's own provider pid | **CORRECT.** Specifically: the exact value you emit as `dspace:providerPid` in the ACK body of `POST /transfers/request`. Not the consumer pid, not a TCK-side id. | Nothing missing. Just make sure the pid you mint is routable. |
| Config key shaped `TP_01_01_AGREEMENTID` | **CORRECT** — strongest-confidence item in this document. | **Incomplete in a way that will silently pass and then fail later.** You need 16 keys (`tp_01_01..05`, `tp_02_01..05`, `tp_03_01..06`), not one. A missing or blank key does not error — it falls back to a random UUID, so tests can fail for reasons that look nothing like config. Add a sentinel value on the first run and confirm it on the wire. |
| Start-message push to callback base + `/transfers/{consumerPid}/start` | **CORRECT.** | Two refinements. (a) **The pid segment is not validated** — the handler literally never sees the path. What actually correlates is `consumerPid` **in the JSON body**. If your start message is being ignored, the path is not the suspect; the body's consumerPid is. (b) **Registration is single-shot.** A retried start, or the second start in the `tp_01_04` suspend→restart flow, gets a 404 unless the test declared a second `expectStartMessage`. Also: append to the callbackAddress string **verbatim** — the TCK does a full-regex match against the raw path, so any prefix you add or slash you drop breaks it. |
| No `dataAddress` emitted | **CORRECT, and it is the better choice**, not merely tolerated. | Emitting one is strictly worse: it activates `data-address-schema.json` (requires `@type: "DataAddress"` and `endpointType`) and an `@id`-form expansion check, for zero assertion benefit. Keep it out. |

**The one thing the plan appears not to cover at all:** the two "unconfirmed values" are now settled, but §1.4 (the ACK body shape) and §1.5 (the GET being polled, aborting on the first bad response) are where a working implementation is most likely to fail on its first TCK run. The `@context`-must-be-an-array rule in particular is **execution-verified** to fail with a bare string, and it is the kind of thing a hand-written JSON-LD emitter gets wrong by default.

---

## 4. Contradictions between findings

**No substantive contradictions.** All seven findings and all three refutations are mutually consistent on every fact that affects the implementation. Four factual corrections, all in the direction of *more* certainty:

1. **`DspSystemLauncher` location.** `start-callback-path` (finder) wrote that the launcher is "a separate artifact, not in this jar" and left its caveat 1 open pending a TCK run. **This is wrong** — the refuter dumped it from the same jar and enumerated all nine of its config keys, none of which touches the callback address or path. `DefaultCallbackEndpoint` is the only `CallbackEndpoint` implementation present, and the launcher resolves it. **The caveat is closed in-jar.** Resolved in the finder's favour.
2. **`ProviderActions` reference count.** `inbound-paths` (finder) said it is referenced from `TransferProcessProvider01Test`. The refuter found it referenced from `01Test`, `02Test`, **and** `03Test` — three times the surface the finder described. Outcome unchanged: in all three it appears only as a method-reference handed to `transferProcessMock.recordTransferRequestedAction(...)`, and `NoOpProviderTransferProcessMock.recordTransferRequestedAction` is a bare `return`. The finder *inferred* this from class names and admitted so; the refuter **traced** it. **Now a fact, not an inference.**
3. **Garbled evidence transcription (cosmetic).** Both `inbound-paths` and `configparam-key` quoted StringConcat recipes with the `\u0001` placeholder bytes eaten by the shell (`#152 = String //` appearing empty, `#318 _`). The real constants are `\u0001\u0001` and `\u0001_\u0001`. The refuter re-dumped them with `od -c`. **Conclusions were correct; the quoted bytes were just mangled in transit.**
4. **Success-response shape on push (refinement, not conflict).** `start-callback-path` said "on success the TCK replies 200 with a serialized JSON body". True for start/completion/suspension (`FallibleDspHandler`, which also maps `ErrorType` → 400/401/404/500/409). **Termination is different**: `expectTerminationMessage` uses `registerHandler` → `DelegatingProtocolHandler`, which hardcodes `200` with no try/catch and no error mapping.

**Consistency spot-checks that held across independent findings:**

- `HttpFunctions.postJson` retry semantics are described by three separate agents (`request-response`, `error-shape`, `inbound-paths` refuter) with identical constants: 3 attempts, 200 ms → 400 ms, only for 4xx-non-404, only when `expectError=false`. Agreed.
- The `getJson` vs `postJson` 404 asymmetry (guarded by `expectError` in the former, unguarded in the latter) is reported once by `error-shape` and is consistent with `state-assertions`' "aborts on first poll" — `thenVerifyProviderState` uses `expectError=false`, so both descriptions collapse to the same behavior.
- The `@context`-array / plain-`@type` / bare-`state` rules appear independently in `request-response` (bytecode + schema files) and `state-assertions` (execution probes against the TCK's own serializer). Identical conclusions, different methods. **This is the second-strongest signal in the document after the config-key rule.**

**Two flagged weaknesses, stated without softening:**

- **§1.4, §1.5, §1.6 and §1.7 were each produced by a single agent and never independently refuted.** They are bytecode-confirmed (and §1.5 partly execution-verified), but they have not survived an adversarial pass the way §1.1–§1.3 have. Treat them as strong, not as settled-beyond-doubt.
- **The "no body required on rejection" result in §1.6 is the most fragile finding here.** It holds only because `AbstractHttpTransferProcessClientImpl` closes the response without touching it — while its sibling `transferRequest` already parses unconditionally. One TCK release that starts reading transfer error bodies would immediately require a `@type`. Emit a `TransferError` body regardless. What is **not** fragile, and must not be relied on either way: **404 is never accepted, on any path.**