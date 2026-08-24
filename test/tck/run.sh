#!/bin/sh
# Brings up the connector, runs the TCK against it, captures stdout, tears down.
# Always exits 0 when the TCK ran to completion: judging the output is the
# gate's job, not this script's.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/../.." && pwd)
compose="docker compose -f $dir/compose.yaml"
output="$root/tck-output.txt"
# The connector's own log, captured before teardown. It is the only place the
# values the TCK actually put on the wire can be read back, since the TCK's
# output records assertions rather than message bodies. That is what tells a
# real protocol fault apart from a fixture the TCK never read: an unset
# @ConfigParam falls back to a random UUID silently, and the two produce
# identical failures everywhere except in this file.
connector_log="$root/tck-connector.txt"

cleanup() {
	$compose logs --no-color dsbox >"$connector_log" 2>&1 || true
	$compose down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Identities, before the connector starts: it loads the roster once at
# startup, so both keys and the roster have to exist first. The TCK's
# credential is minted later, after seeding — a cold image build here can take
# minutes, and a token minted now would spend its five-minute life on the
# build rather than on the suite.
#
# Generated into a gitignored directory. No key material is ever committed,
# and the harness makes its own rather than carrying a fixture.
identity="$dir/identities"
rm -rf "$identity"
mkdir -p "$identity"
( cd "$root" && go build -o "$identity/dsops" ./cmd/dsops )

connector_pub=$("$identity/dsops" keygen -out "$identity/connector.key")
tck_pub=$("$identity/dsops" keygen -out "$identity/tck.key")
operator_pub=$("$identity/dsops" keygen -out "$identity/operator.key")
# keygen writes 0600 (correct for a real operator's own long-lived key), but
# connector.key is bind-mounted into the dsbox container below, which runs as
# the distroless nonroot image's UID 65532 — a different UID than whatever
# generated this file. A real Linux Docker host enforces that mismatch as a
# permission error at container startup; Docker Desktop's macOS VM does not,
# which is why this only ever failed in CI. Relaxed here, not in keygen
# itself: this file is regenerated every run and deleted with $identity
# afterward, unlike a real operator's key.
chmod 644 "$identity/connector.key"
# The TCK's id is TCK_PARTICIPANT because that is the string the harness
# hardcodes as the providerId in both initiate bodies
# (DspConstants.TCK_PARTICIPANT_ID in the pinned image, inlined at every call
# site and not configurable). Its authenticated identity has to equal the
# name it claims, or the connector records a counterparty no inbound request
# will ever present. That coupling is to a constant in an upstream image; it
# is safe because compose.yaml pins that image by digest, and if the pin ever
# moves and the constant changes, the symptom is every CN_C and TP_C result
# failing on a refusal that reads like a protocol bug.
cat >"$identity/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:dsbox-test", "public_key": "$connector_pub"},
    {"id": "TCK_PARTICIPANT", "public_key": "$tck_pub"}
  ]
}
EOF
signature=$("$identity/dsops" roster sign -roster "$identity/roster.json" -key "$identity/operator.key")
cat >"$identity/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:dsbox-test", "public_key": "$connector_pub"},
    {"id": "TCK_PARTICIPANT", "public_key": "$tck_pub"}
  ],
  "signature": "$signature"
}
EOF
export DSBOX_ROSTER_SIGNER="$operator_pub"

$compose up -d --build dsbox
# Recorded so the check after the TCK run can prove this is still the
# container that gets seeded below. See that check for what it guards.
connector_id=$($compose ps -q dsbox)

printf 'waiting for the connector'
i=0
until curl -sf http://127.0.0.1:8081/health >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		echo
		echo "connector did not become ready" >&2
		$compose logs dsbox >&2
		exit 1
	fi
	printf '.'
	sleep 1
done
echo ' ready'

# The TP suite asks this connector to transfer under an agreement id the TCK
# supplies from config.properties. Those agreements were concluded outside
# this connector as far as it is concerned, so they are imported through the
# management API — the same path a real operator would use. This connector
# rejects a transfer citing an agreement it has no record of, which is the
# point of the check and the reason this step exists.
#
# It exits on the first failure rather than reporting at the end: a connector
# that rejects an unknown agreement and a connector that was never given the
# agreement both answer 400, so a silent seeding failure would be
# indistinguishable from a protocol bug for the whole run.
#
# The `|| true` is what makes that loud failure reachable. Under `set -e` a
# command substitution that exits non-zero aborts the script at the assignment
# itself, before the diagnostic below can print — so a transport failure
# (connection refused, DNS, TLS) would kill the run with a bare exit code and
# no message, which is exactly what this block exists to prevent. With it,
# curl's own '%{http_code}' of 000 flows through to the check and gets named.
seed_agreement() {
	code=$(curl -s -o /dev/null -w '%{http_code}' \
		-X POST http://127.0.0.1:8081/agreements \
		-H 'Authorization: Bearer tck-harness-token-0' \
		-H 'Content-Type: application/json' \
		-d "{\"agreementId\":\"$1\",\"datasetId\":\"urn:dataset:tck-transfer\"}") || true
	if [ "$code" != "201" ]; then
		echo "seeding agreement $1 failed with HTTP $code" >&2
		exit 1
	fi
}

# Seven ids, matching config.properties' TP_*_AGREEMENTID values and
# dsbox.yaml's transfer_policies. Adding a test that cites a new agreement
# means adding it in all three places.
seed_agreement urn:uuid:tck-tp-01-01
seed_agreement urn:uuid:tck-tp-01-02
seed_agreement urn:uuid:tck-tp-01-03
seed_agreement urn:uuid:tck-tp-01-04
seed_agreement urn:uuid:tck-tp-01-05
seed_agreement urn:uuid:tck-tp-default
seed_agreement urn:uuid:tck-tp-nostart
# TP_C. Every consumer-role test needs one too, because POST
# /transfers/initiate refuses an agreement this connector has no record of —
# the same rule, from the other role. Twelve of the sixteen share the passive
# id; only the consumer-driven tests need their own.
seed_agreement urn:uuid:tck-tpc-passive
seed_agreement urn:uuid:tck-tpc-02-01
seed_agreement urn:uuid:tck-tpc-02-02
seed_agreement urn:uuid:tck-tpc-02-03
seed_agreement urn:uuid:tck-tpc-02-05
echo 'seeded 12 transfer agreements'

# The TCK's credential, minted now that everything slow is behind us. It is a
# static string for the whole run: DspSystemLauncher registers it as an
# interceptor once and cannot refresh it. A suite takes about 54 seconds
# against a five-minute life, so the margin is the build time this deliberately
# sits after.
token=$("$identity/dsops" token -key "$identity/tck.key" \
	-iss TCK_PARTICIPANT -aud urn:participant:dsbox-test)
cat "$dir/config.properties" >"$identity/config.properties"
printf '\ndataspacetck.dsp.connector.http.headers.authorization=Bearer %s\n' \
	"$token" >>"$identity/config.properties"

# --use-aliases: `compose run` does not register the service's own name as a
# network alias by default (only `up` does), so without this flag the
# connector's callback pushes to http://tck:8083 fail DNS resolution with
# "no such host" the moment any test (first hit: CN:02-01) needs one.
#
# --no-deps: `run` otherwise brings up the `depends_on` service, and Compose
# is free to *recreate* a dependency it considers out of date rather than
# leave the running one alone. That replaces the connector container — and
# with it the whole database, since data_dir is not a volume — throwing away
# the twelve agreements seeded above and failing all 30 TP/TP_C tests with
# "no agreement with id". dsbox is already up from the `up -d` above and
# already carries its network alias from that, so there is nothing here for
# Compose to start. Whether it recreates is version-dependent, which is why
# this only ever failed in CI: Docker Desktop's Compose left the container
# alone and the runner's did not.
$compose run --rm --no-deps --use-aliases tck >"$output" 2>&1 || true

# The connector has to be the same container that was seeded, or the run
# proved nothing: a connector that never received the agreements and one that
# rejects them answer identically. Checked rather than trusted, because the
# failure it guards against was silent for four days and looked like a
# protocol bug in the output.
if [ "$($compose ps -q dsbox)" != "$connector_id" ]; then
	echo "the connector container was replaced during the run; the seeded agreements went to a container that no longer exists" >&2
	exit 1
fi

echo "TCK output written to $output"
echo "connector log will be written to $connector_log"
