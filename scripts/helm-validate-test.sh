#!/usr/bin/env bash
# Self-test for scripts/helm-validate.sh (issue #140): the gate must actually
# gate. The pre-#140 pipeline (`helm template | kubeconform`, no pipefail) had
# been green while validating zero manifests since #42, because a failed render
# left only kubeconform's exit status and kubeconform exits 0 on empty stdin.
# Each case here pins one property whose loss re-opens that hole.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "helm-validate-test: [1/4] gate passes on the real chart with the CI values"
scripts/helm-validate.sh

echo "helm-validate-test: [2/4] gate fails when the render fails (missing required values)"
if HELM_VALIDATE_VALUES=/dev/null scripts/helm-validate.sh >/dev/null 2>&1; then
  echo "helm-validate-test: FAIL — gate exited 0 although the render errored" >&2
  exit 1
fi

echo "helm-validate-test: [3/4] gate fails when the render is empty (0 resources)"
if HELM_VALIDATE_CHART=scripts/testdata/empty-chart HELM_VALIDATE_VALUES=/dev/null \
  scripts/helm-validate.sh >/dev/null 2>&1; then
  echo "helm-validate-test: FAIL — gate exited 0 although kubeconform saw no resources" >&2
  exit 1
fi

echo "helm-validate-test: [4/4] gate fails when the DSN interpolates credentials without URL-escaping (#151)"
if HELM_VALIDATE_CHART=scripts/testdata/unescaped-dsn-chart HELM_VALIDATE_VALUES=/dev/null \
  scripts/helm-validate.sh >/dev/null 2>&1; then
  echo "helm-validate-test: FAIL — gate exited 0 although the DSN does not round-trip a reserved-character password" >&2
  exit 1
fi

echo "helm-validate-test: OK"
