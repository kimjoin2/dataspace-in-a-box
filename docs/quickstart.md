# Quickstart

This stands a provider and a consumer up as native processes on one machine,
has them authenticate against a signed roster, negotiate an agreement, and
move a file — then checks that the file which arrived is the file that was
sent.

Every command below is run by CI, on Linux and on macOS, from this document
itself: `cmd/mdscript` turns the blocks here into a shell script, and the
`quickstart` job runs it. There is no second copy to drift from. If something
here stops working, the build goes red before a reader finds out.

Run `make quickstart` to do the whole thing at once, or follow along from the
repository root. A block tagged `sh` is a command to run; a block tagged with
a path writes that file.

What this does not cover: `did:web` resolution, revoking a participant, and
running the two connectors on different machines. The last of those needs
nothing new here — give each connector the other's real address in the roster
— but nothing in this repository tests it, so it is not claimed.

## Build the binaries, and find this machine's address

`make build` builds the connector alone, so the operator tool is built here
too. Both go into the scratch directory this document works in, which is also
where every file it writes lands, so that removing that one directory undoes
everything.

The two connectors must reach each other at an address that is **not**
loopback. This is the step readers get wrong, so it is worth saying why before
it fails: a callback address on the loopback interface reaches only the
connector's own host, on any network, so no deployment can make sending there
safe — the connector refuses it, and `DECISIONS.md` section 23.6 records the
reasoning. `127.0.0.1` therefore cannot work here even with both connectors on
one machine, and `dev_mode` does not relax it. What does work is this
machine's address on its own network, which is what the block below finds.

```sh
rm -rf quickstart-run
mkdir -p quickstart-run/data/provider quickstart-run/data/consumer
go build -o quickstart-run/dsbox ./cmd/dsbox
go build -o quickstart-run/dsops ./cmd/dsops
export PATH="$PWD/quickstart-run:$PATH"

DSBOX_HOST=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$DSBOX_HOST" ] || DSBOX_HOST=$(ipconfig getifaddr "$(route -n get default 2>/dev/null | awk '/interface:/{print $2}')" 2>/dev/null)
if [ -z "$DSBOX_HOST" ]; then
	echo "no non-loopback address found for this machine." >&2
	echo "the connectors need one to reach each other; set DSBOX_HOST yourself and re-run." >&2
	exit 1
fi
export DSBOX_HOST
echo "    connectors will reach each other at $DSBOX_HOST"
```

If that finds nothing, or finds the wrong one — a VPN interface, a machine
with only a public address — set `DSBOX_HOST` yourself to any address this
machine holds that is not loopback, and run again. Both connectors are on this
host, so the address only has to be one the host answers on.

When an address is refused later, the refusal does not name it. That is
deliberate: the endpoint would otherwise report what resolves where to anyone
who can call it. The connector's own log does say which address and why, which
is what `quickstart-run/provider.log` and `quickstart-run/consumer.log` are
for.

## Mint the identities

Each connector signs what it sends with its own key. The roster is signed by a
separate operator key which is **not** any participant's: the roster is the
registry itself, and signing it with an entry's own key would let that entry
vouch for itself.

The two connectors share one management token here only to keep the document
short. It is the credential for the management API, it has a minimum length
the connector enforces at load, and in a real deployment each connector has
its own.

```sh
PROVIDER_PUB=$(dsops keygen -out quickstart-run/provider.key)
CONSUMER_PUB=$(dsops keygen -out quickstart-run/consumer.key)
OPERATOR_PUB=$(dsops keygen -out quickstart-run/operator.key)
ROSTER_EXPIRY=$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)
MGMT_TOKEN=quickstart-management-token
```

Two operators who have never met would exchange these public keys and the
operator key out of band, and each would build the same roster from them.
Doing that on one machine, as here, is the step this quickstart does not
demonstrate — see `config.example.yaml` on roster distribution.

## Write the roster

The roster says whose signatures each connector accepts. The provider's entry
carries the address it is reached at; the consumer derives the address it
dials from this file rather than from whatever an operator types at the
initiate call, which is why a wrong address here is a refusal rather than a
misdirected message.

**That address must include the protocol version path.** `http://host:8080`
is not enough; it is `http://host:8080/2025-1`. Message paths are appended to
it, and a base without the version produces a `404` at the counterparty and
very little to read at this end.

The consumer's entry carries no address, because nothing here initiates
toward it. The field is optional for exactly that reason.

The signature is written as a placeholder and filled in below. A signature
covers the participants, the version, and the expiry, computed over a
re-marshalling of those fields rather than over the file's bytes — so writing
a placeholder first and substituting into it afterwards yields the same
document as signing a file that never held one.

```json title=quickstart-run/roster.json expand
{
  "participants": [
    {"id": "urn:participant:provider", "public_key": "$PROVIDER_PUB", "connector_address": "http://$DSBOX_HOST:8080/2025-1"},
    {"id": "urn:participant:consumer", "public_key": "$CONSUMER_PUB"}
  ],
  "version": 1,
  "expires_at": "$ROSTER_EXPIRY",
  "signature": "PLACEHOLDER-SIGNATURE"
}
```

```sh
ROSTER_SIGNATURE=$(dsops roster sign -roster quickstart-run/roster.json -key quickstart-run/operator.key)
sed "s|PLACEHOLDER-SIGNATURE|$ROSTER_SIGNATURE|" quickstart-run/roster.json >quickstart-run/roster.tmp
mv quickstart-run/roster.tmp quickstart-run/roster.json
```

## Write the file that will move

Generated rather than committed, so that what follows proves a transfer took
place rather than that a fixture exists.

```csv title=quickstart-run/sample.csv
id,city,population
1,Seoul,9411000
2,Busan,3349000
3,Incheon,2954000
```

## Configure the provider

`public_url` is the address this connector puts on the wire as its own, and it
must use `https` unless the instance declares itself a development one. The
listen addresses are set explicitly rather than left to their defaults: the
defaults would collide with the consumer's, and `dsp_addr` would otherwise
bind every interface on this machine.

A dataset is served because it has a `source_file`. Without one it is
advertised and cannot be fetched.

```yaml title=quickstart-run/provider.yaml expand
public_url: http://$DSBOX_HOST:8080
dev_mode: true
participant_id: urn:participant:provider
participant_key: quickstart-run/provider.key
roster: quickstart-run/roster.json
roster_signer: $OPERATOR_PUB
dsp_addr: $DSBOX_HOST:8080
mgmt_addr: 127.0.0.1:8081
mgmt_token: $MGMT_TOKEN
data_dir: quickstart-run/data/provider
datasets:
  - id: urn:dataset:sample
    source_file: quickstart-run/sample.csv
```

## Configure the consumer

The consumer advertises nothing and pulls what it negotiates for. Its
`public_url` is where the provider pushes protocol messages back to, so it
carries this machine's network address for the same reason the provider's
does.

```yaml title=quickstart-run/consumer.yaml expand
public_url: http://$DSBOX_HOST:8180
dev_mode: true
participant_id: urn:participant:consumer
participant_key: quickstart-run/consumer.key
roster: quickstart-run/roster.json
roster_signer: $OPERATOR_PUB
dsp_addr: $DSBOX_HOST:8180
mgmt_addr: 127.0.0.1:8181
mgmt_token: $MGMT_TOKEN
data_dir: quickstart-run/data/consumer
```

## Start both connectors

The management listener answers an unauthenticated readiness probe, and it
stays on loopback because that is where the operator drives it from.

The ports are checked before anything starts, and that ordering is the point.
The probe is unauthenticated, so a connector left running from an earlier
attempt answers for one that has just died on a port collision — and the dead
one logs that it started, and that it is listening, before it logs that it
could not bind. Asking afterwards cannot tell those apart reliably: the
answer arrives instantly, and whether the shell has yet noticed its own child
died is a race. Asking first has no race in it.

```sh
for port in 8081 8181; do
	if curl -sf -m 2 "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
		echo "something is already listening on port $port." >&2
		echo "a connector from an earlier run is probably still up. stop it and try again." >&2
		exit 1
	fi
done

dsbox -config quickstart-run/provider.yaml >quickstart-run/provider.log 2>&1 &
PROVIDER_PID=$!
dsbox -config quickstart-run/consumer.yaml >quickstart-run/consumer.log 2>&1 &
CONSUMER_PID=$!

cleanup() {
	status=$?
	kill "$PROVIDER_PID" "$CONSUMER_PID" 2>/dev/null || true
	if [ "$status" -ne 0 ]; then
		echo "--- provider log ---" >&2
		cat quickstart-run/provider.log >&2 2>/dev/null || true
		echo "--- consumer log ---" >&2
		cat quickstart-run/consumer.log >&2 2>/dev/null || true
	fi
}
trap cleanup EXIT INT TERM

wait_ready() {
	port=$1
	name=$2
	pid=$3
	i=0
	until curl -sf "http://127.0.0.1:$port/health" >/dev/null 2>&1; do
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "$name exited before it became ready" >&2
			exit 1
		fi
		i=$((i + 1))
		if [ "$i" -ge 30 ]; then
			echo "$name did not become ready" >&2
			exit 1
		fi
		sleep 1
	done
	echo "    $name ready"
}
wait_ready 8081 provider "$PROVIDER_PID"
wait_ready 8181 consumer "$CONSUMER_PID"
```

## Ask the provider what it offers

An offer identifier is derived by a convention private to this implementation,
so a consumer that has not been told one out of band can only learn it by
asking. Asking is an operator action, and it lives on the consumer's
management listener.

The reply carries the provider's address as the roster gives it, which is the
address the negotiation below uses.

```sh
CATALOG=$(curl -sf "http://127.0.0.1:8181/catalog?providerId=urn:participant:provider" \
	-H "Authorization: Bearer $MGMT_TOKEN")
ADDRESS=$(printf '%s' "$CATALOG" | sed -n 's/.*"connectorAddress":"\([^"]*\)".*/\1/p')
OFFER=$(printf '%s' "$CATALOG" |
	sed -n 's/.*"id":"urn:dataset:sample","offerId":"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$OFFER" ] || [ -z "$ADDRESS" ]; then
	echo "discovery did not return what the negotiation needs" >&2
	printf '%s\n' "$CATALOG" >&2
	exit 1
fi
echo "    offer $OFFER"
```

## Negotiate an agreement

The connector answers as soon as the negotiation is recorded and then carries
out the exchange on its own, so the agreement appears afterwards rather than
in this response.

```sh
curl -sf -X POST http://127.0.0.1:8181/negotiations/initiate \
	-H "Authorization: Bearer $MGMT_TOKEN" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"offerId\":\"$OFFER\",\"datasetId\":\"urn:dataset:sample\",\"connectorAddress\":\"$ADDRESS\"}" \
	>/dev/null

AGREEMENT=""
i=0
while [ "$i" -lt 30 ]; do
	AGREEMENT=$(curl -sf http://127.0.0.1:8181/agreements \
		-H "Authorization: Bearer $MGMT_TOKEN" 2>/dev/null |
		sed -n 's/.*"agreementId":"\([^"]*\)".*/\1/p' | head -1 || true)
	[ -n "$AGREEMENT" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$AGREEMENT" ]; then
	echo "no agreement was concluded" >&2
	exit 1
fi
echo "    agreement $AGREEMENT"
```

## Transfer the file, and check that it arrived

`format` is written out here rather than discovered. The catalog advertises a
placeholder rather than a real distribution format, so this is one value that
still travels out of band — the gap is recorded in `docs/follow-ups.md`.

The consumer writes what it pulls under its own data directory, under a name
it assigns. A download still in flight is named so that it can be told apart
from a finished one, which is what the search below excludes.

```sh
curl -sf -X POST http://127.0.0.1:8181/transfers/initiate \
	-H "Authorization: Bearer $MGMT_TOKEN" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$AGREEMENT\",\"format\":\"HTTP-PULL\",\"connectorAddress\":\"$ADDRESS\"}" \
	>/dev/null

RECEIVED=""
i=0
while [ "$i" -lt 30 ]; do
	RECEIVED=$(find quickstart-run/data/consumer/downloads -type f ! -name '.partial-*' 2>/dev/null | head -1 || true)
	[ -n "$RECEIVED" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$RECEIVED" ]; then
	echo "no file arrived" >&2
	exit 1
fi
if ! diff -q quickstart-run/sample.csv "$RECEIVED" >/dev/null; then
	echo "the file that arrived is not the file that was sent" >&2
	diff quickstart-run/sample.csv "$RECEIVED" >&2 || true
	exit 1
fi

echo
echo "moved $(wc -c <"$RECEIVED" | tr -d ' ') bytes from provider to consumer,"
echo "under a negotiated agreement, between authenticated participants."
echo "  sent:     quickstart-run/sample.csv"
echo "  received: $RECEIVED"
```

## Stop, and start over

Stop both connectors here rather than leaving it to the trap, so that what
follows is a machine with nothing of this still running on it. The trap stays
installed for the paths that gave up partway.

```sh
kill "$PROVIDER_PID" "$CONSUMER_PID" 2>/dev/null || true
wait "$PROVIDER_PID" 2>/dev/null || true
wait "$CONSUMER_PID" 2>/dev/null || true
echo "    stopped both connectors"
```

Everything written along the way — the binaries, keys, roster, configurations,
databases, and the file that moved — is under `quickstart-run`, and nothing
was written anywhere else. The file that arrived is still there to look at,
which is why this does not delete it:

    rm -rf quickstart-run

Running it is optional. The first block removes that directory before it does
anything, so a second run is a clean one rather than a run against a database
that remembers the first.
