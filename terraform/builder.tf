# terraform/builder.tf
terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "< 2"
    }
  }
}

variable "builder_name" {
  type = string
}

# Store host configuration - where builders access the shared Nix store
variable "store_host" {
  type        = string
  description = "IP or hostname of the Nix store server (e.g., Hydra server)"
}

variable "store_host_ssh_port" {
  type        = number
  description = "SSH port on the store host"
  default     = 22
}

variable "store_host_user" {
  type        = string
  description = "User on store host for builder access"
  default     = "nix-builder"
}

variable "store_host_public_key" {
  type        = string
  description = "SSH host public key of store server (for known_hosts)"
  default     = ""
}

# SSH keys
variable "proxy_public_key" {
  type        = string
  description = "Public SSH key of the proxy (added to builder's authorized_keys via Hetzner)"
}

variable "builder_store_private_key" {
  type        = string
  description = "Private SSH key for builder to access store host"
  sensitive   = true
}

data "hcloud_ssh_key" "existing_key" {
  name = "simon@thinkpad-simon"
}

resource "hcloud_server" "builder" {
  name        = var.builder_name
  image       = "ubuntu-24.04"
  server_type = "cx33"

  # Inject proxy's public key directly via cloud-init since Hetzner ssh_keys
  # requires pre-existing keys and causes conflicts with per-builder state files
  ssh_keys = [data.hcloud_ssh_key.existing_key.id]

  user_data = templatefile("${path.module}/cloud-init.yaml", {
    store_host                = var.store_host
    store_host_ssh_port       = var.store_host_ssh_port
    store_host_user           = var.store_host_user
    store_host_public_key     = var.store_host_public_key
    builder_store_private_key = var.builder_store_private_key
    proxy_public_key = var.proxy_public_key
  })

  # Ensure the SSH key is created before the server
  # depends_on = [hcloud_ssh_key.proxy]

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  labels = {
    type       = "nix-builder"
    managed_by = "nix-arm-proxy"
  }
}

output "ipv4_address" {
  value = hcloud_server.builder.ipv4_address
}
