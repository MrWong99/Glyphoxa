#!/usr/bin/env bash
# Self-test for the embedded-console gate in scripts/container-smoke.sh (#114):
# the gate must actually gate. It has to FAIL when an image's embedded web root
# is the committed placeholder index.html and PASS only when it is a real Vite
# build. This is the same "prove the gate isn't a silent no-op" discipline the
# helm-validate self-test enforces after #140.
#
# The real gate greps the binary for the bytes a Vite build bakes in, so we
# exercise it against two tiny throwaway images whose only payload is a fake
# /usr/local/bin/glyphoxa carrying the distinguishing bytes — no multi-minute
# native image build needed. SMOKE_ONLY=spa runs only the embedded-console
# assertion against them.
set -euo pipefail

cd "$(dirname "$0")/.."

PLACEHOLDER_IMG="glyphoxa-smoke-selftest-placeholder"
REAL_IMG="glyphoxa-smoke-selftest-real"

cleanup() {
	docker rmi -f "$PLACEHOLDER_IMG" "$REAL_IMG" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# build_fixture TAG CONTENT builds an image whose /usr/local/bin/glyphoxa is a
# fake "binary" holding exactly CONTENT. `docker build -` takes the Dockerfile
# from stdin with an empty context (no repo files shipped), so this is fast.
build_fixture() {
	local tag="$1" content="$2"
	docker build -q -t "$tag" - >/dev/null <<DOCKERFILE
FROM busybox
RUN mkdir -p /usr/local/bin && printf '%s' '${content}' > /usr/local/bin/glyphoxa
DOCKERFILE
}

# The committed placeholder index.html, verbatim (internal/spa/dist/index.html).
PLACEHOLDER='<!doctype html><html><body><div id="root"></div></body></html>'
# A representative slice of a real Vite index.html: a content-hashed bundle ref.
REAL='<script type="module" crossorigin src="/assets/index-a1b2c3d4.js"></script>'

echo "container-smoke-test: [1/2] gate FAILS on a placeholder-only embedded web root"
build_fixture "$PLACEHOLDER_IMG" "$PLACEHOLDER"
if SMOKE_ONLY=spa ./scripts/container-smoke.sh "$PLACEHOLDER_IMG" >/dev/null 2>&1; then
	echo "container-smoke-test: FAIL — the SPA gate passed on the placeholder image" >&2
	exit 1
fi

echo "container-smoke-test: [2/2] gate PASSES on a real Vite-built embedded web root"
build_fixture "$REAL_IMG" "$REAL"
if ! SMOKE_ONLY=spa ./scripts/container-smoke.sh "$REAL_IMG" >/dev/null 2>&1; then
	echo "container-smoke-test: FAIL — the SPA gate failed on a real console image" >&2
	exit 1
fi

echo "container-smoke-test: OK"
