#!/bin/bash
set -euo pipefail

SECRETS_DIR="/secrets"
SSH_DIR="/root/.ssh"

mkdir -p "$SSH_DIR"
chmod 700 "$SSH_DIR"

# Install proxy's public key so the pool can SSH in
install -m 600 "${SECRETS_DIR}/authorized_keys" "${SSH_DIR}/authorized_keys"

# Install the key used to access the Nix store host
install -m 600 "${SECRETS_DIR}/store_key" "${SSH_DIR}/store_key"

STORE_HOST=$(cat "${SECRETS_DIR}/store_host")
STORE_PORT=$(cat "${SECRETS_DIR}/store_host_port")
STORE_USER=$(cat "${SECRETS_DIR}/store_host_user")
STORE_HOST_KEY=$(cat "${SECRETS_DIR}/store_host_key" 2>/dev/null || true)

if [ -n "${STORE_HOST_KEY}" ]; then
    echo "[${STORE_HOST}]:${STORE_PORT} ${STORE_HOST_KEY}" > "${SSH_DIR}/known_hosts"
    chmod 600 "${SSH_DIR}/known_hosts"
    STRICT="yes"
else
    STRICT="accept-new"
fi

cat > "${SSH_DIR}/config" << EOF
Host nix-store
    HostName ${STORE_HOST}
    Port ${STORE_PORT}
    User ${STORE_USER}
    IdentityFile ${SSH_DIR}/store_key
    StrictHostKeyChecking ${STRICT}
EOF
chmod 600 "${SSH_DIR}/config"

# Configure Nix to use the remote store over ssh-ng:// (no FUSE needed)
cat >> /etc/nix/nix.custom.conf << EOF

store = ssh-ng://${STORE_USER}@${STORE_HOST}:${STORE_PORT}?ssh-key=${SSH_DIR}/store_key&trusted=true
max-jobs = auto
cores = 0
sandbox = false
EOF

# Pre-accept the store host key on first connection when no key is pre-configured
if [ "${STRICT}" = "accept-new" ]; then
    ssh -o StrictHostKeyChecking=accept-new \
        -o BatchMode=yes \
        -i "${SSH_DIR}/store_key" \
        -p "${STORE_PORT}" \
        "${STORE_USER}@${STORE_HOST}" \
        true || true
fi

exec /usr/sbin/sshd -D -e
