#!/usr/bin/env bash
#
# container-smoke.sh — the executable acceptance spec for the Glyphoxa OCI image
# (issue #31, ADR-0034). Runs against an already-built image and asserts the
# image is actually runnable: the CLI works, every native dependency the live
# build links is resolvable inside the container, the bundled ONNX runtime is
# present (so the Silero VAD never reaches the network at start, ADR-0034), and
# the process is a non-root user.
#
# Usage: scripts/container-smoke.sh [IMAGE]
#   IMAGE defaults to "glyphoxa:smoke" (what `make docker-build` tags).
#
# Exit status is 0 only if every assertion passes; the first failure aborts.
set -euo pipefail

IMAGE="${1:-glyphoxa:smoke}"

# Resolve the binary path and onnx-lib path once from the image's own config so
# the assertions don't hard-code a layout the Dockerfile might change later.
BIN_PATH="/usr/local/bin/glyphoxa"

pass=0
fail=0
note() { printf '  -> %s\n' "$*"; }
ok() { printf 'PASS: %s\n' "$1"; pass=$((pass + 1)); }
bad() {
	printf 'FAIL: %s\n' "$1" >&2
	fail=$((fail + 1))
}

# run_in_image runs a command inside a throwaway container of $IMAGE, overriding
# the entrypoint so we can invoke arbitrary shell assertions. Returns the
# command's own exit status.
run_in_image() {
	docker run --rm --network none --entrypoint /bin/sh "$IMAGE" -c "$*"
}

# assert_spa is the embedded-console gate (#114, ADR-0034 amendment "the SPA
# bundle is context-fed"). The image must serve the REAL Vite build at the web
# root, not the committed placeholder index.html. The SPA is go:embed'd INTO the
# binary (internal/spa/dist), so it is not a file on disk to stat — instead we
# grep the binary for the distinguishing bytes:
#   - a real build overwrites index.html to reference a content-hashed bundle
#     (/assets/index-<hash>.js|css), and go:embed bakes those bytes in;
#   - the placeholder is a single <div id="root"> line with NO /assets/.
# The check is two-sided: a hashed asset reference must be PRESENT and the exact
# placeholder one-liner must be ABSENT, so a bundle embedded alongside a stale
# placeholder fails as loudly as a missing one.
assert_spa() {
	printf '[5] embedded web root is the real console, not the placeholder\n'
	if run_in_image "grep -aEq '/assets/index-[A-Za-z0-9_-]+\.js' $BIN_PATH"; then
		ok 'binary embeds a hashed /assets/index-*.js reference (real console)'
	else
		bad 'no hashed /assets/ reference in the binary — embedded web root is the placeholder, not a real console build'
	fi
	if run_in_image "grep -aqF '<!doctype html><html><body><div id=\"root\"></div></body></html>' $BIN_PATH"; then
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
# fixture images without the full native runtime the other checks assert.
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
# 2. ldd on the binary resolves EVERY shared lib — no "not found".
#    libopus and libdave are LINK-time deps (pkg-config opus / dave), so they
#    must appear in the binary's ldd and resolve. libonnxruntime is deliberately
#    NOT here: the Silero VAD dlopen()s it at runtime via
#    ort.SetSharedLibraryPath($GLYPHOXA_ONNX_LIB) — it is never link-time linked,
#    so it correctly does not appear in `ldd glyphoxa`. Its resolvability is
#    asserted separately in step 3 (ldd on the .so itself), which is the check
#    that actually predicts whether the runtime dlopen will succeed.
# ---------------------------------------------------------------------------
printf '[2] ldd on the binary resolves every linked library (no "not found")\n'
ldd_out="$(run_in_image "ldd $BIN_PATH" 2>&1)" || true
note "ldd output:"
printf '%s\n' "$ldd_out" | sed 's/^/      /'
if printf '%s\n' "$ldd_out" | grep -qi 'not found'; then
	bad 'ldd reported an unresolved shared library ("not found")'
else
	ok 'ldd reported no "not found" entries'
fi
for lib in libopus libdave; do
	if printf '%s\n' "$ldd_out" | grep -q "$lib"; then
		ok "ldd links $lib"
	else
		bad "ldd output does not mention $lib (expected a link-time dependency)"
	fi
done

# ---------------------------------------------------------------------------
# 3. The bundled ONNX runtime: $GLYPHOXA_ONNX_LIB is set in the image config (so
#    the VAD short-circuits its download path — ADR-0034: no network fetch at
#    container start), the file exists, AND its OWN shared-lib deps all resolve
#    so the runtime dlopen() won't fail with "not found".
# ---------------------------------------------------------------------------
printf '[3] bundled ONNX runtime is set, present, and itself fully resolvable\n'
onnx_lib="$(run_in_image 'printf "%s" "$GLYPHOXA_ONNX_LIB"')" || true
if [ -z "$onnx_lib" ]; then
	bad 'GLYPHOXA_ONNX_LIB is not set in the image config'
else
	note "GLYPHOXA_ONNX_LIB=$onnx_lib"
	if run_in_image "test -e \"\$GLYPHOXA_ONNX_LIB\""; then
		ok "ONNX runtime exists at \$GLYPHOXA_ONNX_LIB"
	else
		bad "no file at \$GLYPHOXA_ONNX_LIB ($onnx_lib)"
	fi
	onnx_ldd="$(run_in_image 'ldd "$GLYPHOXA_ONNX_LIB"' 2>&1)" || true
	note "ldd \$GLYPHOXA_ONNX_LIB:"
	printf '%s\n' "$onnx_ldd" | sed 's/^/      /'
	if printf '%s\n' "$onnx_ldd" | grep -qi 'not found'; then
		bad 'the bundled libonnxruntime has an unresolved dependency ("not found")'
	else
		ok 'libonnxruntime resolves all its own shared libs'
	fi
fi

# ---------------------------------------------------------------------------
# 4. The process runs as a non-root uid.
# ---------------------------------------------------------------------------
printf '[4] process runs as a non-root uid\n'
uid="$(run_in_image 'id -u')" || true
note "container uid: ${uid:-<unknown>}"
if [ -n "${uid:-}" ] && [ "$uid" -ne 0 ]; then
	ok "runs as non-root uid ($uid)"
else
	bad "runs as root (uid=$uid) — expected a non-root user"
fi

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
		if run_glyphoxa migrate up >/dev/null 2>&1; then
			ok 'migrate up succeeded'
		else
			bad 'migrate up failed'
		fi
		if run_glyphoxa seed -bundle /demo.glyphoxa.json >/dev/null 2>&1; then
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
		if run_glyphoxa seed -bundle /demo.glyphoxa.json >/dev/null 2>&1; then
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
