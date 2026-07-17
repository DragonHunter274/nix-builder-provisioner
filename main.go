package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nix-builder-provisioner/gradientproto"
	"nix-builder-provisioner/metrics"
	"nix-builder-provisioner/nixproto"
	"nix-builder-provisioner/provisioner"
	hetznerubuntu "nix-builder-provisioner/provisioner/hetzner-ubuntu"
	kubernetesprov "nix-builder-provisioner/provisioner/kubernetes"

	"golang.org/x/crypto/ssh"
)

// --- Configuration ---

type Config struct {
	HCloudToken         string
	AuthorizedKeysFile  string
	StoreHostIP         string
	StoreHostSSHPort    int
	StoreHostUser       string
	StoreHostKey        string
	BuilderReuse        bool
	BuilderPoolSize     int
	BuilderDestroyDelay time.Duration
	Provisioner         string // "hetzner-ubuntu" or "kubernetes"

	// Kubernetes provisioner settings
	K8sNamespace       string
	K8sBuilderImage    string
	K8sCPURequest      string
	K8sMemoryRequest   string
	K8sCPULimit        string
	K8sMemoryLimit     string
	K8sImagePullSecret string

	// Signing
	SigningKeyName string
	Signer         *nixproto.StoreSigner

	// Gradient worker (see gradientproto/) - registers this proxy as a
	// native Gradient build worker in addition to the ssh-ng:// path.
	GradientEnabled             bool
	GradientServerURL           string
	GradientPeersFile           string
	GradientArchitectures       []string
	GradientMaxConcurrentBuilds uint32
}

type SSHKeyPair struct {
	PrivateKey     []byte
	PublicKey      []byte
	PrivateKeyPath string
	PublicKeyPath  string
	Signer         ssh.Signer
}

// --- Authorized Keys Manager ---

type AuthorizedKeysManager struct {
	mu           sync.RWMutex
	keys         map[string]ssh.PublicKey
	lastModified time.Time
	keyFile      string
}

// --- Main SSH Server Logic ---

// handleConnection handles an SSH connection from a Nix client
// When the client runs "nix-daemon --stdio", we intercept at the Nix protocol level
// and route each BuildDerivation to its own VM
func handleConnection(clientConn net.Conn, pool *provisioner.Pool, keysManager *AuthorizedKeysManager, proxyKey *SSHKeyPair, config *Config, storeKey *SSHKeyPair, metricsDB *metrics.Store) {
	defer clientConn.Close()

	sshConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if keysManager.IsAuthorized(key) {
				log.Printf("SECURITY: Authorized connection from %s (key: %s)",
					conn.RemoteAddr(), ssh.FingerprintSHA256(key))
				return &ssh.Permissions{}, nil
			}
			log.Printf("SECURITY: Rejected connection from %s (key: %s)",
				conn.RemoteAddr(), ssh.FingerprintSHA256(key))
			return nil, fmt.Errorf("unauthorized")
		},
	}
	sshConfig.AddHostKey(proxyKey.Signer)

	// Perform Handshake
	serverConn, chans, reqs, err := ssh.NewServerConn(clientConn, sshConfig)
	if err != nil {
		// Ignore EOF — happens on TCP health probes that connect and immediately close.
		if err != io.EOF && !strings.Contains(err.Error(), "EOF") {
			log.Printf("SSH handshake failed: %v", err)
		}
		return
	}
	defer serverConn.Close()

	log.Printf("New SSH connection from %s", serverConn.RemoteAddr())

	go ssh.DiscardRequests(reqs)

	// Handle channels
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChan.Accept()
		if err != nil {
			log.Printf("Could not accept channel: %v", err)
			continue
		}

		go handleSSHSession(channel, requests, pool, config, storeKey, metricsDB)
	}
}

// handleSSHSession handles an SSH session channel
// We look for "exec" requests running "nix-daemon --stdio" and intercept those
func handleSSHSession(channel ssh.Channel, requests <-chan *ssh.Request, pool *provisioner.Pool, config *Config, storeKey *SSHKeyPair, metricsDB *metrics.Store) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "exec":
			// Parse the command
			if len(req.Payload) < 4 {
				req.Reply(false, nil)
				continue
			}
			cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
			if len(req.Payload) < 4+cmdLen {
				req.Reply(false, nil)
				continue
			}
			cmd := string(req.Payload[4 : 4+cmdLen])

			log.Printf("Exec request: %s", cmd)

			// Check if this is nix-daemon --stdio
			if strings.Contains(cmd, "nix-daemon") && strings.Contains(cmd, "--stdio") {
				req.Reply(true, nil)
				handleNixDaemon(channel, pool, config, storeKey, metricsDB)
				// Send exit status
				channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
				return
			}

			// For other commands, reject
			log.Printf("Rejecting non-nix-daemon command: %s", cmd)
			req.Reply(false, nil)

		case "shell":
			log.Printf("Shell request rejected - only nix-daemon --stdio is supported")
			req.Reply(false, nil)

		case "pty-req":
			// Reject PTY requests
			req.Reply(false, nil)

		case "env":
			// Accept env requests but ignore them
			req.Reply(true, nil)

		default:
			log.Printf("Unknown request type: %s", req.Type)
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// cancelOnCloseReader wraps a reader and cancels a context when the reader returns an error
type cancelOnCloseReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c *cancelOnCloseReader) Read(p []byte) (n int, err error) {
	n, err = c.r.Read(p)
	if err != nil {
		c.cancel()
	}
	return
}

// handleNixDaemon handles a nix-daemon --stdio session
// This is where we intercept the Nix protocol and route BuildDerivation to VMs
func handleNixDaemon(channel ssh.Channel, pool *provisioner.Pool, config *Config, storeKey *SSHKeyPair, metricsDB *metrics.Store) {
	log.Printf("Starting Nix daemon protocol handler")

	// Create a context that gets cancelled when the channel closes/errors
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wrap the channel reader to cancel context on any read error (including EOF)
	reader := &cancelOnCloseReader{r: channel, cancel: cancel}

	proxy := nixproto.NewProxy(nixproto.ProxyConfig{
		StoreDir:      "/nix/store",
		StoreHostAddr: fmt.Sprintf("%s:%d", config.StoreHostIP, config.StoreHostSSHPort),
		StoreHostUser: config.StoreHostUser,
		StoreHostKey:  storeKey.Signer,
		Metrics:       metricsDB,
		Signer:        config.Signer,
	}, pool)

	if err := proxy.HandleConnectionWithContext(ctx, reader, channel); err != nil {
		if err != io.EOF && ctx.Err() == nil {
			log.Printf("Nix protocol error: %v", err)
		}
	}

	log.Printf("Nix daemon session ended")
}

// --- Utils & Boilerplate ---

func LoadConfig() *Config {
	return &Config{
		HCloudToken:         os.Getenv("HCLOUD_TOKEN"),
		AuthorizedKeysFile:  os.Getenv("AUTHORIZED_KEYS_FILE"),
		StoreHostIP:         os.Getenv("STORE_HOST"),
		StoreHostSSHPort:    getEnvInt("STORE_HOST_SSH_PORT", 22),
		StoreHostUser:       getEnvString("STORE_HOST_USER", "nix-builder"),
		StoreHostKey:        os.Getenv("STORE_HOST_KEY"),
		BuilderReuse:        os.Getenv("BUILDER_REUSE") == "true",
		BuilderPoolSize:     getEnvInt("BUILDER_POOL_SIZE", 5),
		BuilderDestroyDelay: getEnvDuration("BUILDER_DESTROY_DELAY", 30*time.Second),
		Provisioner:         getEnvString("PROVISIONER", "hetzner-ubuntu"),

		K8sNamespace:       getEnvString("K8S_NAMESPACE", "nix-builders"),
		K8sBuilderImage:    os.Getenv("K8S_BUILDER_IMAGE"),
		K8sCPURequest:      getEnvString("K8S_CPU_REQUEST", "2"),
		K8sMemoryRequest:   getEnvString("K8S_MEMORY_REQUEST", "4Gi"),
		K8sCPULimit:        getEnvString("K8S_CPU_LIMIT", "4"),
		K8sMemoryLimit:     getEnvString("K8S_MEMORY_LIMIT", "8Gi"),
		K8sImagePullSecret: os.Getenv("K8S_IMAGE_PULL_SECRET"),

		SigningKeyName: getEnvString("SIGNING_KEY_NAME", "nix-builder-proxy-1"),

		GradientEnabled:   os.Getenv("GRADIENT_ENABLED") == "true",
		GradientServerURL: os.Getenv("GRADIENT_SERVER_URL"),
		GradientPeersFile: os.Getenv("GRADIENT_PEERS_FILE"),
		// Defaults to both architectures the hetzner-ubuntu provisioner is
		// itself configured for by default (HCLOUD_SERVER_TYPE_ARM/_X86) -
		// override if this deployment only builds one architecture, or
		// uses a provisioner/config where that default doesn't apply.
		GradientArchitectures:       getEnvStringSlice("GRADIENT_ARCHITECTURES", []string{"x86_64-linux", "aarch64-linux"}),
		GradientMaxConcurrentBuilds: uint32(getEnvInt("GRADIENT_MAX_CONCURRENT_BUILDS", 5)),
	}
}

func getEnvInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return d
}
func getEnvString(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func getEnvDuration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if dur, err := time.ParseDuration(v); err == nil {
			return dur
		}
	}
	return d
}
func getEnvStringSlice(k string, d []string) []string {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return d
	}
	return out
}

func createProvisioner(config *Config) provisioner.Provisioner {
	switch config.Provisioner {
	case "hetzner-ubuntu":
		return hetznerubuntu.NewWithDir("provisioner/hetzner-ubuntu/terraform", hetznerubuntu.HetznerConfig{
			ServerTypeByArch: map[string]string{
				"aarch64": getEnvString("HCLOUD_SERVER_TYPE_ARM", "cax31"),
				"x86_64":  getEnvString("HCLOUD_SERVER_TYPE_X86", "cx33"),
			},
			Location: os.Getenv("HCLOUD_LOCATION"),
		})
	case "kubernetes":
		if config.K8sBuilderImage == "" {
			log.Fatalf("K8S_BUILDER_IMAGE is required for kubernetes provisioner")
		}
		p, err := kubernetesprov.New(kubernetesprov.Config{
			Namespace:       config.K8sNamespace,
			BuilderImage:    config.K8sBuilderImage,
			CPURequest:      config.K8sCPURequest,
			MemoryRequest:   config.K8sMemoryRequest,
			CPULimit:        config.K8sCPULimit,
			MemoryLimit:     config.K8sMemoryLimit,
			ImagePullSecret: config.K8sImagePullSecret,
		})
		if err != nil {
			log.Fatalf("Failed to create kubernetes provisioner: %v", err)
		}
		return p
	default:
		log.Fatalf("Unknown provisioner: %s", config.Provisioner)
		return nil
	}
}

func main() {
	config := LoadConfig()
	if config.Provisioner != "kubernetes" && config.HCloudToken == "" {
		log.Fatal("HCLOUD_TOKEN is required for hetzner provisioners")
	}
	if config.StoreHostIP == "" {
		log.Fatal("STORE_HOST is required")
	}

	proxyKey, _ := generateOrLoadKeyPair("proxy_ssh_key", "nix-proxy")
	storeKey, _ := generateOrLoadKeyPair("builder_store_key", "nix-store-access")

	log.Printf("=== ADD THIS TO STORE HOST AUTHORIZED_KEYS ===\n%s\n==============================================", string(storeKey.PublicKey))

	signer, err := nixproto.GenerateOrLoadStoreSigner("store_signing_key", config.SigningKeyName)
	if err != nil {
		log.Printf("Warning: failed to initialize store signing key: %v (outputs will not be signed)", err)
	} else {
		config.Signer = signer
		log.Printf("=== ADD TO STORE HOST nix.conf trusted-public-keys ===\n%s\n======================================================", signer.PublicKeyString())
	}

	// Create provisioner based on config
	prov := createProvisioner(config)
	log.Printf("Using provisioner: %s", prov.Name())

	// Create provisioner config
	provConfig := provisioner.Config{
		StoreHost:           config.StoreHostIP,
		StoreHostSSHPort:    config.StoreHostSSHPort,
		StoreHostUser:       config.StoreHostUser,
		StoreHostKey:        config.StoreHostKey,
		ProxyPublicKey:      proxyKey.PublicKey,
		BuilderStorePrivKey: storeKey.PrivateKey,
	}

	// Create pool
	poolConfig := provisioner.PoolConfig{
		Reuse:        config.BuilderReuse,
		MaxPoolSize:  config.BuilderPoolSize,
		DestroyDelay: config.BuilderDestroyDelay,
		IdleTimeout:  15 * time.Minute,
	}

	metricsDB, err := metrics.NewStore(getEnvString("METRICS_DB", "build_metrics.db"))
	if err != nil {
		log.Fatalf("Failed to open metrics database: %v", err)
	}
	defer metricsDB.Close()
	log.Printf("Build metrics database: %s", getEnvString("METRICS_DB", "build_metrics.db"))

	akm := NewAuthorizedKeysManager()
	pool := provisioner.NewPool(prov, poolConfig, provConfig, proxyKey.Signer)

	webPort := getEnvString("WEB_PORT", "8080")
	go startWebServer(pool, metricsDB, webPort)

	var gradientCtx context.Context
	var gradientCancel context.CancelFunc
	if config.GradientEnabled {
		if config.GradientServerURL == "" {
			log.Fatal("GRADIENT_SERVER_URL is required when GRADIENT_ENABLED=true")
		}
		gradientCtx, gradientCancel = context.WithCancel(context.Background())
		go startGradientWorker(gradientCtx, config, pool, storeKey)
	}

	listener, err := net.Listen("tcp", ":2222")
	if err != nil {
		log.Fatalf("Failed to listen on :2222: %v", err)
	}

	// Handle shutdown signals - destroy all builders before exiting
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("Received %v, shutting down and destroying all builders...", sig)
		listener.Close()
		if gradientCancel != nil {
			gradientCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pool.DestroyAll(ctx)
		os.Exit(0)
	}()

	log.Println("Nix ARM builder proxy listening on :2222")
	log.Println("Each BuildDerivation operation will be routed to its own VM")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go handleConnection(conn, pool, akm, proxyKey, config, storeKey, metricsDB)
	}
}

// --- Authorized Keys Manager ---

func (akm *AuthorizedKeysManager) IsAuthorized(key ssh.PublicKey) bool {
	akm.mu.RLock()
	defer akm.mu.RUnlock()
	fp := ssh.FingerprintSHA256(key)
	_, exists := akm.keys[fp]
	return exists
}

func NewAuthorizedKeysManager() *AuthorizedKeysManager {
	keyFile := os.Getenv("AUTHORIZED_KEYS_FILE")
	if keyFile == "" {
		keyFile = os.ExpandEnv("$HOME/.ssh/authorized_keys")
	}
	akm := &AuthorizedKeysManager{
		keys:    make(map[string]ssh.PublicKey),
		keyFile: keyFile,
	}
	akm.reloadKeys()
	go func() {
		for {
			time.Sleep(10 * time.Second)
			akm.reloadKeys()
		}
	}()
	return akm
}

func (akm *AuthorizedKeysManager) reloadKeys() {
	akm.mu.Lock()
	defer akm.mu.Unlock()
	f, err := os.Open(akm.keyFile)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey(scanner.Bytes())
		if err == nil {
			akm.keys[ssh.FingerprintSHA256(pubKey)] = pubKey
		}
	}
}

func generateOrLoadKeyPair(keyFile string, comment string) (*SSHKeyPair, error) {
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyFile, "-N", "", "-q", "-C", comment).Run()
	}
	priv, _ := os.ReadFile(keyFile)
	pub, _ := os.ReadFile(keyFile + ".pub")
	signer, _ := ssh.ParsePrivateKey(priv)
	return &SSHKeyPair{PrivateKey: priv, PublicKey: pub, Signer: signer}, nil
}

// generateOrLoadWorkerID returns this proxy's persistent Gradient worker
// UUID, generating and persisting a fresh one on first run. Mirrors
// generateOrLoadKeyPair's generate-if-missing pattern; a stable ID across
// restarts is what lets the Gradient server recognize a returning worker
// and matches any peer tokens pre-registered against it (see
// services.gradient.worker.peersFile / workerId in Gradient's own NixOS
// module).
func generateOrLoadWorkerID(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("generating Gradient worker ID: %v", err)
	}
	id := hex.EncodeToString(b[:])
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		log.Fatalf("persisting Gradient worker ID to %s: %v", path, err)
	}
	return id
}

// startGradientWorker dials the Gradient server and runs the worker
// session loop with reconnect-with-backoff, blocking until ctx is
// cancelled. Meant to run in its own goroutine (see main()) alongside the
// existing ssh-ng:// listener - both share the same *provisioner.Pool, so
// a build assigned via Gradient draws from the identical builder VMs.
func startGradientWorker(ctx context.Context, config *Config, pool *provisioner.Pool, storeKey *SSHKeyPair) {
	workerID := generateOrLoadWorkerID("gradient_worker_id")

	var creds gradientproto.PeerCredentials
	if config.GradientPeersFile != "" {
		var err error
		creds, err = gradientproto.LoadPeersFile(config.GradientPeersFile)
		if err != nil {
			log.Fatalf("loading GRADIENT_PEERS_FILE: %v", err)
		}
	}

	storeSSHConfig := &ssh.ClientConfig{
		User:            config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(storeKey.Signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	storeAddr := fmt.Sprintf("%s:%d", config.StoreHostIP, config.StoreHostSSHPort)

	executor := gradientproto.NewExecutor(pool, nil, nil)
	client := gradientproto.NewClient(gradientproto.ClientConfig{
		ServerURL:           config.GradientServerURL,
		PeerID:              workerID,
		Credentials:         creds,
		Architectures:       config.GradientArchitectures,
		MaxConcurrentBuilds: config.GradientMaxConcurrentBuilds,
	}, executor.HandleJob)

	const (
		minBackoff        = time.Second
		maxBackoff        = time.Minute
		healthySessionMin = 30 * time.Second // see backoff reset comment below
	)
	backoff := minBackoff
	for {
		// Dial the store host fresh for each session - a single
		// long-lived SSH connection held across reconnects/hours of
		// uptime is more failure-prone (idle timeouts, NAT rebinding)
		// than redialing alongside the WebSocket session it serves.
		storeConn, err := ssh.Dial("tcp", storeAddr, storeSSHConfig)
		if err != nil {
			log.Printf("gradient worker: connecting to store host: %v", err)
		} else {
			executor.SetStoreHostExec(gradientproto.NewSSHExecer(storeConn))
			log.Printf("gradient worker: connecting to %s as %s", config.GradientServerURL, workerID)
			start := time.Now()
			err = client.Run(ctx)
			storeConn.Close()
			if ctx.Err() != nil {
				return
			}
			log.Printf("gradient worker: session ended: %v", err)
			// Only reset backoff after a session that actually stayed up
			// for a while - a server that rejects every connection
			// attempt instantly (bad credentials, incompatible protocol
			// version) would otherwise have this loop hammer it at
			// minBackoff forever instead of backing off.
			if time.Since(start) >= healthySessionMin {
				backoff = minBackoff
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}
