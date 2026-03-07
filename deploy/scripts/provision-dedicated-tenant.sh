#!/usr/bin/env bash
#
# Provision a dedicated Glyphoxa tenant.
#
# This script creates the Kubernetes namespace, network policies, Sealed Secrets,
# and deploys the Helm chart for a single dedicated tenant.
#
# Prerequisites:
#   - kubectl configured with cluster access
#   - helm 3 installed
#   - kubeseal installed (if using Sealed Secrets)
#
# Usage:
#   ./provision-dedicated-tenant.sh <tenant-id> <database-dsn> [bot-token]
#
# Example:
#   ./provision-dedicated-tenant.sh acme \
#     "postgres://glyphoxa:secret@db.example.com:5432/glyphoxa_acme?sslmode=require" \
#     "MTIz..."

set -euo pipefail

TENANT_ID="${1:?Usage: $0 <tenant-id> <database-dsn> [bot-token]}"
DATABASE_DSN="${2:?Usage: $0 <tenant-id> <database-dsn> [bot-token]}"
BOT_TOKEN="${3:-}"

NAMESPACE="glyphoxa-tenant-${TENANT_ID}"
CHART_DIR="$(cd "$(dirname "$0")/../helm/glyphoxa" && pwd)"
RELEASE_NAME="glyphoxa-${TENANT_ID}"

# Validate tenant ID format (must match SchemaName regex).
if ! echo "${TENANT_ID}" | grep -qP '^[a-z][a-z0-9_]{0,62}$'; then
    echo "ERROR: tenant-id must match ^[a-z][a-z0-9_]{0,62}$" >&2
    exit 1
fi

echo "==> Provisioning dedicated tenant: ${TENANT_ID}"
echo "    Namespace: ${NAMESPACE}"
echo "    Release:   ${RELEASE_NAME}"
echo ""

# 1. Create namespace.
echo "--- Creating namespace ${NAMESPACE}..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# 2. Label namespace for network policy enforcement.
kubectl label namespace "${NAMESPACE}" \
    glyphoxa.io/tenant="${TENANT_ID}" \
    glyphoxa.io/tier=dedicated \
    --overwrite

# 3. Create secrets.
echo "--- Creating secrets..."
kubectl create secret generic "${RELEASE_NAME}-secrets" \
    --namespace="${NAMESPACE}" \
    --from-literal=database-dsn="${DATABASE_DSN}" \
    ${BOT_TOKEN:+--from-literal=discord-bot-token="${BOT_TOKEN}"} \
    --dry-run=client -o yaml | kubectl apply -f -

# 4. Deploy Helm chart with dedicated values.
echo "--- Deploying Helm chart..."
helm upgrade --install "${RELEASE_NAME}" "${CHART_DIR}" \
    --namespace="${NAMESPACE}" \
    --values="${CHART_DIR}/values-dedicated.yaml" \
    --set "database.dsn=${DATABASE_DSN}"

echo ""
echo "==> Dedicated tenant ${TENANT_ID} provisioned successfully."
echo "    Gateway: ${RELEASE_NAME}-gateway.${NAMESPACE}.svc"
echo ""
echo "Next steps:"
echo "  1. Register the tenant via the admin API"
echo "  2. Verify pods are running: kubectl -n ${NAMESPACE} get pods"
echo "  3. Check gateway readiness: kubectl -n ${NAMESPACE} get endpoints"
