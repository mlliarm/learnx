#!/usr/bin/env bash
# Records the README GIFs with VHS (in Docker) from docs/demo/*.tape.
#
#   docs/demo/record.sh [tape...]
#
# The courses shown come from docs/demo/data (generated syllabi and lessons,
# with hints, as JSON that learnx imports on first open); set DEMO_HOME to use
# another learnx data directory. Either is copied, so recording never changes
# it, and no LLM is called. The tapes answer the fixture's exercises, so they
# need updating if the fixture changes. Exercises run against the
# learnx-elasticsearch sandbox container (start it once with
# `learnx practice elastic-security`), reached over host networking.
set -euo pipefail

cd "$(dirname "$0")/../.."
DEMO_HOME="${DEMO_HOME:-docs/demo/data}"
VHS_IMAGE=ghcr.io/charmbracelet/vhs:v0.12.1
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/bin"
CGO_ENABLED=0 go build -o "$work/bin/learnx" ./cmd/learnx
es_port="$(docker port learnx-elasticsearch 9200/tcp | head -1 | sed 's/.*://')"

tapes=("$@")
[ ${#tapes[@]} -gt 0 ] || tapes=(docs/demo/*.tape)
for tape in "${tapes[@]}"; do
  rm -rf "$work/home" && cp -r "$DEMO_HOME" "$work/home"
  echo "recording $tape"
  docker run --rm --network host \
    -v "$PWD:/vhs" -v "$work:/demo" \
    -e PATH="/demo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    -e LEARNX_HOME=/demo/home \
    -e LEARNX_ES_URL="http://127.0.0.1:$es_port" \
    -e COLORTERM=truecolor \
    --user "$(id -u):$(id -g)" -e HOME=/tmp \
    "$VHS_IMAGE" "$tape"
done
