package hetznerubuntu

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nix-builder-provisioner/provisioner"

	"golang.org/x/crypto/ssh"
)

// HetznerConfig holds Hetzner-specific configuration
type HetznerConfig struct {
	// ServerTypeByArch maps Nix arch (e.g. "aarch64", "x86_64") to Hetzner server type.
	// e.g. {"aarch64": "cax31", "x86_64": "cx33"}
	ServerTypeByArch map[string]string
	Location         string // e.g., "fsn1", "nbg1"
}

// Provisioner implements provisioner.Provisioner for Hetzner using ubuntu-24.04 image
type Provisioner struct {
	mu           sync.Mutex
	terraformDir string
	hetznerCfg   HetznerConfig
	provConfig   *provisioner.Config
	builderArch  map[string]string // builderID -> arch
}

// New creates a new Hetzner Ubuntu provisioner
func New(cfg HetznerConfig) *Provisioner {
	// Get the directory where this package's terraform files are located
	// This assumes terraform/ is a subdirectory of the package
	execPath, _ := os.Executable()
	baseDir := filepath.Dir(execPath)
	terraformDir := filepath.Join(baseDir, "provisioner", "hetzner-ubuntu", "terraform")

	// Fallback to relative path if running with go run
	if _, err := os.Stat(terraformDir); os.IsNotExist(err) {
		terraformDir = "provisioner/hetzner-ubuntu/terraform"
	}

	return &Provisioner{
		terraformDir: terraformDir,
		hetznerCfg:   cfg,
		builderArch:  make(map[string]string),
	}
}

// NewWithDir creates a new provisioner with an explicit terraform directory
func NewWithDir(terraformDir string, cfg HetznerConfig) *Provisioner {
	absDir, err := filepath.Abs(terraformDir)
	if err != nil {
		log.Printf("Warning: could not resolve terraform dir %s: %v", terraformDir, err)
		absDir = terraformDir
	}
	log.Printf("Terraform dir: %s", absDir)
	p := &Provisioner{
		terraformDir: absDir,
		hetznerCfg:   cfg,
		builderArch:  make(map[string]string),
	}
	cmd := exec.Command("tofu", "init", "-upgrade")
	cmd.Dir = absDir
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Warning: tofu init failed in %s: %v\n%s", absDir, err, out)
	}
	return p
}

// Name implements provisioner.Provisioner
func (p *Provisioner) Name() string {
	return "hetzner-ubuntu"
}

// Create implements provisioner.Provisioner
func (p *Provisioner) Create(ctx context.Context, id string, config provisioner.Config, arch string) (string, error) {
	serverType, ok := p.hetznerCfg.ServerTypeByArch[arch]
	if !ok {
		return "", fmt.Errorf("no Hetzner server type configured for arch %q", arch)
	}

	p.mu.Lock()
	p.provConfig = &config
	p.builderArch[id] = arch
	p.mu.Unlock()

	tofuStart := time.Now()

	// Ensure state directory exists
	stateDir := filepath.Join(p.terraformDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", fmt.Errorf("creating state directory: %w", err)
	}

	args := []string{"apply", "-auto-approve",
		fmt.Sprintf("-var=builder_name=nix-builder-%s", id),
		fmt.Sprintf("-var=server_type=%s", serverType),
		fmt.Sprintf("-var=store_host=%s", config.StoreHost),
		fmt.Sprintf("-var=store_host_ssh_port=%d", config.StoreHostSSHPort),
		fmt.Sprintf("-var=store_host_user=%s", config.StoreHostUser),
		fmt.Sprintf("-var=store_host_public_key=%s", config.StoreHostKey),
		fmt.Sprintf("-var=proxy_public_key=%s", strings.TrimSpace(string(config.ProxyPublicKey))),
		fmt.Sprintf("-var=builder_store_private_key=%s", string(config.BuilderStorePrivKey)),
	}

	// Add location if specified
	if p.hetznerCfg.Location != "" {
		args = append(args, fmt.Sprintf("-var=location=%s", p.hetznerCfg.Location))
	}

	args = append(args, "-state", fmt.Sprintf("state/%s.tfstate", id))

	logPath := fmt.Sprintf("/tmp/tofu-apply-%s.log", id)
	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = p.terraformDir
	// Override TMPDIR to /tmp so go-plugin Unix sockets don't exceed the 108-char
	// path limit when the process is spawned from a deeply-nested nix-shell TMPDIR.
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMPDIR=") {
			env = append(env, e)
		}
	}
	env = append(env, "TMPDIR=/tmp", "TF_LOG=TRACE", "TF_LOG_PATH="+logPath)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		logData, _ := os.ReadFile(logPath)
		return "", fmt.Errorf("tofu apply failed: %w\nOutput: %s\nPlugin trace:\n%s",
			err, string(output), extractPluginLines(string(logData)))
	}
	os.Remove(logPath)

	log.Printf("Builder %s: Terraform apply completed in %s", id, time.Since(tofuStart).Round(time.Second))

	// Get IP address
	outputArgs := []string{"output", "-raw", "-state", fmt.Sprintf("state/%s.tfstate", id), "ipv4_address"}
	cmd = exec.CommandContext(ctx, "tofu", outputArgs...)
	cmd.Dir = p.terraformDir
	output, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get IP: %w", err)
	}

	ip := strings.TrimSpace(string(output))

	// Wait for builder to be ready
	if err := p.waitForBuilder(ctx, id, ip, config.ProxyPublicKey); err != nil {
		return "", err
	}

	return ip, nil
}

// Destroy implements provisioner.Provisioner
func (p *Provisioner) Destroy(ctx context.Context, id string) error {
	log.Printf("Destroying builder %s", id)

	p.mu.Lock()
	arch := p.builderArch[id]
	delete(p.builderArch, id)
	config := p.provConfig
	p.mu.Unlock()

	if config == nil || arch == "" {
		return fmt.Errorf("no config/arch recorded for builder %s, cannot destroy", id)
	}

	serverType, ok := p.hetznerCfg.ServerTypeByArch[arch]
	if !ok {
		return fmt.Errorf("no server type for arch %q", arch)
	}

	args := []string{"destroy", "-auto-approve",
		fmt.Sprintf("-var=builder_name=nix-builder-%s", id),
		fmt.Sprintf("-var=server_type=%s", serverType),
		fmt.Sprintf("-var=store_host=%s", config.StoreHost),
		fmt.Sprintf("-var=store_host_ssh_port=%d", config.StoreHostSSHPort),
		fmt.Sprintf("-var=store_host_user=%s", config.StoreHostUser),
		fmt.Sprintf("-var=store_host_public_key=%s", config.StoreHostKey),
		fmt.Sprintf("-var=proxy_public_key=%s", strings.TrimSpace(string(config.ProxyPublicKey))),
		fmt.Sprintf("-var=builder_store_private_key=%s", string(config.BuilderStorePrivKey)),
		"-state", fmt.Sprintf("state/%s.tfstate", id),
	}

	cmd := exec.CommandContext(ctx, "tofu", args...)
	cmd.Dir = p.terraformDir
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMPDIR=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "TMPDIR=/tmp")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Failed to destroy builder %s: %v\n%s", id, err, out)
		return fmt.Errorf("tofu destroy failed for builder %s: %w", id, err)
	}

	log.Printf("Builder %s destroyed", id)

	stateFile := filepath.Join(p.terraformDir, "state", fmt.Sprintf("%s.tfstate", id))
	os.Remove(stateFile)

	return nil
}

func extractPluginLines(log string) string {
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "plugin") || strings.Contains(lower, "provider") || strings.Contains(lower, "cookie") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	return strings.Join(lines, "\n")
}

// waitForBuilder waits for the builder to be ready
func (p *Provisioner) waitForBuilder(ctx context.Context, builderID, ip string, _ []byte) error {
	sshWaitStart := time.Now()

	// 1. Wait for SSH Port
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.DialTimeout("tcp", ip+":22", 2*time.Second)
		if err == nil {
			conn.Close()
			log.Printf("Builder %s: SSH port available after %s", builderID, time.Since(sshWaitStart).Round(time.Second))
			break
		}
		time.Sleep(3 * time.Second)
	}

	// 2. Wait for cloud-init (Nix setup)
	cloudInitStart := time.Now()

	// Parse the proxy key to get a signer
	// We need to derive the private key from what we have - but we only have the public key
	// The actual private key should be passed in. For now, let's use InsecureIgnoreHostKey
	// and just check for the cloud-init marker.

	// Actually, we need to SSH in to check for the marker. Let's load the proxy key.
	// The config only has the public key - we need to get the private key from somewhere.
	// For now, let's use a file-based approach matching the original code.

	proxyKeyPath := "proxy_ssh_key"
	privKey, err := os.ReadFile(proxyKeyPath)
	if err != nil {
		return fmt.Errorf("reading proxy private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("parsing proxy private key: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		client, err := ssh.Dial("tcp", ip+":22", sshConfig)
		if err == nil {
			session, err := client.NewSession()
			if err == nil {
				out, _ := session.CombinedOutput("test -f /var/lib/cloud/instance/boot-finished-nix && echo ready")
				session.Close()
				client.Close()
				if strings.TrimSpace(string(out)) == "ready" {
					log.Printf("Builder %s: Cloud-init completed after %s", builderID, time.Since(cloudInitStart).Round(time.Second))
					return nil
				}
			} else {
				client.Close()
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("builder timed out on cloud-init after %s", time.Since(cloudInitStart).Round(time.Second))
}
