#!/bin/sh
# Replaces CLIENT_API_KEY and TARGET_TOKEN in .env with freshly generated
# random tokens, keeping every other line (e.g. CLIENT_PORT) as it is.
set -eu
cd "$(dirname "$0")/.."

rand() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex 32
	else
		head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
	fi
}

touch .env
tmp=$(mktemp)
grep -v -e '^CLIENT_API_KEY=' -e '^TARGET_TOKEN=' .env > "$tmp" || true
{
	cat "$tmp"
	echo "CLIENT_API_KEY=$(rand)"
	echo "TARGET_TOKEN=$(rand)"
} > .env
rm -f "$tmp"
echo "Wrote new tokens to .env — run 'make up' to restart the services with them."
