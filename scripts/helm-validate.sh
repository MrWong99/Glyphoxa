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

# validate_dsn_roundtrip <user> <password> — the assembled DSN must round-trip
# the exact credentials Postgres is initialized with (issue #151): render with
# the given credentials, schema-validate the render, extract the Secret's
# database-url, URL-parse it the way pgx (Go net/url) does, and compare the
# decoded userinfo to the originals. Raw printf interpolation of a
# reserved-character password fails here.
validate_dsn_roundtrip() {
  local user="$1" password="$2"
  local rendered="${workdir}/dsn-roundtrip.yaml"

  validate_path dsn-roundtrip \
    --set-string database.user="${user}" \
    --set-string database.password="${password}"

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
# broke it. Both render the Web Instance (#118, enabled by default) + the voice
# Deployment, so the new web Deployment + Service are schema-validated on both.
validate_path in-cluster-postgres
validate_path external-db \
  --set postgres.enabled=false \
  --set database.url='postgres://u:p@external.example.com:5432/glyphoxa?sslmode=require'

# The Web-Instance-disabled render path (#118 AC): the chart must still render
# (and validate) with web.enabled=false — a voice-only / DB-prep install that
# needs none of the web OAuth credentials.
validate_path web-disabled --set web.enabled=false

# The Ingress render paths (#121 AC): enabled with an externally supplied TLS
# Secret (cert-manager off) and enabled with the cert-manager cluster-issuer
# path must each produce a schema-valid networking.k8s.io/v1 Ingress. Only a
# host is needed to enable; nulling the ci-values web.oauth.redirectUrl exercises
# the derive-from-host branch (the redirect URL is derived when no explicit value
# is set) end to end through the render gate.
validate_path ingress-external-tls \
  --set ingress.enabled=true \
  --set ingress.host=glyphoxa.example.com \
  --set ingress.tls.secretName=glyphoxa-web-tls \
  --set web.oauth.redirectUrl=null
validate_path ingress-cert-manager \
  --set ingress.enabled=true \
  --set ingress.host=glyphoxa.example.com \
  --set ingress.certManager.enabled=true \
  --set ingress.certManager.clusterIssuer=letsencrypt-prod \
  --set web.oauth.redirectUrl=null

# Reserved-character credentials (issue #151): the same raw values feed
# POSTGRES_USER/POSTGRES_PASSWORD, so the assembled DSN must percent-encode
# them or the migrate hook and the app parse a different credential than the
# one Postgres was initialized with. The set includes an interior space AND a
# literal '+': QueryEscape (sprig urlquery) encodes a space as '+', but
# net/url userinfo decoding keeps '+' literal — the one character class where
# the two disagree, so bare urlquery alone fails here too. Obviously-dummy
# values, never deployed.
validate_dsn_roundtrip 'us@r/n: m+e?' 'dummy-p@ss/w: r+d?'

echo "helm-validate: OK"
