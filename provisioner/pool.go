package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// archFromPlatform extracts the architecture component from a Nix platform string.
// e.g. "aarch64-linux" -> "aarch64", "x86_64-linux" -> "x86_64"
func archFromPlatform(platform string) string {
	parts := strings.SplitN(platform, "-", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// PoolConfig holds configuration for the builder pool
type PoolConfig struct {
	Reuse        bool
	MaxPoolSize  int // Per arch
	DestroyDelay time.Duration
	IdleTimeout  time.Duration
}

// BuilderRequest represents a request for a builder of a specific arch
type BuilderRequest struct {
	ResponseChan chan *Builder
	Ctx          context.Context
	Arch         string
}

// Pool manages a pool of builder VMs, routing requests by architecture.
// Implements the nixproto.BuilderProvider interface.
type Pool struct {
	mu             sync.Mutex
	builders       map[string]*Builder
	availablePool  map[string][]*Builder // arch -> available builders
	activeBuilders map[string]*Builder   // builderID -> Builder (currently in use)
	waiters        []*BuilderRequest     // pending requests waiting for a builder

	provisionSignal   chan bool
	provisioningCount map[string]int // arch -> count of in-flight provisioning goroutines

	provisioner Provisioner
	config      PoolConfig
	provConfig  Config
	sshSigner   ssh.Signer
}

// NewPool creates a new builder pool
func NewPool(p Provisioner, config PoolConfig, provConfig Config, sshSigner ssh.Signer) *Pool {
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 15 * time.Minute
	}

	pool := &Pool{
		builders:          make(map[string]*Builder),
		availablePool:     make(map[string][]*Builder),
		activeBuilders:    make(map[string]*Builder),
		waiters:           make([]*BuilderRequest, 0),
		provisionSignal:   make(chan bool, 10),
		provisioningCount: make(map[string]int),
		provisioner:       p,
		config:            config,
		provConfig:        provConfig,
		sshSigner:         sshSigner,
	}

	go pool.maintenanceWorker()
	return pool
}

// GetBuilder implements nixproto.BuilderProvider.
// platform is the Nix platform string from the derivation, e.g. "aarch64-linux".
func (bp *Pool) GetBuilder(drvPath, platform string) (*ssh.Client, string, error) {
	arch := archFromPlatform(platform)
	if arch == "" {
		return nil, "", fmt.Errorf("cannot determine architecture from platform %q", platform)
	}

	builder, err := bp.acquireBuilder(drvPath, arch)
	if err != nil {
		return nil, "", err
	}

	sshConfig := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(bp.sshSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", builder.IP+":22", sshConfig)
	if err != nil {
		bp.releaseBuilder(builder.ID)
		return nil, "", fmt.Errorf("failed to connect to builder %s: %w", builder.ID, err)
	}

	return client, builder.ID, nil
}

// ReleaseBuilder implements nixproto.BuilderProvider
func (bp *Pool) ReleaseBuilder(builderID string) {
	bp.releaseBuilder(builderID)
}

// acquireBuilder gets a builder of the requested arch from the pool or waits for one to be provisioned
func (bp *Pool) acquireBuilder(drvPath, arch string) (*Builder, error) {
	bp.mu.Lock()

	if builders := bp.availablePool[arch]; len(builders) > 0 {
		builder := builders[0]
		bp.availablePool[arch] = builders[1:]
		builder.Status = BuilderStatusInUse
		builder.LastUsed = time.Now()
		builder.DrvPath = drvPath
		bp.activeBuilders[builder.ID] = builder
		log.Printf("Assigned pooled %s builder %s to derivation %s", arch, builder.ID, drvPath)
		bp.mu.Unlock()
		return builder, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	request := &BuilderRequest{
		ResponseChan: make(chan *Builder, 1),
		Ctx:          ctx,
		Arch:         arch,
	}

	bp.waiters = append(bp.waiters, request)
	log.Printf("Derivation %s waiting for %s builder (waiters: %d, provisioning: %d)",
		drvPath, arch, len(bp.waiters), bp.provisioningCount[arch])
	bp.mu.Unlock()

	select {
	case bp.provisionSignal <- true:
	default:
	}

	select {
	case builder := <-request.ResponseChan:
		bp.mu.Lock()
		builder.Status = BuilderStatusInUse
		builder.LastUsed = time.Now()
		builder.DrvPath = drvPath
		bp.activeBuilders[builder.ID] = builder
		bp.mu.Unlock()
		log.Printf("Got %s builder %s for derivation %s", arch, builder.ID, drvPath)
		return builder, nil
	case <-ctx.Done():
		bp.mu.Lock()
		for i, req := range bp.waiters {
			if req == request {
				bp.waiters = append(bp.waiters[:i], bp.waiters[i+1:]...)
				break
			}
		}
		bp.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for %s builder provisioning", arch)
	}
}

// releaseBuilder returns a builder to the pool or destroys it
func (bp *Pool) releaseBuilder(builderID string) {
	bp.mu.Lock()
	builder, exists := bp.activeBuilders[builderID]
	if !exists {
		bp.mu.Unlock()
		return
	}

	delete(bp.activeBuilders, builderID)
	builder.DrvPath = ""
	builder.LastUsed = time.Now()

	log.Printf("Released %s builder %s", builder.Arch, builderID)

	// Hand off directly to a matching waiter
	for i, req := range bp.waiters {
		if req.Ctx.Err() != nil {
			continue
		}
		if req.Arch == builder.Arch {
			bp.waiters = append(bp.waiters[:i], bp.waiters[i+1:]...)
			log.Printf("Handing free %s builder %s directly to waiter", builder.Arch, builder.ID)
			req.ResponseChan <- builder
			bp.mu.Unlock()
			return
		}
	}

	// No matching waiter — pool or destroy
	if bp.config.Reuse && len(bp.availablePool[builder.Arch]) < bp.config.MaxPoolSize {
		builder.Status = BuilderStatusPooled
		bp.availablePool[builder.Arch] = append(bp.availablePool[builder.Arch], builder)
		log.Printf("Returned %s builder %s to pool (pool size: %d)",
			builder.Arch, builder.ID, len(bp.availablePool[builder.Arch]))
	} else {
		builder.Status = BuilderStatusDestroying
		go bp.scheduleDestruction(builder.ID)
	}
	bp.mu.Unlock()
}

func (bp *Pool) maintenanceWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-bp.provisionSignal:
			bp.checkAndScale()
		case <-ticker.C:
			bp.checkAndScale()
			bp.cleanupOldBuilders()
		}
	}
}

func (bp *Pool) checkAndScale() {
	bp.mu.Lock()

	// Count active (non-cancelled) waiters per arch
	archWaiters := make(map[string]int)
	for _, req := range bp.waiters {
		if req.Ctx.Err() == nil {
			archWaiters[req.Arch]++
		}
	}

	for arch, waiters := range archWaiters {
		provisioning := bp.provisioningCount[arch]
		if waiters > provisioning {
			needed := waiters - provisioning
			for i := 0; i < needed; i++ {
				bp.provisioningCount[arch]++
				go bp.provisionBackground(arch)
			}
		}
	}

	bp.mu.Unlock()
}

func (bp *Pool) provisionBackground(arch string) {
	builderID := generateID()
	provisionStart := time.Now()
	log.Printf("Worker: Provisioning new %s builder %s via %s", arch, builderID, bp.provisioner.Name())

	defer func() {
		bp.mu.Lock()
		bp.provisioningCount[arch]--
		bp.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	ip, err := bp.provisioner.Create(ctx, builderID, bp.provConfig, arch)
	if err != nil {
		log.Printf("Worker: Provisioning %s builder %s failed after %s: %v",
			arch, builderID, time.Since(provisionStart).Round(time.Second), err)
		return
	}

	log.Printf("Worker: %s builder %s fully provisioned in %s (IP: %s)",
		arch, builderID, time.Since(provisionStart).Round(time.Second), ip)

	builder := &Builder{
		ID:      builderID,
		IP:      ip,
		Arch:    arch,
		Created: time.Now(),
		Status:  BuilderStatusReady,
	}

	bp.mu.Lock()
	bp.builders[builderID] = builder

	// Hand off to a matching waiter
	for i, req := range bp.waiters {
		if req.Ctx.Err() != nil {
			continue
		}
		if req.Arch == arch {
			bp.waiters = append(bp.waiters[:i], bp.waiters[i+1:]...)
			log.Printf("Worker: Handing new %s builder %s to waiter", arch, builderID)
			req.ResponseChan <- builder
			bp.mu.Unlock()
			return
		}
	}

	// No matching waiter — pool it
	builder.Status = BuilderStatusPooled
	bp.availablePool[arch] = append(bp.availablePool[arch], builder)
	log.Printf("Worker: No waiter for %s, pooled builder %s", arch, builderID)
	bp.mu.Unlock()
}

func (bp *Pool) scheduleDestruction(builderID string) {
	time.Sleep(bp.config.DestroyDelay)

	bp.mu.Lock()
	builder, exists := bp.builders[builderID]
	if !exists || builder.Status != BuilderStatusDestroying {
		bp.mu.Unlock()
		return
	}
	delete(bp.builders, builderID)
	bp.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := bp.provisioner.Destroy(ctx, builderID); err != nil {
		log.Printf("Failed to destroy builder %s: %v", builderID, err)
	}
}

// DestroyAll destroys all tracked builders (active, pooled, and destroying).
func (bp *Pool) DestroyAll(ctx context.Context) {
	bp.mu.Lock()
	ids := make([]string, 0, len(bp.builders))
	for id := range bp.builders {
		ids = append(ids, id)
	}
	bp.builders = make(map[string]*Builder)
	bp.availablePool = make(map[string][]*Builder)
	bp.activeBuilders = make(map[string]*Builder)
	bp.mu.Unlock()

	if len(ids) == 0 {
		return
	}

	log.Printf("Destroying %d builders...", len(ids))

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(builderID string) {
			defer wg.Done()
			log.Printf("Destroying builder %s", builderID)
			if err := bp.provisioner.Destroy(ctx, builderID); err != nil {
				log.Printf("Failed to destroy builder %s: %v", builderID, err)
			} else {
				log.Printf("Destroyed builder %s", builderID)
			}
		}(id)
	}
	wg.Wait()
	log.Printf("All builders destroyed")
}

func (bp *Pool) cleanupOldBuilders() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	now := time.Now()
	for arch, builders := range bp.availablePool {
		var keep []*Builder
		for _, builder := range builders {
			if now.Sub(builder.LastUsed) > bp.config.IdleTimeout {
				log.Printf("Worker: Destroying idle %s builder %s", arch, builder.ID)
				builder.Status = BuilderStatusDestroying
				go func(id string) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					bp.provisioner.Destroy(ctx, id)
				}(builder.ID)
				delete(bp.builders, builder.ID)
			} else {
				keep = append(keep, builder)
			}
		}
		bp.availablePool[arch] = keep
	}
}
