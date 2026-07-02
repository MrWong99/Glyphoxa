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

# The two render branches the chart supports: in-cluster Postgres (default)
# and external DB (postgres disabled) — same paths the gate checked before #42
# broke it.
validate_path in-cluster-postgres
validate_path external-db \
  --set postgres.enabled=false \
  --set database.url='postgres://u:p@external.example.com:5432/glyphoxa?sslmode=require'

echo "helm-validate: OK"
