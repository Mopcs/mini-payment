#!/bin/sh
# Run one load test scenario in the pinned k6 container.
#
# The container gets its configuration from tests/load/.env.k6, which is ignored
# by Git. Copy .env.k6.example and fill it in first.

set -eu

here=$(cd "$(dirname "$0")" && pwd)

scenario="${1:-}"
case "$scenario" in
baseline | transfers | idempotency | stress) ;;
*)
	echo "usage: $0 <baseline|transfers|idempotency|stress>" >&2
	exit 2
	;;
esac

env_file="${LOAD_ENV_FILE:-$here/.env.k6}"
if [ ! -f "$env_file" ]; then
	echo "run: $env_file is missing; copy .env.k6.example and fill it in" >&2
	exit 2
fi

# Docker Desktop publishes the host as host.docker.internal, which is the
# default in .env.k6.example. On Linux, set K6_DOCKER_NETWORK=host and point
# BASE_URL at 127.0.0.1 instead.
network_args=""
if [ -n "${K6_DOCKER_NETWORK:-}" ]; then
	network_args="--network ${K6_DOCKER_NETWORK}"
fi

mkdir -p "$here/results"

# Docker Desktop needs a Windows path for the bind source: a /c/... path is
# rewritten into something it silently mounts as an empty directory. pwd -W
# gives C:/... on Git Bash and fails on Linux, where pwd is already correct.
mount_source=$(cd "$here" && (pwd -W 2>/dev/null || pwd))

# The double slash in "//scripts" survives the path rewriting that Git Bash
# applies to arguments, so the container still sees the mount point as /scripts.
# shellcheck disable=SC2086
docker run --rm -i \
	$network_args \
	--env-file "$env_file" \
	-e LOAD_TEST_COMMAND="k6 run scenarios/$scenario.js" \
	-v "${mount_source}:/scripts" \
	-w //scripts \
	"${K6_IMAGE:-grafana/k6:2.4.0}" run "scenarios/$scenario.js"
