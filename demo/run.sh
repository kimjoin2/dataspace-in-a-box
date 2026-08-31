#!/bin/sh
# Stands two connectors up, has them authenticate, negotiate, transfer, and
# move a real file, then checks the file that arrived is the one that was
# sent. Exits non-zero if it is not, or if it never arrives.
#
# This is the milestone's real test. No TCK suite moves a byte, so nothing
# upstream can tell us the data plane works — this can.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/.." && pwd)
compose="docker compose -f $dir/compose.yaml"
gen="$dir/generated"

cleanup() {
	$compose logs --no-color >"$dir/demo.log" 2>&1 || true
	$compose down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> identities"
rm -rf "$gen"
mkdir -p "$gen/consumer-data"
chmod 777 "$gen/consumer-data"
( cd "$root" && go build -o "$gen/dsops" ./cmd/dsops )
provider_pub=$("$gen/dsops" keygen -out "$gen/provider.key")
consumer_pub=$("$gen/dsops" keygen -out "$gen/consumer.key")
operator_pub=$("$gen/dsops" keygen -out "$gen/operator.key")
# keygen writes 0600 (correct for a real operator's own long-lived key), but
# provider.key and consumer.key are bind-mounted into their containers below,
# which run as the distroless nonroot image's UID 65532 — a different UID
# than whatever generated these files. A real Linux Docker host enforces
# that mismatch as a permission error at container startup; Docker Desktop's
# macOS VM does not, which is why this only ever failed in CI. Relaxed here,
# not in keygen itself: these files are regenerated every run and deleted
# with $gen afterward, unlike a real operator's key.
chmod 644 "$gen/provider.key" "$gen/consumer.key"

# Computed once: the two heredocs below must carry the same value, because
# the signature covers it and the second copy is what the connector loads.
# A day out — long enough for a cold image build plus the suite, short
# enough to be a real timestamp rather than a far-future constant that would
# leave the field decorative.
#
# date has no portable relative form: GNU takes -d, macOS takes -v, and
# busybox has neither, where this aborts under set -eu rather than producing
# a wrong timestamp. Verified on macOS: the GNU form fails cleanly and the
# fallback runs.
roster_expiry=$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
	|| date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)
# The provider carries the in-network address the connectors reach each other
# at, version path included -- dsbox serves DSP under one. The consumer
# carries none: nothing in this demo initiates toward it, and a participant
# this connector only ever receives from needs no address.
cat >"$gen/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:provider", "public_key": "$provider_pub", "connector_address": "http://provider:8080/2025-1"},
    {"id": "urn:participant:consumer", "public_key": "$consumer_pub"}
  ],
  "version": 1,
  "expires_at": "$roster_expiry"
}
EOF
# roster_signer, not a participant: the roster is the registry itself, and
# signing it with an entry's own key would let that entry vouch for itself.
signature=$("$gen/dsops" roster sign -roster "$gen/roster.json" -key "$gen/operator.key")
cat >"$gen/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:provider", "public_key": "$provider_pub", "connector_address": "http://provider:8080/2025-1"},
    {"id": "urn:participant:consumer", "public_key": "$consumer_pub"}
  ],
  "version": 1,
  "expires_at": "$roster_expiry",
  "signature": "$signature"
}
EOF
export DSBOX_ROSTER_SIGNER="$operator_pub"

# The file that will be moved. Generated rather than committed so the demo
# proves a transfer rather than the presence of a fixture.
cat >"$gen/sample.csv" <<'EOF'
id,city,population
1,Seoul,9411000
2,Busan,3349000
3,Incheon,2954000
EOF

# A larger file than sample.csv, generated the same way and for the same
# reason — proving a transfer, not the presence of a fixture — but large
# enough that truncating it partway through and resuming is a meaningful
# exercise rather than a coin flip on three lines.
: >"$gen/sample-resume.csv"
i=1
while [ "$i" -le 500 ]; do
	echo "$i,row-$i,$((i * 37))" >>"$gen/sample-resume.csv"
	i=$((i + 1))
done

echo "==> connectors"
$compose up -d --build >/dev/null

# An expired roster reaches this loop by either of the routes the design
# provides. A roster already past its expires_at kills the process at boot,
# so curl gets a refused connection; a roster that expires mid-run leaves
# /health answering 503, which curl -sf also treats as a failure. Either way
# this reports the connector as not ready after the full cap. That is
# correct — a connector that can serve no counterparty is not ready — but the
# message names the symptom. The reason is in the logs dumped below.
wait_ready() {
	i=0
	until curl -sf "http://127.0.0.1:$1/health" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -ge 60 ] && { echo "$2 did not become ready" >&2; $compose logs "$2" >&2; exit 1; }
		sleep 1
	done
	echo "    $2 ready"
}
wait_ready 9181 provider
wait_ready 9281 consumer

# Ask the provider what it advertises, instead of knowing it in advance. The
# offer identifier is derived by a convention private to this implementation,
# so a consumer that has not been told it out of band can only learn it here.
#
# The transfer format is read here too, which it was not before: the catalog
# advertised a placeholder, so this script had to know a value the provider
# had never told it.
#
# What this still does not change is how the agreement ids are read further
# down — those sed calls depend on the field order of the agreements response,
# exactly as they did before, and this script stays a demonstration rather
# than a client anyone should copy.
echo "==> discovery"
catalog=$(curl -sf "http://127.0.0.1:9281/catalog?providerId=urn:participant:provider" \
	-H "Authorization: Bearer demo-management-token")
address=$(printf '%s' "$catalog" | sed -n 's/.*"connectorAddress":"\([^"]*\)".*/\1/p')
offer=$(printf '%s' "$catalog" |
	sed -n 's/.*"id":"urn:dataset:sample","offerId":"\([^"]*\)".*/\1/p' | head -1)
resume_offer=$(printf '%s' "$catalog" |
	sed -n 's/.*"id":"urn:dataset:sample-resume","offerId":"\([^"]*\)".*/\1/p' | head -1)
# The transfer format comes out of the catalog now rather than being written
# here. It was the last value this script supplied that the provider had never
# told it.
#
# Anchored to its own dataset, like the offer ids above. An unanchored pattern
# reads whichever format appears last in the response, which is a different
# dataset's -- and when a dataset advertises none, it silently borrows a
# neighbour's instead of leaving the value empty for the check below.
format=$(printf '%s' "$catalog" |
	sed -n 's/.*"id":"urn:dataset:sample","offerId":"[^"]*","format":"\([^"]*\)".*/\1/p')
resume_format=$(printf '%s' "$catalog" |
	sed -n 's/.*"id":"urn:dataset:sample-resume","offerId":"[^"]*","format":"\([^"]*\)".*/\1/p')
if [ -z "$offer" ] || [ -z "$resume_offer" ] || [ -z "$address" ] ||
	[ -z "$format" ] || [ -z "$resume_format" ]; then
	echo "discovery did not return what the negotiations need" >&2
	printf '%s\n' "$catalog" >&2
	exit 1
fi

echo "==> negotiate"
# Driven from the host over the published ports: the image is distroless, so
# there is no shell inside to drive it from. The initiate hooks are on the
# consumer's management listener, so this is its management port and its
# management token — asking a connector to start an exchange is an operator
# action. connectorAddress is the in-network name, because that is the
# address the connectors use to reach each other.
curl -sf -X POST http://127.0.0.1:9281/negotiations/initiate \
	-H "Authorization: Bearer demo-management-token" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"offerId\":\"$offer\",\"datasetId\":\"urn:dataset:sample\",\"connectorAddress\":\"$address\"}" \
	>/dev/null

echo "==> waiting for the agreement"
agreement=""
i=0
while [ "$i" -lt 40 ]; do
	agreement=$(curl -sf http://127.0.0.1:9281/agreements \
		-H "Authorization: Bearer demo-management-token" 2>/dev/null |
		sed -n 's/.*"agreementId":"\([^"]*\)".*/\1/p' | head -1 || true)
	[ -n "$agreement" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$agreement" ]; then
	echo "no agreement was concluded" >&2
	$compose logs >&2
	exit 1
fi
echo "    agreement $agreement"

# Retried, and only this call is. The provider pushes the agreement to the
# consumer before it writes the agreement row that a transfer request is
# checked against (DECISIONS.md 23.12 fixes that ordering, and 25.1 fixes the
# 400), so a request sent the instant the agreement appears here can arrive
# inside that window and be refused for an agreement that is about to exist.
# Retrying is safe in exactly this case: a provider that says it has no record
# did not accept, so there is no transfer to duplicate — which is the hazard
# transfer_client.go names when it declines to retry in general. The TCK's own
# harness retries a non-404 4xx on this path three times, which is why the
# suite never sees this.
initiate_transfer() {
	i=0
	while [ "$i" -lt 5 ]; do
		if curl -sf -X POST http://127.0.0.1:9281/transfers/initiate \
			-H "Authorization: Bearer demo-management-token" \
			-H 'Content-Type: application/json' \
			-d "$1" >/dev/null 2>&1; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	echo "the transfer request was refused on every attempt" >&2
	$compose logs >&2
	exit 1
}

echo "==> transfer"
initiate_transfer "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$agreement\",\"format\":\"$format\",\"connectorAddress\":\"$address\"}"

echo "==> waiting for the file"
i=0
downloaded=""
while [ "$i" -lt 40 ]; do
	downloaded=$(find "$gen/consumer-data/downloads" -type f ! -name '.partial-*' 2>/dev/null | head -1 || true)
	[ -n "$downloaded" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$downloaded" ]; then
	echo "no file arrived" >&2
	$compose logs >&2
	exit 1
fi

if ! diff -q "$gen/sample.csv" "$downloaded" >/dev/null; then
	echo "the file that arrived is not the file that was sent" >&2
	diff "$gen/sample.csv" "$downloaded" >&2 || true
	exit 1
fi

echo
echo "moved $(wc -c <"$downloaded" | tr -d ' ') bytes from provider to consumer,"
echo "under a negotiated agreement, between authenticated participants."
echo "  sent:     $gen/sample.csv"
echo "  received: $downloaded"

echo
echo "==> negotiate (resume scenario)"
curl -sf -X POST http://127.0.0.1:9281/negotiations/initiate \
	-H "Authorization: Bearer demo-management-token" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"offerId\":\"$resume_offer\",\"datasetId\":\"urn:dataset:sample-resume\",\"connectorAddress\":\"$address\"}" \
	>/dev/null

echo "==> waiting for the resume-scenario agreement"
resume_agreement=""
i=0
while [ "$i" -lt 40 ]; do
	resume_agreement=$(curl -sf http://127.0.0.1:9281/agreements \
		-H "Authorization: Bearer demo-management-token" 2>/dev/null |
		sed -n 's/.*"agreementId":"\([^"]*\)","datasetId":"urn:dataset:sample-resume".*/\1/p' | head -1 || true)
	[ -n "$resume_agreement" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$resume_agreement" ]; then
	echo "no resume-scenario agreement was concluded" >&2
	$compose logs >&2
	exit 1
fi
echo "    agreement $resume_agreement"

echo "==> transfer (resume scenario)"
initiate_transfer "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$resume_agreement\",\"format\":\"$resume_format\",\"connectorAddress\":\"$address\"}"

echo "==> waiting for the resumed file"
i=0
resume_downloaded=""
while [ "$i" -lt 60 ]; do
	resume_downloaded=$(find "$gen/consumer-data/downloads" -type f ! -name '.partial-*' ! -path "$downloaded" 2>/dev/null | head -1 || true)
	[ -n "$resume_downloaded" ] && break
	i=$((i + 1))
	sleep 1
done
if [ -z "$resume_downloaded" ]; then
	echo "the resumed file never arrived" >&2
	$compose logs >&2
	exit 1
fi

if ! diff -q "$gen/sample-resume.csv" "$resume_downloaded" >/dev/null; then
	echo "the resumed file does not match what was sent" >&2
	diff "$gen/sample-resume.csv" "$resume_downloaded" >&2 || true
	exit 1
fi

if ! $compose logs consumer 2>/dev/null | grep -q "resumed transfer data pull"; then
	echo "the resumed file matched, but the consumer log shows no evidence the resume path actually ran" >&2
	$compose logs consumer >&2
	exit 1
fi

echo
echo "resumed a deliberately interrupted $(wc -c <"$resume_downloaded" | tr -d ' ')-byte transfer"
echo "after a real suspend/restart cycle, and the recovered file matches byte for byte."
