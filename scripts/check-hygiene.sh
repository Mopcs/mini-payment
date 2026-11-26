#!/bin/sh
# Fails when the repository tracks files that should never be committed: real
# environment files, private keys, or build output. Only file names are printed,
# never file contents.

set -eu

status=0
tracked=$(git ls-files)

fail() {
	echo "hygiene check: $1" >&2
	status=1
}

report() {
	echo "$1" | while IFS= read -r path; do
		[ -n "$path" ] || continue
		echo "  $path" >&2
	done
}

# 1. Real environment files. .env.example is documentation and is allowed.
env_files=$(echo "$tracked" | grep -E '(^|/)\.env$|(^|/)\.env\.(local|dev|development|prod|production|staging)$' || true)
if [ -n "$env_files" ]; then
	fail "tracked environment files:"
	report "$env_files"
fi

# 2. Private keys and certificate bundles.
key_files=$(echo "$tracked" | grep -Ei '\.(pem|key|p12|pfx|jks|keystore)$|(^|/)id_(rsa|dsa|ecdsa|ed25519)$' || true)
if [ -n "$key_files" ]; then
	fail "tracked key material:"
	report "$key_files"
fi

# 3. Build output that belongs in an artefact store, not in Git.
binary_files=$(echo "$tracked" | grep -Ei '\.(exe|dll|so|dylib|test|out)$|(^|/)(bin|build)/' || true)
if [ -n "$binary_files" ]; then
	fail "tracked build output:"
	report "$binary_files"
fi

# 4. .env must stay ignored, otherwise the next commit can leak a secret.
if ! git check-ignore -q .env; then
	fail ".env is not ignored by Git"
fi

if [ "$status" -ne 0 ]; then
	exit "$status"
fi
echo "hygiene check: no tracked secrets, keys, or build output"
