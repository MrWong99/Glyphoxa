#!/usr/bin/env bash
# vault-setup.sh — Configure Vault Transit + PKI for Glyphoxa.
#
# Prerequisites:
#   - vault CLI installed and VAULT_ADDR / VAULT_TOKEN set
#   - Vault unsealed and reachable
#
# Usage:
#   export VAULT_ADDR=https://vault.openclaw.lan
#   export VAULT_TOKEN=<root-or-admin-token>
#   ./scripts/vault-setup.sh
#
# This script is idempotent — safe to run multiple times.

set -euo pipefail

echo "=== Glyphoxa Vault Setup ==="
echo "VAULT_ADDR=${VAULT_ADDR:?'VAULT_ADDR must be set'}"
vault status >/dev/null 2>&1 || { echo "ERROR: cannot reach Vault at $VAULT_ADDR"; exit 1; }

# ── Transit Secrets Engine ───────────────────────────────────────────────────
echo ""
echo "--- Transit Secrets Engine ---"

if vault secrets list -format=json | grep -q '"transit/"'; then
    echo "Transit engine already enabled."
else
    vault secrets enable transit
    echo "Transit engine enabled."
fi

# Create the encryption key for bot tokens.
if vault read transit/keys/glyphoxa-bot-tokens >/dev/null 2>&1; then
    echo "Transit key 'glyphoxa-bot-tokens' already exists."
else
    vault write -f transit/keys/glyphoxa-bot-tokens \
        type=aes256-gcm96 \
        deletion_allowed=false
    echo "Transit key 'glyphoxa-bot-tokens' created (AES-256-GCM96)."
fi

# ── PKI Secrets Engine ───────────────────────────────────────────────────────
echo ""
echo "--- PKI Secrets Engine ---"

if vault secrets list -format=json | grep -q '"pki/"'; then
    echo "PKI engine already enabled."
else
    vault secrets enable pki
    vault secrets tune -max-lease-ttl=8760h pki  # 1 year max
    echo "PKI engine enabled (max TTL: 1 year)."
fi

# Generate root CA (self-signed) if not present.
if vault read pki/cert/ca >/dev/null 2>&1; then
    echo "PKI root CA already exists."
else
    vault write pki/root/generate/internal \
        common_name="Glyphoxa Internal CA" \
        ttl=8760h \
        key_type=ec \
        key_bits=256
    echo "PKI root CA generated (EC P-256, 1 year TTL)."
fi

# Configure CA and CRL URLs.
vault write pki/config/urls \
    issuing_certificates="${VAULT_ADDR}/v1/pki/ca" \
    crl_distribution_points="${VAULT_ADDR}/v1/pki/crl"

# Create role for gRPC mTLS certificates.
if vault read pki/roles/glyphoxa-grpc >/dev/null 2>&1; then
    echo "PKI role 'glyphoxa-grpc' already exists."
else
    vault write pki/roles/glyphoxa-grpc \
        allowed_domains="glyphoxa.svc,glyphoxa.local,openclaw.lan,localhost" \
        allow_subdomains=true \
        allow_localhost=true \
        max_ttl=72h \
        ttl=24h \
        key_type=ec \
        key_bits=256 \
        require_cn=true \
        server_flag=true \
        client_flag=true
    echo "PKI role 'glyphoxa-grpc' created (EC P-256, 24h default TTL)."
fi

# ── Policy for Glyphoxa Service ──────────────────────────────────────────────
echo ""
echo "--- Vault Policy ---"

vault policy write glyphoxa - <<'POLICY'
# Transit: encrypt/decrypt bot tokens.
path "transit/encrypt/glyphoxa-bot-tokens" {
  capabilities = ["update"]
}
path "transit/decrypt/glyphoxa-bot-tokens" {
  capabilities = ["update"]
}

# PKI: issue gRPC mTLS certificates.
path "pki/issue/glyphoxa-grpc" {
  capabilities = ["create", "update"]
}

# Health check.
path "sys/health" {
  capabilities = ["read"]
}
POLICY
echo "Policy 'glyphoxa' written."

# ── Create AppRole or Token ─────────────────────────────────────────────────
echo ""
echo "--- Service Token ---"
echo "Creating a service token with the 'glyphoxa' policy (renewable, 720h TTL)..."
vault token create \
    -policy=glyphoxa \
    -ttl=720h \
    -renewable \
    -display-name="glyphoxa-service" \
    -format=json | tee /tmp/glyphoxa-vault-token.json

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Service token saved to /tmp/glyphoxa-vault-token.json"
echo ""
echo "Set these environment variables for Glyphoxa:"
echo "  export VAULT_ADDR=${VAULT_ADDR}"
TOKEN=$(cat /tmp/glyphoxa-vault-token.json | python3 -c "import sys,json; print(json.load(sys.stdin)['auth']['client_token'])" 2>/dev/null || echo "<see /tmp/glyphoxa-vault-token.json>")
echo "  export VAULT_TOKEN=${TOKEN}"
echo ""
echo "To manually issue a gRPC certificate:"
echo "  vault write pki/issue/glyphoxa-grpc \\"
echo "    common_name=gateway.glyphoxa.svc \\"
echo "    ttl=24h"
