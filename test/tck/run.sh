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

$compose up -d --build dsbox

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

# --use-aliases: `compose run` does not register the service's own name as a
# network alias by default (only `up` does), so without this flag the
# connector's callback pushes to http://tck:8083 fail DNS resolution with
# "no such host" the moment any test (first hit: CN:02-01) needs one.
$compose run --rm --use-aliases tck >"$output" 2>&1 || true
echo "TCK output written to $output"
echo "connector log will be written to $connector_log"
