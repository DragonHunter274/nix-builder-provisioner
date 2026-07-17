# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Nix ARM builder provisioner - similar to [nixbuild.net](https://nixbuild.net). It's an SSH server that speaks the Nix daemon protocol and dynamically provisions Hetzner Cloud ARM servers (Ubuntu with Nix) as remote builders. **Each derivation build gets its own dedicated VM**.

Clients connect via SSH to the proxy (port 2222) using `ssh-ng://` protocol, and the proxy intercepts `BuildDerivation` operations at the Nix protocol level, routing each build to a freshly provisioned or pooled ARM builder.

**Key features**:
- **One VM per derivation** - Each `BuildDerivation` operation gets its own isolated VM
- Builders are Ubuntu 24.04 ARM servers with Nix installed via [Determinate Systems installer](https://github.com/DeterminateSystems/nix-installer)
- Builders use a shared Nix store via SSH (`mounted-ssh-ng://` store protocol)
- All SSH keys are automatically generated and managed
- The store can be hosted on any machine with Nix and SSH (Hydra server, dedicated cache, etc.)

## Architecture

### Core Components

1. **SSH Server & Nix Protocol Proxy** ([main.go](main.go))
   - Listens on port 2222 for incoming SSH connections
   - Handles `nix-daemon --stdio` exec requests
   - Speaks the Nix daemon wire protocol (handshake, operations, stderr streaming)
   - Routes `BuildDerivation` operations (opcode 36) to dedicated VMs

2. **Nix Protocol Implementation** ([nixproto/](nixproto/))
   - `protocol.go` - Wire format primitives (uint64, strings, padding)
   - `derivation.go` - BasicDerivation and BuildResult types
   - `conn.go` - Protocol connection handling and handshake
   - `proxy.go` - Operation dispatch and BuildDerivation routing

3. **Builder Pool** ([main.go](main.go))
   - Implements `nixproto.BuilderProvider` interface
   - Manages lifecycle of ephemeral ARM builder VMs
   - Provisions new VMs on demand or reuses from pool
   - Optional builder pooling (`BUILDER_REUSE=true`)

4. **Automatic SSH Key Management**
   - `proxy_ssh_key` - Proxy's host key AND key to SSH into builders
   - `builder_store_key` - Used by builders to access the store host
   - Both keypairs are auto-generated on first run

5. **Terraform Integration** ([terraform/builder.tf](terraform/builder.tf))
   - Uses OpenTofu (Terraform) to provision Hetzner Cloud servers
   - Creates Ubuntu 24.04 ARM instances (server type: cax31)
   - Configures builders via cloud-init to install Nix and set up store access

6. **Gradient Worker** ([gradientproto/](gradientproto/))
   - Native worker client for [Gradient](https://github.com/wavelens/gradient) (`GRADIENT_ENABLED=true`)
   - `wire/` - pure-Go reimplementation of Gradient's `rkyv` binary wire format
   - `client.go` - handshake, capability advertisement, job pull/dispatch loop
   - `executor.go` - bridges assigned jobs to the same builder pool/`nixproto.ExecuteBuild` as the ssh-ng:// path
   - `narupload.go` - zstd NAR compression, chunked WebSocket push, presigned S3 upload

### Connection Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Nix Client                                     │
│                                                                             │
│   nix build --store ssh-ng://proxy:2222 ...                                │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    │ SSH connection
                                    │ exec: "nix-daemon --stdio"
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Proxy (this project)                              │
│                                                                             │
│   1. SSH authentication (authorized_keys)                                   │
│   2. Nix protocol handshake                                                 │
│   3. Handle operations:                                                     │
│      - SetOptions, IsValidPath, QueryValidPaths → local responses          │
│      - BuildDerivation → route to dedicated VM                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
                    ▼               ▼               ▼
              ┌─────────┐     ┌─────────┐     ┌─────────┐
              │  VM #1  │     │  VM #2  │     │  VM #3  │
              │ (drv A) │     │ (drv B) │     │ (drv C) │
              └────┬────┘     └────┬────┘     └────┬────┘
                   │               │               │
                   └───────────────┼───────────────┘
                                   │
                                   │ mounted-ssh-ng://
                                   ▼
                          ┌─────────────────┐
                          │   Store Host    │
                          │ (Hydra, cache)  │
                          │   /nix/store    │
                          └─────────────────┘
```

### How BuildDerivation Routing Works

1. Client sends `BuildDerivation` operation (opcode 36) with:
   - Derivation path (e.g., `/nix/store/xxx.drv`)
   - BasicDerivation struct (outputs, inputs, platform, builder, args, env)
   - Build mode (normal/repair/check)

2. Proxy calls `BuilderPool.GetBuilder(drvPath)`:
   - If pooled builder available → return immediately
   - Otherwise → provision new VM, wait for cloud-init

3. Proxy opens SSH session to builder, runs `nix-daemon --stdio`

4. Proxy forwards the `BuildDerivation` to builder's nix-daemon

5. Builder executes the build, streams stderr/logs back

6. Proxy returns `BuildResult` to client

7. Proxy releases builder back to pool (or destroys it)

### Nix Protocol Details

The proxy implements the Nix daemon wire protocol:

- **Handshake**: Magic bytes (0x6e697863 / 0x6478696f), version negotiation (1.37)
- **Operations**: SetOptions, IsValidPath, QueryValidPaths, BuildDerivation, etc.
- **Stderr streaming**: Log messages (0x6f6c6d67), errors (0x63787470), activities
- **Wire format**: Little-endian uint64, length-prefixed strings with 8-byte padding

Key files:
- `nixproto/protocol.go:117-180` - Wire format read/write functions
- `nixproto/conn.go:35-85` - Handshake implementation
- `nixproto/proxy.go:85-130` - Operation dispatch
- `nixproto/proxy.go:175-260` - BuildDerivation handler

## Store Host Requirements

The store host (e.g., your Hydra server) needs:

1. **Nix installed** with nix-daemon running
2. **sshd running** (configurable port via `STORE_HOST_SSH_PORT`)
3. **A user for builder access** (default: `nix-builder`)
4. **Builder public key** in that user's `authorized_keys`
5. **User in trusted-users** in nix.conf

Example NixOS configuration for store host:
```nix
users.users.nix-builder = {
  isNormalUser = true;
  openssh.authorizedKeys.keys = [
    # Contents of builder_store_key.pub (printed at proxy startup)
  ];
};

nix.settings.trusted-users = [ "nix-builder" ];
```

## Development Commands

### Running the Proxy

```bash
# Required
export HCLOUD_TOKEN="your-hetzner-api-token"
export STORE_HOST="hydra.example.com"  # Your Nix store server

# Store host configuration (optional, has defaults)
export STORE_HOST_SSH_PORT="22"
export STORE_HOST_USER="nix-builder"
export STORE_HOST_KEY="ssh-ed25519 AAAA..."  # Optional: host key for known_hosts

# Builder pool configuration
export BUILDER_REUSE="true"          # Enable builder pooling for efficiency
export BUILDER_POOL_SIZE="5"         # Max builders to keep in pool
export BUILDER_DESTROY_DELAY="30s"   # Delay before destroying unused builder

# Client authentication
export AUTHORIZED_KEYS_FILE="$HOME/.ssh/authorized_keys"

# Run the proxy
go run main.go
```

On first run, the proxy will:
1. Generate `proxy_ssh_key` (used to connect to builders)
2. Generate `builder_store_key` (used by builders to connect to store)
3. Print the `builder_store_key.pub` - add this to your store host's authorized_keys

### Building

```bash
go build -o nix-builder-provisioner .
```

### Client Usage

Configure your Nix client to use the proxy:

```bash
# Direct usage
nix build --store ssh-ng://root@proxy-host:2222 nixpkgs#hello

# Or add to /etc/nix/machines for distributed builds
# ssh-ng://root@proxy-host:2222 aarch64-linux - 1 1 big-parallel
```

### Terraform Operations

The Go program handles Terraform operations automatically, but for manual debugging:

```bash
cd terraform

# Initialize (first time only)
tofu init

# Manually create a builder
tofu apply -auto-approve \
  -var=builder_name=nix-builder-test \
  -var=store_host=hydra.example.com \
  -var=store_host_user=nix-builder \
  -var='proxy_public_key=ssh-ed25519 AAAA...' \
  -var='builder_store_private_key=...' \
  -state=state/test.tfstate

# Get IP address
tofu output -raw -state=state/test.tfstate ipv4_address

# Manually destroy
tofu destroy -auto-approve -state=state/test.tfstate
```

## Key Implementation Details

### SSH Key Management (Automatic)

The proxy automatically manages two keypairs:

| Key File | Purpose |
|----------|---------|
| `proxy_ssh_key` | Proxy's host key for client connections AND private key for connecting to builders |
| `builder_store_key` | Used by builders to SSH into the store host for Nix operations |

No manual SSH key configuration is needed - just add the printed `builder_store_key.pub` to your store host.

### Builder Image

Builders use **Ubuntu 24.04 ARM** with:
- Nix installed via [Determinate Systems installer](https://github.com/DeterminateSystems/nix-installer)
- Experimental features enabled (nix-command, flakes, mounted-ssh-store)
- Store configured to use `mounted-ssh-ng://` remote store via SSHFS

This is faster to provision than NixOS images and more flexible.

### Security Logging

All authentication events are logged with "SECURITY:" prefix:
- Successful authentications log client IP and key fingerprint
- Failed authentications log rejected key fingerprints

### State Management

- Builder state files stored in `terraform/state/<builder-id>.tfstate`
- State directory created automatically at startup
- State files deleted after builder destruction

### Concurrency

- BuilderPool protected by `sync.Mutex`
- AuthorizedKeysManager uses `sync.RWMutex` for read-heavy workload
- Each client connection handled in separate goroutine
- Each BuildDerivation can run concurrently on different VMs

## Environment Variables Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `HCLOUD_TOKEN` | Yes | - | Hetzner Cloud API token |
| `STORE_HOST` | Yes | - | IP or hostname of the Nix store server (e.g., Hydra) |
| `STORE_HOST_SSH_PORT` | No | `22` | SSH port on store host |
| `STORE_HOST_USER` | No | `nix-builder` | User on store host for builder access |
| `STORE_HOST_KEY` | No | - | SSH host public key of store server (for known_hosts) |
| `BUILDER_REUSE` | No | `false` | Enable builder pooling |
| `BUILDER_POOL_SIZE` | No | `5` | Max builders to keep in pool |
| `BUILDER_DESTROY_DELAY` | No | `30s` | Delay before destroying unused builder |
| `AUTHORIZED_KEYS_FILE` | No | `~/.ssh/authorized_keys` | Path to authorized_keys file |
| `GRADIENT_ENABLED` | No | `false` | Register as a native [Gradient](https://github.com/wavelens/gradient) build worker (see [gradientproto/](gradientproto/)), alongside the ssh-ng:// listener |
| `GRADIENT_SERVER_URL` | Yes if enabled | - | Gradient server's `/proto` WebSocket endpoint, e.g. `wss://gradient.example.com/proto` |
| `GRADIENT_PEERS_FILE` | No | - | `peer_id:token` credential file for the handshake; omit for open/discoverable mode |
| `GRADIENT_ARCHITECTURES` | No | `x86_64-linux,aarch64-linux` | Comma-separated Nix system strings advertised to Gradient's scheduler |
| `GRADIENT_MAX_CONCURRENT_BUILDS` | No | `5` | Build capacity advertised to Gradient |

## Gradient Worker

In addition to the `ssh-ng://` proxy, this project can register directly as a native
[Gradient](https://github.com/wavelens/gradient) build worker (`GRADIENT_ENABLED=true`) - see
[gradientproto/](gradientproto/) for the implementation. Gradient's wire protocol is `rkyv`
binary archives over a WebSocket, not JSON/protobuf, so `gradientproto/wire` reimplements the
relevant parts of that codec in pure Go; see `gradientproto/testdata/gen-fixtures/README.md` for
how it was reverse-engineered and cross-validated against the real `rkyv` crate. The Gradient
worker shares the same `*provisioner.Pool` as the ssh-ng:// path - a build assigned via either
protocol draws from the identical VM-per-derivation builder pool. Only the `build` capability is
implemented (not `eval`/`fetch`/`federate`/`cache`), and `external_cached` build tasks and NAR
pull/resume are not yet supported - see doc comments in `gradientproto/executor.go` and
`gradientproto/narupload.go` for the current scope.

## Cloud-Init Configuration

Builders are configured at boot via [terraform/cloud-init.yaml](terraform/cloud-init.yaml):

1. Proxy's public key added to root's authorized_keys
2. Store access private key written to `/root/.ssh/store_key`
3. Store host key added to known_hosts (if provided)
4. SSHFS installed and remote `/nix/store` mounted via systemd service
5. Nix installed via Determinate Systems installer
6. Nix configured to use `mounted-ssh-ng://` store with `mounted-ssh-store` experimental feature
7. Remote store added to `trusted-substituters`
8. nix-daemon restarted with new configuration

## Differences from nixbuild.net

This is a simplified, self-hosted alternative to nixbuild.net:

| Feature | This Project | nixbuild.net |
|---------|--------------|--------------|
| VM per derivation | ✅ | ✅ |
| Builder pooling | ✅ | ✅ |
| Automatic resource sizing | ❌ | ✅ |
| Multi-region | ❌ | ✅ |
| Build deduplication | ❌ | ✅ |
| Web dashboard | ❌ | ✅ |
| Self-hosted | ✅ | ❌ |
| Open source | ✅ | ❌ |
