#!/usr/bin/env bash
#
# container-smoke.sh — the executable acceptance spec for the Glyphoxa OCI image
# (issue #31, ADR-0034; scratch image since #468). Runs against an already-built
# image and asserts the image is actually runnable AND minimal: the CLI works,
# the binary is fully static (issue #468's acceptance criterion, kept through
# the libopus encoder revert via a static CGO link — ADR-0034 amendment
# 2026-07-19), the image is genuinely scratch (no shell), CA certificates are
# present for outbound TLS, and the process is a non-root user.
#
# The image has no shell, so assertions that used to run inside the container
# now run on the HOST against the binary extracted via `docker cp`.
#
# Usage: scripts/container-smoke.sh [IMAGE]
#   IMAGE defaults to "glyphoxa:smoke" (what `make docker-build` tags).
#
# Exit status is 0 only if every assertion passes; the first failure aborts.
set -euo pipefail

IMAGE="${1:-glyphoxa:smoke}"

BIN_PATH="/usr/local/bin/glyphoxa"

pass=0
fail=0
note() { printf '  -> %s\n' "$*"; }
ok() { printf 'PASS: %s\n' "$1"; pass=$((pass + 1)); }
bad() {
	printf 'FAIL: %s\n' "$1" >&2
	fail=$((fail + 1))
}

# Extraction scratchpad: a throwaway container of $IMAGE we `docker cp` from.
# Populated lazily by extract_from_image; removed by the EXIT trap.
EXTRACT_DIR="$(mktemp -d)"
EXTRACT_CTR=""
extract_cleanup() {
	[ -n "$EXTRACT_CTR" ] && docker rm -f "$EXTRACT_CTR" >/dev/null 2>&1 || true
	rm -rf "$EXTRACT_DIR"
}
trap extract_cleanup EXIT

# extract_from_image SRC DST copies a path out of the (never-started) container
# onto the host. Works for scratch images — no shell needed. Returns non-zero
# if the path does not exist in the image.
extract_from_image() {
	if [ -z "$EXTRACT_CTR" ]; then
		EXTRACT_CTR="$(docker create "$IMAGE")"
	fi
	docker cp "$EXTRACT_CTR:$1" "$2" >/dev/null 2>&1
}

# assert_spa is the embedded-console gate (#114, ADR-0034 amendment "the SPA
# bundle is context-fed"). The image must serve the REAL Vite build at the web
# root, not the committed placeholder index.html. The SPA is go:embed'd INTO the
# binary (internal/spa/dist), so we grep the extracted binary for the
# distinguishing bytes:
#   - a real build overwrites index.html to reference a content-hashed bundle
#     (/assets/index-<hash>.js|css), and go:embed bakes those bytes in;
#   - the placeholder is a single <div id="root"> line with NO /assets/.
# The check is two-sided: a hashed asset reference must be PRESENT and the exact
# placeholder one-liner must be ABSENT, so a bundle embedded alongside a stale
# placeholder fails as loudly as a missing one.
assert_spa() {
	printf '[5] embedded web root is the real console, not the placeholder\n'
	local bin="$EXTRACT_DIR/glyphoxa-spa"
	if ! extract_from_image "$BIN_PATH" "$bin"; then
		bad "could not extract $BIN_PATH from the image"
		return
	fi
	if grep -aEq '/assets/index-[A-Za-z0-9_-]+\.js' "$bin"; then
		ok 'binary embeds a hashed /assets/index-*.js reference (real console)'
	else
		bad 'no hashed /assets/ reference in the binary — embedded web root is the placeholder, not a real console build'
	fi
	if grep -aqF '<!doctype html><html><body><div id="root"></div></body></html>' "$bin"; then
		bad 'binary still contains the committed placeholder index.html one-liner (a real build must overwrite it)'
	else
		ok 'committed placeholder index.html one-liner is absent'
	fi
}

# summary prints the pass/fail tally and exits: non-zero if any assertion failed.
summary() {
	printf '\n== summary: %d passed, %d failed ==\n' "$pass" "$fail"
	if [ "$fail" -ne 0 ]; then
		exit 1
	fi
	printf 'all container smoke assertions passed\n'
	exit 0
}

printf '== Glyphoxa container smoke test ==\n'
printf 'image: %s\n\n' "$IMAGE"

# Fail fast with a clear message if the image was never built.
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
	printf 'FAIL: image %q does not exist — build it first (make docker-build / docker build -t %q .)\n' "$IMAGE" "$IMAGE" >&2
	exit 1
fi

# SMOKE_ONLY=spa runs ONLY the embedded-console gate and exits. scripts/
# container-smoke-test.sh uses this to point the gate at tiny placeholder/real
# fixture images without a full image build.
if [ "${SMOKE_ONLY:-}" = "spa" ]; then
	assert_spa
	summary
fi

# ---------------------------------------------------------------------------
# 1. `glyphoxa migrate --help` exits 0 (issue #31 acceptance criterion).
#    The entrypoint is the binary, so we invoke the subcommand via run args, and
#    --network none proves the help path needs no network. RunMigrate
#    (cmd/glyphoxa/migrate.go) short-circuits --help BEFORE its $GLYPHOXA_DATABASE_URL
#    check, so this succeeds in a fresh DB-less container.
# ---------------------------------------------------------------------------
printf '[1] glyphoxa migrate --help exits 0\n'
if docker run --rm --network none "$IMAGE" migrate --help >/dev/null 2>&1; then
	ok 'migrate --help exited 0'
else
	bad "migrate --help did not exit 0 (got $?)"
fi

# ---------------------------------------------------------------------------
# 2. The binary is fully static (issue #468 acceptance criterion): ldd on the
#    extracted binary must report "not a dynamic executable". A static binary
#    is the load-bearing property that lets the runtime stage be scratch. CGO
#    is ON again for the libopus outbound encoder (ADR-0034 amendment
#    2026-07-19), but libopus/libc are linked statically — so the assertion is
#    unchanged, and any dynamically-linked native binding fails here first.
# ---------------------------------------------------------------------------
printf '[2] the binary is statically linked (ldd: "not a dynamic executable")\n'
BIN_LOCAL="$EXTRACT_DIR/glyphoxa"
if ! extract_from_image "$BIN_PATH" "$BIN_LOCAL"; then
	bad "could not extract $BIN_PATH from the image"
else
	ldd_out="$(ldd "$BIN_LOCAL" 2>&1)" || true
	note "ldd output:"
	printf '%s\n' "$ldd_out" | sed 's/^/      /'
	if printf '%s\n' "$ldd_out" | grep -qi 'not a dynamic executable'; then
		ok 'ldd reports "not a dynamic executable" (fully static)'
	else
		bad 'binary is dynamically linked — the static-link property (#468, kept through the libopus encoder revert) regressed'
	fi
fi

# ---------------------------------------------------------------------------
# 3. The image is genuinely scratch-minimal: no shell to run (defense in
#    depth — nothing to pivot to inside the container), and the CA bundle is
#    present so outbound TLS to the providers works.
# ---------------------------------------------------------------------------
printf '[3] scratch minimalism: no shell, CA certificates present\n'
if docker run --rm --network none --entrypoint /bin/sh "$IMAGE" -c 'true' >/dev/null 2>&1; then
	bad 'image contains /bin/sh — expected a scratch runtime stage with no shell'
else
	ok 'image has no /bin/sh (scratch runtime)'
fi
if extract_from_image /etc/ssl/certs/ca-certificates.crt "$EXTRACT_DIR/ca-certificates.crt"; then
	ok 'CA bundle present at /etc/ssl/certs/ca-certificates.crt'
else
	bad 'no CA bundle at /etc/ssl/certs/ca-certificates.crt — outbound TLS to providers would fail'
fi

# ---------------------------------------------------------------------------
# 4. The process runs as a non-root uid. The image has no `id` to exec, so
#    assert the image CONFIG: a numeric non-zero USER is what the kubelet and
#    dockerd enforce at start.
# ---------------------------------------------------------------------------
printf '[4] image config sets a non-root user\n'
img_user="$(docker image inspect --format '{{.Config.User}}' "$IMAGE")" || true
note "image config User: ${img_user:-<unset>}"
case "${img_user%%:*}" in
'' | 0 | root)
	bad "image runs as root (User=${img_user:-<unset>}) — expected a non-root numeric user"
	;;
*)
	ok "image config sets non-root user ($img_user)"
	;;
esac

# ---------------------------------------------------------------------------
# 5. The embedded web root is the REAL console build, not the placeholder (#114).
# ---------------------------------------------------------------------------
assert_spa

# ---------------------------------------------------------------------------
# 6. `glyphoxa seed -bundle` against a real Postgres (#293): the canonical demo
#    bundle (scripts/testdata/demo.glyphoxa.json) imports cleanly and
#    idempotently, so the self-host "seed a playable campaign" story actually
#    works end-to-end through the shipped image. Unlike the DB-less checks above
#    this needs a database, so it stands up a throwaway pgvector Postgres on a
#    SCOPED docker network (NOT --network none — the seed container must reach
#    the DB by name) and runs the image against it. Assertions go through
#    `docker exec … psql` so the host needs no psql client. All state is torn
#    down by a trap on exit.
# ---------------------------------------------------------------------------
printf '[6] seed -bundle imports the demo bundle idempotently against Postgres\n'

SMOKE_NET="glyphoxa-smoke-net-$$"
PG_NAME="glyphoxa-smoke-pg-$$"
PG_IMAGE="pgvector/pgvector:pg17"
DB_URL="postgres://glyphoxa:glyphoxa@${PG_NAME}:5432/glyphoxa?sslmode=disable"
TESTDATA_DIR="$(cd "$(dirname "$0")/testdata" && pwd)"

seed_cleanup() {
	docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
	docker network rm "$SMOKE_NET" >/dev/null 2>&1 || true
	extract_cleanup
}
trap seed_cleanup EXIT

# psql_count runs a scalar COUNT query inside the DB container and trims whitespace.
psql_count() {
	docker exec "$PG_NAME" psql -U glyphoxa -d glyphoxa -tAc "$1" 2>/dev/null | tr -d '[:space:]'
}
# run_glyphoxa runs the image on the scoped network with the DSN env and the demo
# bundle bind-mounted read-only at /demo.glyphoxa.json.
run_glyphoxa() {
	docker run --rm --network "$SMOKE_NET" \
		-e GLYPHOXA_DATABASE_URL="$DB_URL" \
		-v "$TESTDATA_DIR/demo.glyphoxa.json:/demo.glyphoxa.json:ro" \
		"$IMAGE" "$@"
}
# run_step runs the image capturing its COMBINED output; on failure it echoes that
# output (indented) so a CI failure here is diagnosable instead of a bare non-zero
# exit swallowed by >/dev/null. Returns the command's own status for the caller's
# if/else so set -e never aborts the section mid-way.
run_step() {
	local label="$1"
	shift
	local out rc
	if out="$(run_glyphoxa "$@" 2>&1)"; then
		return 0
	fi
	rc=$?
	note "$label output (exit $rc):"
	printf '%s\n' "$out" | sed 's/^/      /'
	return "$rc"
}

if ! docker network create "$SMOKE_NET" >/dev/null 2>&1; then
	bad "could not create scoped docker network $SMOKE_NET"
elif ! docker run -d --name "$PG_NAME" --network "$SMOKE_NET" \
	-e POSTGRES_USER=glyphoxa -e POSTGRES_PASSWORD=glyphoxa -e POSTGRES_DB=glyphoxa \
	"$PG_IMAGE" >/dev/null 2>&1; then
	bad "could not start throwaway Postgres ($PG_IMAGE)"
else
	ready=0
	for _ in $(seq 1 30); do
		if docker exec "$PG_NAME" pg_isready -U glyphoxa -d glyphoxa >/dev/null 2>&1; then
			ready=1
			break
		fi
		sleep 1
	done
	if [ "$ready" -ne 1 ]; then
		bad 'throwaway Postgres never became ready'
	else
		if run_step 'migrate up' migrate up; then
			ok 'migrate up succeeded'
		else
			bad 'migrate up failed'
		fi
		if run_step 'seed -bundle' seed -bundle /demo.glyphoxa.json; then
			ok 'seed -bundle succeeded'
		else
			bad 'seed -bundle failed'
		fi
		camp="$(psql_count 'SELECT count(*) FROM campaign;')"
		agents="$(psql_count 'SELECT count(*) FROM agents;')"
		if [ "$camp" = "1" ]; then
			ok 'exactly one campaign after seed'
		else
			bad "campaign count = ${camp:-<none>}, want 1"
		fi
		if [ "$agents" = "2" ]; then
			ok 'exactly two agents (Butler + Bart)'
		else
			bad "agent count = ${agents:-<none>}, want 2"
		fi
		# Idempotence (ADR-0053 §4): re-running the seed hits the name precheck and
		# skips, so the campaign count must stay at 1 (no always-mint duplicate).
		if run_step 're-run seed -bundle' seed -bundle /demo.glyphoxa.json; then
			ok 're-run seed -bundle exited 0'
		else
			bad 're-run seed -bundle failed'
		fi
		camp2="$(psql_count 'SELECT count(*) FROM campaign;')"
		if [ "$camp2" = "1" ]; then
			ok 'still one campaign after re-run (idempotent)'
		else
			bad "campaign count after re-run = ${camp2:-<none>}, want 1"
		fi
	fi
fi

# ---------------------------------------------------------------------------
# Summary.
# ---------------------------------------------------------------------------
summary
