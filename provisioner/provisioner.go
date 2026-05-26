package provisioner

import (
	"context"
	"time"
)

// BuilderStatus represents the state of a builder
type BuilderStatus int

const (
	BuilderStatusProvisioning BuilderStatus = iota
	BuilderStatusReady
	BuilderStatusInUse
	BuilderStatusPooled
	BuilderStatusDestroying
)

// Builder represents a running builder VM
type Builder struct {
	ID       string
	IP       string
	Arch     string // e.g., "aarch64", "x86_64"
	Created  time.Time
	LastUsed time.Time
	Status   BuilderStatus
	DrvPath  string // The derivation this builder is handling (if any)
}

// Config holds configuration passed to provisioners
type Config struct {
	StoreHost        string
	StoreHostSSHPort int
	StoreHostUser    string
	StoreHostKey     string // Public key for known_hosts

	// SSH keys
	ProxyPublicKey      []byte // Added to builder's authorized_keys
	BuilderStorePrivKey []byte // Used by builder to access store
}

// Provisioner creates and destroys builder VMs
type Provisioner interface {
	// Create provisions a new builder VM for the given architecture.
	// arch is the Nix system architecture, e.g. "aarch64" or "x86_64".
	// Returns when the builder is ready to accept SSH connections.
	Create(ctx context.Context, id string, config Config, arch string) (ip string, err error)

	// Destroy terminates a builder VM.
	Destroy(ctx context.Context, id string) error

	// Name returns the provisioner name (e.g., "hetzner-ubuntu", "hetzner-snapshot")
	Name() string
}
