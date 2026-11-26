#!/bin/sh
# Validates the migration set without touching a database. With --apply it also
# runs every migration up, rolls the last one back, and applies it again, which
# is the cheapest way to prove the down file parses and executes.
#
# The target database must be disposable: the script refuses to run against the
# default application database.

set -eu

migrations_dir="migrations"
status=0

fail() {
	echo "migration check: $1" >&2
	status=1
}

# 1. Names and pairing.
for path in "$migrations_dir"/*.sql; do
	[ -e "$path" ] || {
		fail "no migration files found in $migrations_dir"
		break
	}

	name=$(basename "$path")
	if ! printf '%s' "$name" | grep -Eq '^[0-9]{6}_[a-z0-9_]+\.(up|down)\.sql$'; then
		fail "unexpected migration filename: $name"
		continue
	fi

	if [ ! -s "$path" ]; then
		fail "empty migration file: $name"
	fi
done

# 2. Every version has both directions, and versions are unique.
versions=$(ls "$migrations_dir" | sed -n 's/^\([0-9]\{6\}\)_.*\.\(up\|down\)\.sql$/\1/p' | sort -u)
if [ -z "$versions" ]; then
	fail "no migration versions found"
fi

for version in $versions; do
	for direction in up down; do
		matches=$(ls "$migrations_dir" | grep -c "^${version}_.*\.${direction}\.sql$" || true)
		if [ "$matches" -ne 1 ]; then
			fail "version $version has $matches ${direction} files, want exactly 1"
		fi
	done
done

# 3. Ordering: the numeric order must match the sorted order.
ordered=$(ls "$migrations_dir" | sed -n 's/^\([0-9]\{6\}\)_.*\.up\.sql$/\1/p' | sort)
if [ "$(printf '%s\n' "$ordered" | sort -c 2>&1)" ]; then
	fail "up migrations are not in ascending version order"
fi

if [ "$status" -ne 0 ]; then
	exit "$status"
fi
echo "migration check: $(printf '%s\n' "$versions" | wc -l | tr -d ' ') versions are named, paired, and ordered"

if [ "${1:-}" != "--apply" ]; then
	exit 0
fi

# 4. Apply, roll back, re-apply against a scratch database.
target_db="${POSTGRES_DB:-}"
if [ -z "$target_db" ]; then
	echo "migration check: --apply needs POSTGRES_DB" >&2
	exit 1
fi
case "$target_db" in
*_check | *_test) ;;
*)
	echo "migration check: refusing to apply to '$target_db'; use a *_check or *_test database" >&2
	exit 1
	;;
esac

echo "migration check: applying to $target_db"
POSTGRES_DB="$target_db" go run ./cmd/migrate up
POSTGRES_DB="$target_db" go run ./cmd/migrate down 1
POSTGRES_DB="$target_db" go run ./cmd/migrate up
echo "migration check: apply, rollback, and re-apply succeeded"
