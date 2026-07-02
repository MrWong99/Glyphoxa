#!/usr/bin/env bash
# Schema-validation gate for the deploy chart (issue #140): render every value
# path the chart supports and kubeconform-validate each against the upstream
# Kubernetes schemas. Shared by `make helm-validate` and ci.yml's helm job.
#
# This replaces the former `helm template | kubeconform` pipelines, which had
# been a dead gate since #42: without pipefail a failed render (missing
# `required` values) left only kubeconform's exit status, and kubeconform exits
# 0 on empty stdin — so the step passed while validating zero manifests. Here
# the render goes to a file first (a render error fails the gate directly) and
# the gate additionally requires a non-zero validated-resource count, so an
# empty render can never pass again. scripts/helm-validate-test.sh pins both
# properties.
set -euo pipefail

cd "$(dirname "$0")/.."

CHART="${HELM_VALIDATE_CHART:-deploy/charts/glyphoxa}"
# The render needs the chart's `required` values; CI dummies live next to the
# chart. Overridable so the self-test can force a failing render.
VALUES="${HELM_VALIDATE_VALUES:-${CHART}/ci/ci-values.yaml}"
KUBERNETES_VERSION="1.30.0"

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT

# validate_path <label> [extra helm args...] — render one value path of the
# chart and schema-validate the result, failing on a render error, an invalid
# manifest, or an empty render.
validate_path() {
  local label="$1"
  shift
  local rendered="${workdir}/${label}.yaml"

  echo "helm-validate: rendering '${label}'"
  helm template glyphoxa "${CHART}" --values "${VALUES}" "$@" >"${rendered}"

  local summary
  if ! summary="$(kubeconform -strict -summary -kubernetes-version "${KUBERNETES_VERSION}" "${rendered}")"; then
    echo "${summary}" >&2
    echo "helm-validate: FAIL (${label}): kubeconform rejected the render" >&2
    exit 1
  fi
  echo "helm-validate: ${label}: ${summary}"

  # kubeconform exits 0 when it parsed nothing, so a successful-but-empty
  # render must be rejected here.
  local valid
  valid="$(sed -n 's/.*Valid: \([0-9][0-9]*\).*/\1/p' <<<"${summary}")"
  if [[ -z "${valid}" || "${valid}" -eq 0 ]]; then
    echo "helm-validate: FAIL (${label}): kubeconform validated 0 resources — empty or unparsed render" >&2
    exit 1
  fi
}

# check_dsn_roundtrip <rendered> <user> <password> — the assembled DSN must
# round-trip the exact credentials Postgres is initialized with (issue #151):
# extract the Secret's database-url from the render, URL-parse it the way pgx
# (Go net/url) does, and compare the decoded userinfo to the originals. Raw
# printf interpolation of a reserved-character password fails here.
check_dsn_roundtrip() {
  local rendered="$1" user="$2" password="$3"
  if ! python3 - "${rendered}" "${user}" "${password}" <<'PY'; then
import re, sys, urllib.parse

rendered, want_user, want_password = sys.argv[1], sys.argv[2], sys.argv[3]
with open(rendered, encoding="utf-8") as f:
    text = f.read()

m = re.search(r'^\s*database-url:\s*"([^"]*)"\s*$', text, re.M)
if not m:
    sys.exit("no database-url found in the render")
dsn = m.group(1)

try:
    url = urllib.parse.urlsplit(dsn)
    # net/url unescapes %XX in userinfo but leaves '+' literal — mirror that
    # (unquote, NOT unquote_plus) so the check matches the real consumer.
    got_user = urllib.parse.unquote(url.username or "")
    got_password = urllib.parse.unquote(url.password or "")
except ValueError as err:
    sys.exit(f"DSN does not URL-parse: {dsn!r}: {err}")

if (got_user, got_password) != (want_user, want_password):
    sys.exit(
        f"DSN does not round-trip the credentials: {dsn!r} "
        f"parses to user={got_user!r} password={got_password!r}, "
        f"want user={want_user!r} password={want_password!r}"
    )
PY
    echo "helm-validate: FAIL (dsn-roundtrip): see above" >&2
    exit 1
  fi
  echo "helm-validate: dsn-roundtrip: DSN round-trips reserved-character credentials"
}

# The two render branches the chart supports: in-cluster Postgres (default)
# and external DB (postgres disabled) — same paths the gate checked before #42
# broke it.
validate_path in-cluster-postgres
validate_path external-db \
  --set postgres.enabled=false \
  --set database.url='postgres://u:p@external.example.com:5432/glyphoxa?sslmode=require'

# Reserved-character credentials (issue #151): the same raw values feed
# POSTGRES_USER/POSTGRES_PASSWORD, so the assembled DSN must percent-encode
# them or the migrate hook and the app parse a different credential than the
# one Postgres was initialized with.
DSN_USER='us@r/n:me?'
DSN_PASSWORD='p@ss/w:rd?'
validate_path dsn-roundtrip \
  --set-string database.user="${DSN_USER}" \
  --set-string database.password="${DSN_PASSWORD}"
check_dsn_roundtrip "${workdir}/dsn-roundtrip.yaml" "${DSN_USER}" "${DSN_PASSWORD}"

echo "helm-validate: OK"
