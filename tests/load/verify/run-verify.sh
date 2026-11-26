#!/bin/sh
# Run the read-only financial invariant checks and fail when any of them finds a
# violation.
#
# Connection order:
#   1. VERIFY_DATABASE_URL, when set
#   2. VERIFY_DOCKER_CONTAINER, when set - a running postgres container, which is
#      how you reach a stack that does not publish the database port
#   3. the postgres service of the local Compose stack
#
# Nothing here writes to the database.

set -eu

here=$(cd "$(dirname "$0")" && pwd)
pending_age_seconds="${PENDING_AGE_SECONDS:-60}"

case "$pending_age_seconds" in
'' | *[!0-9]*)
	echo "run-verify: PENDING_AGE_SECONDS must be a whole number of seconds" >&2
	exit 2
	;;
esac

if [ -n "${VERIFY_DATABASE_URL:-}" ]; then
	connection="psql \"\$VERIFY_DATABASE_URL\""
	query() {
		psql "$VERIFY_DATABASE_URL" \
			-v ON_ERROR_STOP=1 \
			-v pending_age_seconds="$pending_age_seconds" \
			-A -F'|' -t -f "$here/invariants.sql"
	}
elif [ -n "${VERIFY_DOCKER_CONTAINER:-}" ]; then
	user="${POSTGRES_USER:-payment_user}"
	database="${POSTGRES_DB:-payment_db}"
	connection="docker exec $VERIFY_DOCKER_CONTAINER psql -U $user -d $database"
	query() {
		docker exec -i "$VERIFY_DOCKER_CONTAINER" psql \
			-U "$user" -d "$database" \
			-v ON_ERROR_STOP=1 \
			-v pending_age_seconds="$pending_age_seconds" \
			-A -F'|' -t -f - <"$here/invariants.sql"
	}
else
	user="${POSTGRES_USER:-payment_user}"
	database="${POSTGRES_DB:-payment_db}"
	connection="docker compose exec postgres psql -U $user -d $database"
	query() {
		docker compose exec -T postgres psql \
			-U "$user" -d "$database" \
			-v ON_ERROR_STOP=1 \
			-v pending_age_seconds="$pending_age_seconds" \
			-A -F'|' -t -f - <"$here/invariants.sql"
	}
fi

echo "verifying financial invariants against: $connection"
echo "unpublished or unconsumed events older than ${pending_age_seconds}s count as violations"
echo

results=$(mktemp)
trap 'rm -f "$results"' EXIT

if ! query >"$results"; then
	echo "run-verify: the checks could not run; see the error above" >&2
	exit 2
fi

status=0
total=0
while IFS='|' read -r check_name violations; do
	[ -n "$check_name" ] || continue
	total=$((total + 1))
	if [ "$violations" = "0" ]; then
		printf '  ok    %s\n' "$check_name"
	else
		printf '  FAIL  %s: %s\n' "$check_name" "$violations"
		status=1
	fi
done <"$results"

echo
if [ "$status" -ne 0 ]; then
	echo "financial invariants: FAILED"
	exit 1
fi

echo "financial invariants: $total checks passed"
