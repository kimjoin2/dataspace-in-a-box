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
cat >"$gen/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:provider", "public_key": "$provider_pub"},
    {"id": "urn:participant:consumer", "public_key": "$consumer_pub"}
  ]
}
EOF
# roster_signer, not a participant: the roster is the registry itself, and
# signing it with an entry's own key would let that entry vouch for itself.
signature=$("$gen/dsops" roster sign -roster "$gen/roster.json" -key "$gen/operator.key")
cat >"$gen/roster.json" <<EOF
{
  "participants": [
    {"id": "urn:participant:provider", "public_key": "$provider_pub"},
    {"id": "urn:participant:consumer", "public_key": "$consumer_pub"}
  ],
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

# The initiate hooks are on the protocol listener and behind the same
# credential as everything else there (see the connector authentication
# design). An operator driving their own connector therefore presents a
# self-issued one: signed by this connector's key, addressed to itself. It
# verifies because the issuer is in the roster and the audience is this
# connector, which is exactly what the middleware asks.
operator=$("$gen/dsops" token -key "$gen/consumer.key" \
	-iss urn:participant:consumer -aud urn:participant:consumer)

echo "==> negotiate"
# Driven from the host over the published ports: the image is distroless, so
# there is no shell inside to drive it from. connectorAddress is the in-network
# name, because that is the address the connectors use to reach each other.
curl -sf -X POST http://127.0.0.1:9280/2025-1/negotiations/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d '{"providerId":"urn:participant:provider","offerId":"urn:dataset:sample#offer","datasetId":"urn:dataset:sample","connectorAddress":"http://provider:8080/2025-1"}' \
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

echo "==> transfer"
curl -sf -X POST http://127.0.0.1:9280/2025-1/transfers/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$agreement\",\"format\":\"HTTP-PULL\",\"connectorAddress\":\"http://provider:8080/2025-1\"}" \
	>/dev/null

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
curl -sf -X POST http://127.0.0.1:9280/2025-1/negotiations/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d '{"providerId":"urn:participant:provider","offerId":"urn:dataset:sample-resume#offer","datasetId":"urn:dataset:sample-resume","connectorAddress":"http://provider:8080/2025-1"}' \
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
curl -sf -X POST http://127.0.0.1:9280/2025-1/transfers/initiate \
	-H "Authorization: Bearer $operator" \
	-H 'Content-Type: application/json' \
	-d "{\"providerId\":\"urn:participant:provider\",\"agreementId\":\"$resume_agreement\",\"format\":\"HTTP-PULL\",\"connectorAddress\":\"http://provider:8080/2025-1\"}" \
	>/dev/null

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
