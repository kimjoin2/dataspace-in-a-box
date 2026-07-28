#!/bin/sh
# Brings up the connector, runs the TCK against it, captures stdout, tears down.
# Always exits 0 when the TCK ran to completion: judging the output is the
# gate's job, not this script's.
set -eu

dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$dir/../.." && pwd)
compose="docker compose -f $dir/compose.yaml"
output="$root/tck-output.txt"

cleanup() {
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

$compose run --rm tck >"$output" 2>&1 || true
echo "TCK output written to $output"
