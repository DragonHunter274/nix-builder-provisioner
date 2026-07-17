package nixproto

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// BuilderProvider is an interface for obtaining builders for derivation builds
type BuilderProvider interface {
	// GetBuilder provisions or retrieves a builder for the given derivation.
	// platform is the Nix platform string from the derivation (e.g. "aarch64-linux").
	// Returns the builder's SSH client connection.
	GetBuilder(drvPath, platform string) (*ssh.Client, string, error)

	// ReleaseBuilder releases a builder back to the pool or destroys it
	ReleaseBuilder(builderID string)
}

// MetricsRecorder records build metrics. Implementations must be safe for concurrent use.
type MetricsRecorder interface {
	RecordBuild(drvPath string, env map[string]string, platform string, builderID string, status uint64, errorMsg string, startTime uint64, stopTime uint64, cpuUserUsec *uint64, cpuSystemUsec *uint64, peakMemoryBytes *uint64)
}

// ProxyConfig holds configuration for the Nix protocol proxy
type ProxyConfig struct {
	// StoreDir is the Nix store directory (usually /nix/store)
	StoreDir string

	// Store Host connection info (for NarFromPath)
	StoreHostAddr string     // host:port for SSH
	StoreHostUser string     // SSH user
	StoreHostKey  ssh.Signer // SSH private key for auth

	// Metrics is an optional recorder for build metrics
	Metrics MetricsRecorder

	// Signer, if set, signs build outputs and registers the signatures with the
	// store host after each successful build. The store host must have the
	// corresponding public key in nix.conf trusted-public-keys, and the
	// StoreHostUser must be in trusted-users.
	Signer *StoreSigner

	// Per-user input store settings. When UserStoreEnabled is true:
	//   - AddMultipleToStore uploads land in a private store at
	//     UserStoreRoot/<sanitized-fingerprint>/ on the store host.
	//   - Before each BuildDerivation the private store's contents are
	//     copied into the shared store (just-in-time), so builders can
	//     access them normally. They are cleaned up by Nix's GC in due
	//     course because they have no permanent GC root.
	// Requires /nix/user-inputs/ (or UserStoreRoot) writable by StoreHostUser.
	UserStoreEnabled   bool
	UserFingerprint    string // SSH key fingerprint of the connected client
	UserStoreRoot      string // Base dir on store host, e.g. /nix/user-inputs
}

// Proxy handles Nix daemon protocol connections and routes operations
type Proxy struct {
	config   ProxyConfig
	provider BuilderProvider
}

// NewProxy creates a new Nix protocol proxy
func NewProxy(config ProxyConfig, provider BuilderProvider) *Proxy {
	return &Proxy{
		config:   config,
		provider: provider,
	}
}

// HandleConnection handles a single Nix daemon protocol connection
// The reader/writer should be from an SSH channel running "nix-daemon --stdio"
func (p *Proxy) HandleConnection(r io.Reader, w io.Writer) error {
	return p.HandleConnectionWithContext(context.Background(), r, w)
}

// HandleConnectionWithContext handles a connection with cancellation support
func (p *Proxy) HandleConnectionWithContext(ctx context.Context, r io.Reader, w io.Writer) error {
	conn := NewConnWithContext(ctx, r, w)

	// Perform handshake
	if err := conn.Handshake(); err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	// Send ClientHandshakeInfo (version string, trust, StderrLast)
	if err := conn.PostHandshake(); err != nil {
		return fmt.Errorf("post-handshake failed: %w", err)
	}

	log.Printf("Nix connection established, protocol version %d.%d",
		(conn.Version>>8)&0xff, conn.Version&0xff)

	// Main operation loop
	opCount := 0
	for {
		opCount++
		op, err := conn.ReadOp()
		if err != nil {
			if err == io.EOF {
				log.Printf("Client disconnected (after %d operations)", opCount-1)
				return nil
			}
			return fmt.Errorf("reading operation #%d: %w", opCount, err)
		}

		log.Printf(">>> Operation #%d: %s (%d)", opCount, op, op)

		if err := p.handleOperation(conn, op); err != nil {
			log.Printf("Operation #%d %s failed: %v", opCount, op, err)
			// Don't return - try to continue with next operation
			// The error should have been sent to the client already
		}

		log.Printf("<<< Operation #%d %s completed, flushing", opCount, op)

		// Ensure all response data is flushed before waiting for next operation
		if err := conn.Flush(); err != nil {
			return fmt.Errorf("flushing after operation #%d: %w", opCount, err)
		}

		log.Printf("--- Operation #%d %s done, waiting for next", opCount, op)
	}
}

func (p *Proxy) handleOperation(conn *Conn, op WorkerOp) error {
	switch op {
	case OpSetOptions:
		return p.handleSetOptions(conn)

	case OpIsValidPath:
		return p.handleIsValidPath(conn)

	case OpQueryValidPaths:
		return p.handleQueryValidPaths(conn)

	case OpQueryPathInfo:
		return p.handleQueryPathInfo(conn)

	case OpBuildDerivation:
		return p.handleBuildDerivation(conn)

	case OpAddToStore:
		return p.handleAddToStore(conn)

	case OpAddMultipleToStore:
		return p.handleAddMultipleToStore(conn)

	case OpNarFromPath:
		return p.handleNarFromPath(conn)

	case OpQueryReferrers:
		return p.handleQueryReferrers(conn)

	case OpAddTempRoot:
		return p.handleAddTempRoot(conn)

	case OpQueryMissing:
		return p.handleQueryMissing(conn)

	default:
		// For unhandled operations, return an error
		log.Printf("Unhandled operation: %s", op)
		return conn.StopWorkWithError(fmt.Sprintf("operation %s not implemented", op), 1)
	}
}

// handleSetOptions handles the SetOptions operation
func (p *Proxy) handleSetOptions(conn *Conn) error {
	// Read all the options (we mostly ignore them)
	_, err := ReadBool(conn.Reader()) // keepFailed
	if err != nil {
		return err
	}
	_, err = ReadBool(conn.Reader()) // keepGoing
	if err != nil {
		return err
	}
	_, err = ReadBool(conn.Reader()) // tryFallback
	if err != nil {
		return err
	}
	_, err = ReadUint64(conn.Reader()) // verbosity
	if err != nil {
		return err
	}
	_, err = ReadUint64(conn.Reader()) // maxBuildJobs
	if err != nil {
		return err
	}
	_, err = ReadUint64(conn.Reader()) // maxSilentTime
	if err != nil {
		return err
	}

	minor := conn.Version & 0xff
	if minor >= 2 {
		_, err = ReadBool(conn.Reader()) // useBuildHook (obsolete)
		if err != nil {
			return err
		}
	}
	if minor >= 4 {
		_, err = ReadUint64(conn.Reader()) // verboseBuild
		if err != nil {
			return err
		}
		_, err = ReadUint64(conn.Reader()) // logType (obsolete)
		if err != nil {
			return err
		}
		_, err = ReadUint64(conn.Reader()) // printBuildTrace (obsolete)
		if err != nil {
			return err
		}
	}
	if minor >= 6 {
		_, err = ReadUint64(conn.Reader()) // buildCores
		if err != nil {
			return err
		}
	}
	if minor >= 10 {
		_, err = ReadBool(conn.Reader()) // useSubstitutes
		if err != nil {
			return err
		}
	}
	if minor >= 12 {
		// Read overrides map
		count, err := ReadUint64(conn.Reader())
		if err != nil {
			return err
		}
		for i := uint64(0); i < count; i++ {
			_, err = ReadString(conn.Reader()) // key
			if err != nil {
				return err
			}
			_, err = ReadString(conn.Reader()) // value
			if err != nil {
				return err
			}
		}
	}

	// SetOptions doesn't send any response, just returns
	return conn.StopWork()
}

// handleIsValidPath checks if a path exists in the store by querying the Store Host
func (p *Proxy) handleIsValidPath(conn *Conn) error {
	path, err := ReadString(conn.Reader())
	if err != nil {
		return err
	}

	log.Printf("IsValidPath: %s", path)

	valid := false
	if p.config.StoreHostAddr != "" && p.config.StoreHostKey != nil {
		pathInfo, err := p.queryStoreHostPathInfo(path)
		if err == nil && pathInfo != nil {
			valid = true
		}
	}

	log.Printf("IsValidPath: %s -> %v", path, valid)

	if err := conn.StopWork(); err != nil {
		return err
	}
	return WriteBool(conn.Writer(), valid)
}

// handleQueryValidPaths queries which paths are valid
// It first checks the store host, then tries to substitute missing paths from configured substituters
func (p *Proxy) handleQueryValidPaths(conn *Conn) error {
	paths, err := ReadStrings(conn.Reader())
	if err != nil {
		return err
	}
	minor := conn.Version & 0xff
	substitute := false
	if minor >= 27 {
		substitute, err = ReadBool(conn.Reader()) // substitute flag
		if err != nil {
			return err
		}
	}

	log.Printf("QueryValidPaths: %d paths (substitute=%v)", len(paths), substitute)

	// Query the store host to see which paths actually exist
	validPaths := []string{}

	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		log.Printf("QueryValidPaths: Store host not configured, returning no valid paths")
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}

	// Connect to store host
	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		log.Printf("QueryValidPaths: failed to connect to store host: %v, returning no valid paths", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		log.Printf("QueryValidPaths: failed to create session: %v", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}
	defer session.Close()

	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()

	// Ensure stdin is closed when we're done to signal nix-daemon to exit
	defer stdin.Close()

	// Monitor context for cancellation and close session if client disconnects
	// Use a done channel to stop the goroutine when the function returns normally
	ctx := conn.Context()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			log.Printf("QueryValidPaths: client disconnected, aborting store operation")
			stdin.Close()
			session.Close()
		case <-done:
			// Function completed normally, nothing to do
		}
	}()

	if err := session.Start("nix-daemon --stdio"); err != nil {
		log.Printf("QueryValidPaths: failed to start nix-daemon: %v", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}

	storeConn := NewConn(stdout, stdin)

	// Handshake with store
	if err := handshakeWithBuilder(storeConn); err != nil {
		log.Printf("QueryValidPaths: store handshake failed: %v", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}

	// Send SetOptions
	if err := sendSetOptions(storeConn, clientLogSink(conn)); err != nil {
		log.Printf("QueryValidPaths: SetOptions failed: %v", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), validPaths)
	}

	// Step 1: Query which paths are already valid on the store (with substitute=true to let store handle closure)
	validPaths, err = p.queryValidPathsOnStoreWithSubstitute(storeConn, clientLogSink(conn), paths, substitute)
	if err != nil {
		log.Printf("QueryValidPaths: querying store failed: %v", err)
		if err := conn.StopWork(); err != nil {
			return err
		}
		return WriteStrings(conn.Writer(), []string{})
	}

	log.Printf("QueryValidPaths: %d/%d paths valid on store (with substitute=%v)", len(validPaths), len(paths), substitute)

	// Log which paths are not valid (client will need to upload these)
	if len(validPaths) < len(paths) {
		validSet := make(map[string]bool)
		for _, p := range validPaths {
			validSet[p] = true
		}
		notValidCount := 0
		for _, p := range paths {
			if !validSet[p] {
				notValidCount++
				if notValidCount <= 20 { // Only log first 20
					log.Printf("QueryValidPaths: NOT valid (client must upload): %s", StorePathBasename(p))
				}
			}
		}
		if notValidCount > 20 {
			log.Printf("QueryValidPaths: ... and %d more paths not valid", notValidCount-20)
		}
	}

	if err := conn.StopWork(); err != nil {
		return err
	}
	return WriteStrings(conn.Writer(), validPaths)
}

// queryValidPathsOnStoreWithSubstitute queries which paths are valid on the store using the nix-daemon protocol
// If substitute is true, the store will attempt to substitute missing paths from configured substituters
func (p *Proxy) queryValidPathsOnStoreWithSubstitute(storeConn *Conn, sink StderrSink, paths []string, substitute bool) ([]string, error) {
	if err := storeConn.WriteOp(OpQueryValidPaths); err != nil {
		return nil, fmt.Errorf("writing op: %w", err)
	}

	if err := WriteStrings(storeConn.Writer(), paths); err != nil {
		return nil, fmt.Errorf("writing paths: %w", err)
	}

	minor := storeConn.Version & 0xff
	if minor >= 27 {
		if err := WriteBool(storeConn.Writer(), substitute); err != nil {
			return nil, fmt.Errorf("writing substitute flag: %w", err)
		}
	}

	if err := storeConn.Flush(); err != nil {
		return nil, fmt.Errorf("flushing: %w", err)
	}

	if err := readStderrStream(storeConn, sink); err != nil {
		return nil, fmt.Errorf("reading stderr: %w", err)
	}

	validPaths, err := ReadStrings(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return validPaths, nil
}

// handleQueryPathInfo returns info about a store path by querying the Store Host
func (p *Proxy) handleQueryPathInfo(conn *Conn) error {
	path, err := ReadString(conn.Reader())
	if err != nil {
		return err
	}

	log.Printf("QueryPathInfo: %s", path)

	minor := conn.Version & 0xff

	// Check if Store Host is configured
	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		log.Printf("QueryPathInfo: Store Host not configured, returning not found")
		conn.StopWork()
		if minor >= 17 {
			return WriteBool(conn.Writer(), false)
		}
		return conn.StopWorkWithError("path not found", 1)
	}

	// Query Store Host for path info
	pathInfo, err := p.queryStoreHostPathInfo(path)
	if err != nil {
		log.Printf("QueryPathInfo: failed to query Store Host: %v", err)
		conn.StopWork()
		if minor >= 17 {
			return WriteBool(conn.Writer(), false)
		}
		return conn.StopWorkWithError("path not found", 1)
	}

	if pathInfo == nil {
		log.Printf("QueryPathInfo: path not found on Store Host: %s", path)
		conn.StopWork()
		if minor >= 17 {
			return WriteBool(conn.Writer(), false)
		}
		return conn.StopWorkWithError("path not found", 1)
	}

	log.Printf("QueryPathInfo: found on Store Host: %s (narSize=%d)", path, pathInfo.NarSize)

	conn.StopWork()

	// Write response for protocol >= 1.17
	if minor >= 17 {
		if err := WriteBool(conn.Writer(), true); err != nil { // path is valid
			return err
		}
	}

	// Write ValidPathInfo
	// deriver (optional path)
	if pathInfo.Deriver != "" {
		if err := WriteString(conn.Writer(), pathInfo.Deriver); err != nil {
			return err
		}
	} else {
		if err := WriteString(conn.Writer(), ""); err != nil {
			return err
		}
	}

	// narHash
	if err := WriteString(conn.Writer(), pathInfo.NarHash); err != nil {
		return err
	}

	// references
	if err := WriteStrings(conn.Writer(), pathInfo.References); err != nil {
		return err
	}

	// registrationTime
	if err := WriteUint64(conn.Writer(), pathInfo.RegistrationTime); err != nil {
		return err
	}

	// narSize
	if err := WriteUint64(conn.Writer(), pathInfo.NarSize); err != nil {
		return err
	}

	// Protocol >= 1.16: ultimate flag
	if minor >= 16 {
		if err := WriteBool(conn.Writer(), pathInfo.Ultimate); err != nil {
			return err
		}

		// sigs
		if err := WriteStrings(conn.Writer(), pathInfo.Sigs); err != nil {
			return err
		}

		// ca (content address)
		if err := WriteString(conn.Writer(), pathInfo.CA); err != nil {
			return err
		}
	}

	return nil
}

// handleBuildDerivation is the key operation - this is where we route to a VM
func (p *Proxy) handleBuildDerivation(conn *Conn) error {
	// Read derivation path
	drvPath, err := ReadString(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading drv path: %w", err)
	}

	// Read the derivation itself
	drv, err := ReadBasicDerivation(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading derivation: %w", err)
	}

	// Read build mode
	buildModeVal, err := ReadUint64(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading build mode: %w", err)
	}
	buildMode := BuildMode(buildModeVal)

	log.Printf("BuildDerivation: %s (platform: %s, builder: %s, mode: %d)",
		drvPath, drv.Platform, drv.Builder, buildMode)
	log.Printf("  Outputs: %v", drv.Outputs)
	log.Printf("  InputSrcs: %d paths", len(drv.InputSrcs))

	// Get a builder for this derivation, routing by the derivation's target platform
	builderSSH, builderID, err := p.provider.GetBuilder(drvPath, drv.Platform)
	if err != nil {
		conn.StopWorkWithError(fmt.Sprintf("failed to get builder: %v", err), 1)
		return err
	}
	defer p.provider.ReleaseBuilder(builderID)

	log.Printf("Got builder %s for derivation %s", builderID, drvPath)

	// If per-user input stores are enabled, copy the user's private store paths
	// into the shared store so the builder can access them.
	p.importUserInputsToSharedStore()

	// Connect to nix-daemon on the builder
	result, err := p.executeBuildOnBuilder(builderSSH, conn, drvPath, drv, buildMode)
	if err != nil {
		log.Printf("Build failed on builder: %v", err)
		conn.StopWorkWithError(fmt.Sprintf("build failed: %v", err), 1)
		return err
	}

	// Sign outputs and register signatures with the store (best-effort)
	p.signAndRegisterOutputs(result)

	// Record build metrics
	if p.config.Metrics != nil {
		peakMem := collectPeakMemory(builderSSH)
		p.config.Metrics.RecordBuild(
			drvPath, drv.Env, drv.Platform, builderID,
			uint64(result.Status), result.ErrorMsg,
			result.StartTime, result.StopTime,
			result.CpuUser, result.CpuSystem,
			peakMem,
		)
	}

	// Send result to client

	// 1. Signal end of log stream
	// log.Printf("DEBUG: Sending StderrLast (0x%x)", StderrLast)
	if err := conn.StopWork(); err != nil {
		return fmt.Errorf("StopWork failed: %w", err)
	}

	// 2. IMMEDIATELY write the result (NO separator uint64)
	// log.Printf("DEBUG: Writing BuildResult...")
	if err := WriteBuildResult(conn.Writer(), result, conn.Version); err != nil {
		return fmt.Errorf("WriteBuildResult failed: %w", err)
	}

	// 3. Flush and exit
	// log.Printf("DEBUG: Flushing BuildResult to client...")
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush after BuildResult failed: %w", err)
	}
	// log.Printf("DEBUG: Build result sent and flushed successfully")
	return nil
}

// executeBuildOnBuilder runs the build on a remote builder via SSH. Thin
// adapter over ExecuteBuild that forwards builder logs to the nix-wire
// client connection that originated this build (the ssh-ng:// path).
func (p *Proxy) executeBuildOnBuilder(builderSSH *ssh.Client, clientConn *Conn, drvPath string, drv *BasicDerivation, buildMode BuildMode) (*BuildResult, error) {
	ctx := context.Background()
	if clientConn != nil {
		ctx = clientConn.Context()
	}
	return ExecuteBuild(ctx, builderSSH, drvPath, drv, buildMode, clientLogSink(clientConn))
}

// StderrSink receives nix-daemon stderr-stream events (log lines and
// activity markers) forwarded while a build/operation is in progress on a
// second (builder- or store-host-facing) connection. *Conn satisfies this
// directly - forwarding straight through to a nix-wire client - which is
// how the ssh-ng:// proxy path uses it. Other callers (e.g. a Gradient
// worker) can implement a partial adapter that only cares about log lines
// and no-ops the activity methods, since those have no equivalent in
// Gradient's own protocol.
type StderrSink interface {
	WriteStderrLog(msg string) error
	WriteStderrStartActivity(act, level, type_ uint64, text string, fields []ActivityField, parent uint64) error
	WriteStderrStopActivity(act uint64) error
	WriteStderrResult(act, type_ uint64, fields []ActivityField) error
}

// clientLogSink adapts a nix-wire client connection into the StderrSink
// used by handshakeWithBuilder/sendSetOptions/readStderrStream/ExecuteBuild.
// Returns a literal nil StderrSink when conn is nil - never a non-nil
// interface wrapping a nil *Conn - so `sink != nil` checks downstream stay
// correct and call sites that don't have a client connection to forward to
// (e.g. internal store-host operations like signing or AddMultipleToStore)
// can pass it straight through.
func clientLogSink(conn *Conn) StderrSink {
	if conn == nil {
		return nil
	}
	return conn
}

// ExecuteBuild runs a single BuildDerivation on builderSSH's nix-daemon and
// returns the BuildResult. It is the protocol-agnostic core shared by the
// ssh-ng:// proxy path (via executeBuildOnBuilder) and any other caller that
// wants to drive a build on an already-provisioned builder without speaking
// the nix-wire protocol itself (e.g. a Gradient worker client) - sink
// receives stderr-stream events as they arrive; pass nil to discard them.
// ctx is watched for cancellation: if it's done before the build completes,
// the builder session is torn down and the build aborted.
func ExecuteBuild(ctx context.Context, builderSSH *ssh.Client, drvPath string, drv *BasicDerivation, buildMode BuildMode, sink StderrSink) (*BuildResult, error) {
	log.Printf("DEBUG ExecuteBuild: drvPath=%s, platform=%s, outputs=%d", drvPath, drv.Platform, len(drv.Outputs))
	// log.Printf("DEBUG: Opening SSH session to builder")

	// Open a session to run nix-daemon --stdio
	session, err := builderSSH.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("getting stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("getting stdout pipe: %w", err)
	}

	// Abort the build if the caller's context is cancelled (e.g. client
	// disconnect on the ssh-ng path, or AbortJob on the Gradient path).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			log.Printf("ExecuteBuild: context cancelled, aborting build on builder")
			stdin.Close()
			session.Close()
		case <-done:
		}
	}()

	// log.Printf("DEBUG: Starting nix-daemon --stdio on builder")

	// Start nix-daemon in stdio mode
	// Use exec to avoid any shell startup scripts (.bashrc) that might output text
	if err := session.Start("exec nix-daemon --stdio"); err != nil {
		return nil, fmt.Errorf("starting nix-daemon: %w", err)
	}

	// log.Printf("DEBUG: nix-daemon started, beginning handshake")

	// Create a connection to the builder's nix-daemon
	builderConn := NewConn(stdout, stdin)

	// Perform handshake with builder
	if err := handshakeWithBuilder(builderConn); err != nil {
		return nil, fmt.Errorf("builder handshake failed: %w", err)
	}

	log.Printf("DEBUG: Handshake with builder complete")

	// Send SetOptions first (required before other operations)
	if err := sendSetOptions(builderConn, sink); err != nil {
		return nil, fmt.Errorf("sending options: %w", err)
	}

	log.Printf("DEBUG: SetOptions complete, sending BuildDerivation")

	if err := builderConn.WriteOp(OpBuildDerivation); err != nil {
		return nil, fmt.Errorf("writing BuildDerivation op: %w", err)
	}

	// Write derivation path
	if err := WriteString(builderConn.Writer(), drvPath); err != nil {
		return nil, fmt.Errorf("writing drv path: %w", err)
	}

	// Write derivation
	if err := WriteBasicDerivation(builderConn.Writer(), drv); err != nil {
		return nil, fmt.Errorf("writing derivation: %w", err)
	}

	// Write build mode
	if err := WriteUint64(builderConn.Writer(), uint64(buildMode)); err != nil {
		return nil, fmt.Errorf("writing build mode: %w", err)
	}

	// Flush to send the operation
	if err := builderConn.Flush(); err != nil {
		return nil, fmt.Errorf("flushing BuildDerivation: %w", err)
	}

	log.Printf("DEBUG: BuildDerivation sent, waiting for build to complete...")

	// Read stderr stream and wait for completion
	// The builder will stream logs, then send STDERR_LAST, then the result
	if err := readStderrStream(builderConn, sink); err != nil {
		return nil, fmt.Errorf("reading stderr stream: %w", err)
	}

	// Read the build result
	result, err := ReadBuildResult(builderConn.Reader(), builderConn.Version)
	if err != nil {
		return nil, fmt.Errorf("reading build result: %w", err)
	}

	log.Printf("Build result: status=%d, error=%s, outputs=%d", result.Status, result.ErrorMsg, len(result.BuiltOutputs))

	// Log build result details
	isSuccess := result.Status == BuildResultBuilt ||
		result.Status == BuildResultSubstituted ||
		result.Status == BuildResultAlreadyValid
	_ = isSuccess // used below

	// If BuiltOutputs is empty but the build was successful (e.g. AlreadyValid),
	// we need to populate it for the client (protocol 1.35+)
	if isSuccess && len(result.BuiltOutputs) == 0 {
		// log.Printf("DEBUG: Successful build (status=%d) with empty BuiltOutputs, populating...", result.Status)
		result.BuiltOutputs = make(map[string]Realisation)

		// Compute the proper derivation hash from the derivation itself
		// The client expects the full SHA256 of the derivation ATerm
		drvHash := ComputeDerivationHash(drv)

		for name, output := range drv.Outputs {
			// Construct the ID using the computed full hash
			id := fmt.Sprintf("sha256:%s!%s", drvHash, name)

			// We assume the output path in the derivation is correct (especially for input-addressed)
			// TODO: For CA derivations, we might need more info, but AlreadyValid implies it's known
			real := Realisation{
				ID:                    id,
				OutPath:               output.Path,
				Signatures:            []string{},
				DependentRealisations: make(map[string]string),
			}
			result.BuiltOutputs[id] = real
			// log.Printf("DEBUG: Synthesized output %s -> %s", id, output.Path)
		}
	}

	return result, nil
}

// handshakeWithBuilder performs handshake as a client to the builder's nix-daemon
func handshakeWithBuilder(conn *Conn) error {
	// log.Printf("DEBUG: Writing client magic + version to builder")

	// Write client magic and version together
	if err := WriteUint64(conn.Writer(), ClientMagic); err != nil {
		return fmt.Errorf("writing client magic: %w", err)
	}
	if err := WriteUint64(conn.Writer(), ProtocolVersion); err != nil {
		return fmt.Errorf("writing client version: %w", err)
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flushing magic+version: %w", err)
	}

	// log.Printf("DEBUG: Reading server magic + version")

	// Read server magic
	serverMagic, err := ReadUint64(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading server magic: %w", err)
	}
	if serverMagic != ServerMagic {
		return fmt.Errorf("invalid server magic: got %x, expected %x", serverMagic, ServerMagic)
	}

	// Read server's protocol version
	serverVersion, err := ReadUint64(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading server version: %w", err)
	}

	// Use minimum version
	conn.Version = serverVersion
	if ProtocolVersion < serverVersion {
		conn.Version = ProtocolVersion
	}

	minor := conn.Version & 0xff

	// Feature exchange (protocol 1.38+)
	// log.Printf("DEBUG: Sending client features (empty)")
	if err := WriteStrings(conn.Writer(), []string{}); err != nil {
		return fmt.Errorf("writing client features: %w", err)
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flushing features: %w", err)
	}

	_, err = ReadStrings(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading server features: %w", err)
	}
	// log.Printf("DEBUG: Server features: %v", serverFeatures)

	// For protocol >= 1.14, send CPU affinity and reserve space (obsolete but required)
	if minor >= 14 {
		// log.Printf("DEBUG: Sending affinity + reserveSpace")
		if err := WriteUint64(conn.Writer(), 0); err != nil { // cpu affinity
			return err
		}
		if err := WriteUint64(conn.Writer(), 0); err != nil { // reserve space
			return err
		}
		if err := conn.Flush(); err != nil {
			return fmt.Errorf("flushing affinity: %w", err)
		}
	}

	// Read ClientHandshakeInfo from server
	// For protocol >= 1.33: daemon version string
	if minor >= 33 {
		// log.Printf("DEBUG: Reading daemon version string")
		daemonVersion, err := ReadString(conn.Reader())
		if err != nil {
			return fmt.Errorf("reading daemon version: %w", err)
		}
		log.Printf("Builder daemon version: %s", daemonVersion)
	}

	// For protocol >= 1.35: read trust status
	if minor >= 35 {
		// log.Printf("DEBUG: Reading trust status")
		trusted, err := ReadUint64(conn.Reader())
		if err != nil {
			return fmt.Errorf("reading trust status: %w", err)
		}
		conn.ClientTrusted = trusted != 0
		log.Printf("Builder trusts us: %v", conn.ClientTrusted)
	}

	// Read StderrLast to complete handshake
	log.Printf("DEBUG: Reading StderrLast from builder")
	marker, err := ReadUint64(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading StderrLast: %w", err)
	}
	if marker != StderrLast {
		return fmt.Errorf("expected StderrLast (0x%x), got 0x%x", StderrLast, marker)
	}

	// log.Printf("DEBUG: Builder handshake complete")
	return nil
}

// sendSetOptions sends the SetOptions operation to initialize the connection
func sendSetOptions(conn *Conn, sink StderrSink) error {
	// log.Printf("DEBUG: Sending SetOptions to builder")

	if err := conn.WriteOp(OpSetOptions); err != nil {
		return err
	}

	// Write options
	WriteBool(conn.Writer(), false) // keepFailed
	WriteBool(conn.Writer(), true)  // keepGoing
	WriteBool(conn.Writer(), true)  // tryFallback
	WriteUint64(conn.Writer(), 3)   // verbosity (Info)
	WriteUint64(conn.Writer(), 1)   // maxBuildJobs
	WriteUint64(conn.Writer(), 0)   // maxSilentTime (no limit)

	minor := conn.Version & 0xff
	if minor >= 2 {
		WriteBool(conn.Writer(), false) // useBuildHook (obsolete)
	}
	if minor >= 4 {
		WriteUint64(conn.Writer(), 0) // verboseBuild
		WriteUint64(conn.Writer(), 0) // logType (obsolete)
		WriteUint64(conn.Writer(), 0) // printBuildTrace (obsolete)
	}
	if minor >= 6 {
		WriteUint64(conn.Writer(), 0) // buildCores (0 = use all)
	}
	if minor >= 10 {
		WriteBool(conn.Writer(), true) // useSubstitutes
	}
	if minor >= 12 {
		WriteUint64(conn.Writer(), 0) // empty overrides map
	}

	// Flush to send the operation
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flushing SetOptions: %w", err)
	}

	// log.Printf("DEBUG: Waiting for SetOptions response")

	// SetOptions has no response except STDERR_LAST
	if err := readStderrStream(conn, sink); err != nil {
		return err
	}

	// log.Printf("DEBUG: SetOptions complete")
	return nil
}

// readStderrStream reads stderr messages until STDERR_LAST
func readStderrStream(conn *Conn, sink StderrSink) error {
	for {
		marker, err := ReadUint64(conn.Reader())
		if err != nil {
			return fmt.Errorf("reading stderr marker: %w", err)
		}

		switch marker {
		case StderrLast:
			return nil

		case StderrNext:
			msg, err := ReadString(conn.Reader())
			if err != nil {
				return err
			}
			log.Printf("[builder] %s", msg)
			if sink != nil {
				// Forward log to client
				if err := sink.WriteStderrLog(msg); err != nil {
					log.Printf("Failed to forward log to client: %v", err)
					// Verify if we should stop here
				}
			}

		case StderrError:
			minor := conn.Version & 0xff
			if minor >= 26 {
				errType, _ := ReadString(conn.Reader())
				ReadUint64(conn.Reader()) // level
				ReadString(conn.Reader()) // name
				msg, _ := ReadString(conn.Reader())
				havePos, _ := ReadUint64(conn.Reader())
				if havePos != 0 {
					// Read position info: file, line, column
					ReadString(conn.Reader()) // file
					ReadUint64(conn.Reader()) // line
					ReadUint64(conn.Reader()) // column
				}
				tracesCount, _ := ReadUint64(conn.Reader())
				for i := uint64(0); i < tracesCount; i++ {
					haveTracePos, _ := ReadUint64(conn.Reader())
					if haveTracePos != 0 {
						ReadString(conn.Reader()) // file
						ReadUint64(conn.Reader()) // line
						ReadUint64(conn.Reader()) // column
					}
					ReadString(conn.Reader()) // hint
				}
				return fmt.Errorf("builder error (%s): %s", errType, msg)
			} else {
				msg, _ := ReadString(conn.Reader())
				status, _ := ReadUint64(conn.Reader())
				return fmt.Errorf("builder error (status %d): %s", status, msg)
			}

		case StderrWrite:
			// Data being written - read and discard for now
			_, err := ReadBytes(conn.Reader())
			if err != nil {
				return err
			}

		case StderrRead:
			// Builder needs data - this shouldn't happen for BuildDerivation
			return fmt.Errorf("unexpected StderrRead")

		case StderrStartActivity:
			act, err := ReadUint64(conn.Reader()) // act
			if err != nil {
				return err
			}
			lvl, err := ReadUint64(conn.Reader()) // lvl
			if err != nil {
				return err
			}
			type_, err := ReadUint64(conn.Reader()) // type
			if err != nil {
				return err
			}
			text, err := ReadString(conn.Reader()) // text
			if err != nil {
				return err
			}
			fieldsCount, err := ReadUint64(conn.Reader())
			if err != nil {
				return err
			}

			var fields []ActivityField
			for i := uint64(0); i < fieldsCount; i++ {
				fieldType, err := ReadUint64(conn.Reader())
				if err != nil {
					return err
				}
				var intVal uint64
				var strVal string
				if fieldType == 0 {
					intVal, err = ReadUint64(conn.Reader())
				} else {
					strVal, err = ReadString(conn.Reader())
				}
				if err != nil {
					return err
				}
				fields = append(fields, ActivityField{Type: fieldType, IntVal: intVal, StrVal: strVal})
			}
			parent, err := ReadUint64(conn.Reader()) // parent
			if err != nil {
				return err
			}

			if sink != nil {
				sink.WriteStderrStartActivity(act, lvl, type_, text, fields, parent)
			}

		case StderrStopActivity:
			act, err := ReadUint64(conn.Reader()) // act
			if err != nil {
				return err
			}
			if sink != nil {
				sink.WriteStderrStopActivity(act)
			}

		case StderrResult:
			act, err := ReadUint64(conn.Reader()) // act
			if err != nil {
				return err
			}
			type_, err := ReadUint64(conn.Reader()) // type
			if err != nil {
				return err
			}
			fieldsCount, err := ReadUint64(conn.Reader())
			if err != nil {
				return err
			}
			var fields []ActivityField
			for i := uint64(0); i < fieldsCount; i++ {
				fieldType, err := ReadUint64(conn.Reader())
				if err != nil {
					return err
				}
				var intVal uint64
				var strVal string
				if fieldType == 0 {
					intVal, err = ReadUint64(conn.Reader())
				} else {
					strVal, err = ReadString(conn.Reader())
				}
				if err != nil {
					return err
				}
				fields = append(fields, ActivityField{Type: fieldType, IntVal: intVal, StrVal: strVal})
			}

			if sink != nil {
				sink.WriteStderrResult(act, type_, fields)
			}

		default:
			// Some protocol versions or operations might send 0/1 as boolean responses
			// after the stderr stream. If we see these, treat them as completion signals.
			if marker == 0 {
				// 0 typically means "false" or "not found" - treat as error
				return fmt.Errorf("operation returned false/failure (marker=0)")
			} else if marker == 1 {
				// 1 typically means "true" or "success" - treat as successful completion
				return nil
			}
			return fmt.Errorf("unknown stderr marker: 0x%x", marker)
		}
	}
}

// PathInfo holds information about a store path
type PathInfo struct {
	Deriver          string   // Derivation that produced this path (empty if unknown)
	NarHash          string   // Hash of the NAR serialization
	References       []string // Paths this path references
	RegistrationTime uint64   // When the path was registered
	NarSize          uint64   // Size of the NAR serialization
	Ultimate         bool     // Whether this is an "ultimate" path
	Sigs             []string // Signatures
	CA               string   // Content address (empty for input-addressed)
}

// queryStoreHostPathInfo queries the Store Host for path info via SSH
func (p *Proxy) queryStoreHostPathInfo(path string) (*PathInfo, error) {
	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("connecting to Store Host: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	// Run nix path-info --json to get path information
	cmd := fmt.Sprintf("nix path-info --json %q 2>/dev/null", path)
	output, err := session.Output(cmd)
	if err != nil {
		// Path doesn't exist or command failed
		return nil, nil
	}

	// Parse JSON output - nix path-info --json returns an array or object depending on version
	// Define the struct for a single path info
	type jsonPathInfo struct {
		Path             string   `json:"path"`
		Deriver          string   `json:"deriver"`
		NarHash          string   `json:"narHash"`
		NarSize          uint64   `json:"narSize"`
		References       []string `json:"references"`
		RegistrationTime uint64   `json:"registrationTime"`
		Ultimate         bool     `json:"ultimate"`
		Signatures       []string `json:"signatures"`
		CA               string   `json:"ca"`
	}

	// log.Printf("DEBUG: Raw path-info output: %s", string(output))

	var pathInfos []jsonPathInfo

	// Try unmarshaling as a list first
	if err := json.Unmarshal(output, &pathInfos); err != nil {
		// If that fails, try unmarshaling as a map keyed by path (newer Nix versions?)
		var pathInfoMap map[string]jsonPathInfo
		if err2 := json.Unmarshal(output, &pathInfoMap); err2 == nil {
			// Convert map values to slice
			for _, info := range pathInfoMap {
				pathInfos = append(pathInfos, info)
			}
		} else {
			// Finally, try simple object (unlikely given keys, but for robustness)
			var singleInfo jsonPathInfo
			if err3 := json.Unmarshal(output, &singleInfo); err3 == nil {
				pathInfos = []jsonPathInfo{singleInfo}
			} else {
				return nil, fmt.Errorf("parsing path-info JSON failed (tried list, map, object). Raw: %s", string(output))
			}
		}
	}

	if len(pathInfos) == 0 {
		return nil, nil
	}

	info := pathInfos[0]
	refs := info.References
	if refs == nil {
		refs = []string{}
	}
	sigs := info.Signatures
	if sigs == nil {
		sigs = []string{}
	}

	return &PathInfo{
		Deriver:          info.Deriver,
		NarHash:          info.NarHash,
		References:       refs,
		RegistrationTime: info.RegistrationTime,
		NarSize:          info.NarSize,
		Ultimate:         info.Ultimate,
		Sigs:             sigs,
		CA:               info.CA,
	}, nil
}

// queryDerivationHashFromStore queries the store host for the SHA256 hash of a derivation file
// This is the "derivation hash" used to construct DrvOutput IDs
func (p *Proxy) queryDerivationHashFromStore(drvPath string) (string, error) {
	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		return "", fmt.Errorf("store host not configured")
	}

	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		return "", fmt.Errorf("connecting to store host: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	// Use sha256sum to get the hash of the derivation file
	// Use set -o pipefail to catch errors in the pipe
	cmd := fmt.Sprintf("set -o pipefail; sha256sum %q | cut -d' ' -f1", drvPath)
	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return "", fmt.Errorf("running sha256sum on store host: %w. Output: %s", err, string(output))
	}

	hash := strings.TrimSpace(string(output))

	if len(hash) != 64 {
		return "", fmt.Errorf("unexpected hash length: got %d chars, expected 64. Full output: %s", len(hash), string(output))
	}

	// log.Printf("DEBUG: Derivation hash for %s (from store host): %s", drvPath, hash)
	return hash, nil
}

// queryRealisation queries the builder for a realisation by DrvOutput ID
// Returns nil if the realisation doesn't exist
func (p *Proxy) queryRealisation(conn *Conn, drvOutputID string) (*Realisation, error) {
	// log.Printf("DEBUG: Sending QueryRealisation for %s", drvOutputID)

	if err := conn.WriteOp(OpQueryRealisation); err != nil {
		return nil, fmt.Errorf("writing QueryRealisation op: %w", err)
	}

	// Write the DrvOutput ID
	if err := WriteString(conn.Writer(), drvOutputID); err != nil {
		return nil, fmt.Errorf("writing DrvOutput ID: %w", err)
	}

	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("flushing QueryRealisation: %w", err)
	}

	// Read stderr stream
	if err := readStderrStream(conn, nil); err != nil {
		return nil, fmt.Errorf("reading stderr for QueryRealisation: %w", err)
	}

	// Read the response - it's an optional<Realisation> serialized as count + JSON strings
	// If count is 0, no realisation exists
	count, err := ReadUint64(conn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading realisation count: %w", err)
	}

	if count == 0 {
		// log.Printf("DEBUG: No realisation found for %s", drvOutputID)
		return nil, nil
	}

	// Read the realisation as JSON string
	jsonStr, err := ReadString(conn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading realisation JSON: %w", err)
	}

	var real Realisation
	if err := json.Unmarshal([]byte(jsonStr), &real); err != nil {
		return nil, fmt.Errorf("unmarshaling realisation: %w", err)
	}

	// Ensure non-nil collections
	if real.Signatures == nil {
		real.Signatures = []string{}
	}
	if real.DependentRealisations == nil {
		real.DependentRealisations = make(map[string]string)
	}

	return &real, nil
}

// handleAddToStore handles adding a path to the store
func (p *Proxy) handleAddToStore(conn *Conn) error {
	minor := conn.Version & 0xff

	if minor >= 25 {
		// New format
		name, err := ReadString(conn.Reader())
		if err != nil {
			return err
		}
		camStr, err := ReadString(conn.Reader())
		if err != nil {
			return err
		}
		_, err = ReadStrings(conn.Reader()) // refs
		if err != nil {
			return err
		}
		_, err = ReadBool(conn.Reader()) // repair
		if err != nil {
			return err
		}

		log.Printf("AddToStore: %s (cam: %s) - not implemented", name, camStr)

		// Would need to read framed source data here
		return conn.StopWorkWithError("AddToStore not implemented", 1)
	} else {
		// Old format
		_, err := ReadString(conn.Reader()) // baseName
		if err != nil {
			return err
		}
		_, err = ReadBool(conn.Reader()) // fixed
		if err != nil {
			return err
		}
		_, err = ReadUint64(conn.Reader()) // recursive
		if err != nil {
			return err
		}
		_, err = ReadString(conn.Reader()) // hashAlgo
		if err != nil {
			return err
		}

		return conn.StopWorkWithError("AddToStore not implemented", 1)
	}
}

// handleAddMultipleToStore handles adding multiple paths
// We forward this to the store host so derivations are available there
func (p *Proxy) handleAddMultipleToStore(conn *Conn) error {
	log.Printf("DEBUG AddMultipleToStore: starting, reading repair flag...")
	repair, err := ReadBool(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading repair flag: %w", err)
	}
	log.Printf("DEBUG AddMultipleToStore: repair=%v, reading dontCheckSigs...", repair)
	dontCheckSigs, err := ReadBool(conn.Reader())
	if err != nil {
		return fmt.Errorf("reading dontCheckSigs flag: %w", err)
	}
	log.Printf("DEBUG AddMultipleToStore: dontCheckSigs=%v", dontCheckSigs)

	// Check if we have Store Host configured
	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		log.Printf("AddMultipleToStore: Store Host not configured, discarding")
		// Fall back to consuming and discarding
		return p.consumeAndDiscardFramedSource(conn)
	}

	log.Printf("AddMultipleToStore: forwarding to store host %s", p.config.StoreHostAddr)

	// From this point on, the client has committed to streaming a
	// FramedSource next regardless of what happens here. Any early return
	// MUST drain that data first (see discardFramedSourceBody), or the
	// leftover bytes desync the rest of the session.
	failWithDrain := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		log.Printf("AddMultipleToStore: %s", msg)
		if drainErr := p.discardFramedSourceBody(conn); drainErr != nil {
			log.Printf("AddMultipleToStore: failed to drain client framed source after error: %v", drainErr)
		}
		return conn.StopWorkWithError(msg, 1)
	}

	// Connect to Store Host via SSH
	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		return failWithDrain("failed to connect to Store Host: %v", err)
	}
	defer client.Close()

	// Start session to run nix-daemon --stdio
	session, err := client.NewSession()
	if err != nil {
		return failWithDrain("failed to create SSH session: %v", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return failWithDrain("failed to get stdin pipe: %v", err)
	}
	// Ensure stdin is closed when we're done to signal nix-daemon to exit
	defer stdin.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return failWithDrain("failed to get stdout pipe: %v", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		return failWithDrain("failed to get stderr pipe: %v", err)
	}

	// Capture stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[store-host-stderr] %s", scanner.Text())
		}
	}()

	// Choose the nix-daemon command: shared store or user-private store.
	nixDaemonCmd := "nix-daemon --stdio"
	if p.config.UserStoreEnabled && p.config.UserFingerprint != "" {
		storeRoot := p.userStoreRoot()
		// Ensure the user store directory exists before starting the daemon.
		nixDaemonCmd = fmt.Sprintf("mkdir -p %q && exec nix-daemon --stdio --store 'local?root=%s'",
			storeRoot, storeRoot)
		log.Printf("AddMultipleToStore: routing to user store at %s", storeRoot)
	}

	if err := session.Start(nixDaemonCmd); err != nil {
		return failWithDrain("failed to start nix-daemon: %v", err)
	}

	// Create connection to store host's nix-daemon
	storeConn := NewConn(stdout, stdin)

	// Perform handshake with store host
	if err := handshakeWithBuilder(storeConn); err != nil {
		return failWithDrain("store host handshake failed: %v", err)
	}

	// Send SetOptions first
	if err := sendSetOptions(storeConn, nil); err != nil {
		return failWithDrain("store host SetOptions failed: %v", err)
	}

	// Now send AddMultipleToStore operation
	// log.Printf("DEBUG: AddMultipleToStore: sending op to store host")
	if err := storeConn.WriteOp(OpAddMultipleToStore); err != nil {
		return failWithDrain("writing AddMultipleToStore op: %v", err)
	}

	// When uploading to the shared store: enforce sig checking regardless of what
	// the client requested (clients cannot bypass signature verification).
	// When uploading to a user-private store: allow unsigned paths (the user is
	// uploading their own source inputs which are not signed).
	allowUnsigned := p.config.UserStoreEnabled && p.config.UserFingerprint != ""
	if dontCheckSigs && !allowUnsigned {
		log.Printf("AddMultipleToStore: client requested dontCheckSigs=true, overriding to false")
	}
	if err := WriteBool(storeConn.Writer(), repair); err != nil {
		return failWithDrain("writing repair: %v", err)
	}
	if err := WriteBool(storeConn.Writer(), allowUnsigned); err != nil {
		return failWithDrain("writing dontCheckSigs: %v", err)
	}

	// Flush operation and flags before starting to forward data
	if err := storeConn.Flush(); err != nil {
		return failWithDrain("flushing op and flags: %v", err)
	}

	// Start reading stderr stream concurrently to prevent deadlock
	// If the buffer fills up with logs while we are writing, the store host will block
	readErrCh := make(chan error, 1)
	go func() {
		log.Printf("AddMultipleToStore: stderr reader goroutine started")
		err := readStderrStream(storeConn, nil)
		log.Printf("AddMultipleToStore: stderr reader goroutine finished: %v", err)
		readErrCh <- err
	}()

	// failMidUpload reports a failure that happens once we're already
	// forwarding frames. remainingCurrentFrame is how many bytes of the
	// frame in progress the client has not yet sent us (0 if we're at a
	// frame boundary); we must drain those plus every subsequent frame
	// before replying, or the client's still-incoming bytes get misread as
	// the next operation's opcode. Read-side failures on conn itself (the
	// client connection breaking mid-frame) are the one case where there's
	// nothing reliable left to drain, so those stay as bare errors below.
	failMidUpload := func(remainingCurrentFrame uint64, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		log.Printf("AddMultipleToStore: %s", msg)
		if remainingCurrentFrame > 0 {
			if err := discardRawBytes(conn.Reader(), remainingCurrentFrame); err != nil {
				log.Printf("AddMultipleToStore: failed to drain remainder of current frame: %v", err)
				return conn.StopWorkWithError(msg, 1)
			}
		}
		if err := p.discardFramedSourceBody(conn); err != nil {
			log.Printf("AddMultipleToStore: failed to drain remaining framed source: %v", err)
		}
		return conn.StopWorkWithError(msg, 1)
	}

	// Forward the framed source data
	totalBytes := uint64(0)
	totalFrameBytes := uint64(0) // includes frame length headers
	frameCount := 0
uploadLoop:
	for {
		// Check if reading failed already
		select {
		case err := <-readErrCh:
			if err != nil {
				return failMidUpload(0, "store host returned error during upload: %v", err)
			}
			// If nil, it means StderrLast was received early? That shouldn't happen during upload normally.
			break uploadLoop
		default:
		}

		frameLen, err := ReadUint64(conn.Reader())
		if err != nil {
			return fmt.Errorf("reading frame length: %w", err)
		}

		frameCount++
		totalFrameBytes += 8 // frame length header
		//// log.Printf("DEBUG: AddMultipleToStore frame #%d: len=%d", frameCount, frameLen)

		// Write frame length to store host
		if err := WriteUint64(storeConn.Writer(), frameLen); err != nil {
			return failMidUpload(frameLen, "writing frame length to store: %v", err)
		}

		if frameLen == 0 {
			break // End of framed source
		}

		// Read frame data (FramedSource chunks are NOT padded)
		toRead := frameLen
		totalBytes += frameLen

		buf := make([]byte, 32*1024)
		for toRead > 0 {
			n := toRead
			if n > uint64(len(buf)) {
				n = uint64(len(buf))
			}
			_, err := io.ReadFull(conn.Reader(), buf[:n])
			if err != nil {
				return fmt.Errorf("reading frame data: %w", err)
			}
			// Forward to store host
			if _, err := storeConn.Writer().Write(buf[:n]); err != nil {
				return failMidUpload(toRead-n, "writing frame data to store: %v", err)
			}
			toRead -= n
		}
	}

	// Flush to store host
	// log.Printf("DEBUG: AddMultipleToStore: flushing to store host...")
	if err := storeConn.Flush(); err != nil {
		return fmt.Errorf("flushing to store host: %w", err)
	}
	// log.Printf("DEBUG: AddMultipleToStore: flush completed successfully")

	// NOTE: Do NOT close stdin here. The framed source is terminated by the 0-length frame
	// we already sent. Closing stdin causes "interrupted by the user" errors.
	// The session will be closed when we defer session.Close() at function exit.

	log.Printf("AddMultipleToStore: forwarded %d bytes to store host, waiting for response (5 min timeout)...", totalBytes)

	// Wait for the reader to finish with a timeout
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	timeout := time.After(5 * time.Minute)

waitLoop:
	for {
		select {
		case err := <-readErrCh:
			if err != nil {
				log.Printf("AddMultipleToStore: store host returned error: %v", err)
				return conn.StopWorkWithError(fmt.Sprintf("store host error: %v", err), 1)
			}
			break waitLoop
		case <-ticker.C:
			log.Printf("AddMultipleToStore: still waiting for store host response...")
		case <-timeout:
			log.Printf("AddMultipleToStore: TIMEOUT waiting for store host response after 5 minutes")
			return conn.StopWorkWithError("store host timeout", 1)
		}
	}

	log.Printf("AddMultipleToStore: store host accepted data, sending StderrLast to client")
	if err := conn.StopWork(); err != nil {
		return fmt.Errorf("AddMultipleToStore StopWork failed: %w", err)
	}
	log.Printf("AddMultipleToStore: completed successfully")
	return nil
}

// discardRawBytes reads and discards exactly n unframed, unpadded bytes.
func discardRawBytes(r io.Reader, n uint64) error {
	buf := make([]byte, 32*1024)
	for n > 0 {
		chunk := n
		if chunk > uint64(len(buf)) {
			chunk = uint64(len(buf))
		}
		if _, err := io.ReadFull(r, buf[:chunk]); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// discardFramedSourceBody reads and discards a client's FramedSource payload
// (the sequence of length-prefixed chunks terminated by a zero-length frame)
// without sending any reply. Once a client has sent an op that carries a
// FramedSource (e.g. AddMultipleToStore), it unconditionally starts
// streaming that data next - regardless of whether we can actually forward
// it anywhere. Any error path taken before that data is fully read MUST
// drain it here first, or the leftover bytes get misread as the next
// operation's opcode, corrupting the rest of the session.
func (p *Proxy) discardFramedSourceBody(conn *Conn) error {
	totalBytes := uint64(0)
	for {
		frameLen, err := ReadUint64(conn.Reader())
		if err != nil {
			return fmt.Errorf("reading frame length: %w", err)
		}
		if frameLen == 0 {
			break
		}
		totalBytes += frameLen
		if err := discardRawBytes(conn.Reader(), frameLen); err != nil {
			return fmt.Errorf("reading frame data: %w", err)
		}
	}

	log.Printf("AddMultipleToStore: discarded %d bytes of framed source", totalBytes)
	return nil
}

// consumeAndDiscardFramedSource reads and discards framed source data, then
// reports success to the client (used when we deliberately have nowhere to
// forward the data, e.g. no Store Host configured).
func (p *Proxy) consumeAndDiscardFramedSource(conn *Conn) error {
	if err := p.discardFramedSourceBody(conn); err != nil {
		return err
	}
	return conn.StopWork()
}

// handleNarFromPath handles NAR export - fetches from Store Host
func (p *Proxy) handleNarFromPath(conn *Conn) error {
	path, err := ReadString(conn.Reader())
	if err != nil {
		return err
	}

	log.Printf("NarFromPath: %s", path)

	// Check if we have Store Host configured
	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		log.Printf("NarFromPath: Store Host not configured")
		return conn.StopWorkWithError("NarFromPath: Store Host not configured", 1)
	}

	// Connect to Store Host via SSH
	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		log.Printf("NarFromPath: failed to connect to Store Host: %v", err)
		return conn.StopWorkWithError(fmt.Sprintf("failed to connect to Store Host: %v", err), 1)
	}
	defer client.Close()

	// Start session to run nix-store --dump
	session, err := client.NewSession()
	if err != nil {
		log.Printf("NarFromPath: failed to create SSH session: %v", err)
		return conn.StopWorkWithError(fmt.Sprintf("failed to create SSH session: %v", err), 1)
	}
	defer session.Close()

	// Get stdout pipe
	stdout, err := session.StdoutPipe()
	if err != nil {
		return conn.StopWorkWithError(fmt.Sprintf("failed to get stdout pipe: %v", err), 1)
	}

	// Start the command
	cmd := fmt.Sprintf("nix-store --dump %q", path)
	log.Printf("NarFromPath: running on Store Host: %s", cmd)
	if err := session.Start(cmd); err != nil {
		return conn.StopWorkWithError(fmt.Sprintf("failed to start nix-store --dump: %v", err), 1)
	}

	// Send StderrLast to signal end of logs and start of raw data stream
	if err := conn.StopWork(); err != nil {
		return fmt.Errorf("NarFromPath StopWork: %w", err)
	}

	// Stream NAR data raw (no StderrWrite framing)
	// Read in chunks and write directly to conn.Writer()
	buf := make([]byte, 64*1024) // 64KB chunks
	totalBytes := uint64(0)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			// Write the data chunk directly
			if _, err := conn.Writer().Write(buf[:n]); err != nil {
				session.Close()
				return fmt.Errorf("writing NAR chunk: %w", err)
			}
			totalBytes += uint64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			session.Close()
			return fmt.Errorf("reading NAR data: %w", err)
		}
	}

	// Wait for command to complete
	if err := session.Wait(); err != nil {
		log.Printf("NarFromPath: nix-store --dump failed: %v", err)
		// We can't really report this error cleanly if we've already started streaming data,
		// but the client might detect the truncated stream.
		return fmt.Errorf("nix-store --dump failed: %w", err)
	}

	// Flush the writer
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flushing NAR stream: %w", err)
	}

	log.Printf("NarFromPath: streamed %d bytes for %s", totalBytes, path)
	return nil
}

// handleQueryReferrers queries paths that reference a given path
func (p *Proxy) handleQueryReferrers(conn *Conn) error {
	path, err := ReadString(conn.Reader())
	if err != nil {
		return err
	}

	log.Printf("QueryReferrers: %s", path)

	// Return empty set
	conn.StopWork()
	return WriteStrings(conn.Writer(), []string{})
}

// handleAddTempRoot adds a temporary GC root
func (p *Proxy) handleAddTempRoot(conn *Conn) error {
	path, err := ReadString(conn.Reader())
	if err != nil {
		return err
	}

	log.Printf("AddTempRoot: %s", path)

	// Just acknowledge - we don't actually track roots
	if err := conn.StopWork(); err != nil {
		return fmt.Errorf("AddTempRoot StopWork: %w", err)
	}
	// log.Printf("DEBUG: AddTempRoot completed, sent StderrLast")
	return nil
}

// handleQueryMissing queries which paths are missing
func (p *Proxy) handleQueryMissing(conn *Conn) error {
	// Read DerivedPaths
	count, err := ReadUint64(conn.Reader())
	if err != nil {
		return err
	}

	for i := uint64(0); i < count; i++ {
		// Read DerivedPath (complex type)
		// For simplicity, just read as raw type indicator + path
		pathType, err := ReadUint64(conn.Reader())
		if err != nil {
			return err
		}
		_, err = ReadString(conn.Reader()) // path
		if err != nil {
			return err
		}
		if pathType == 1 { // Built type has outputs
			_, err = ReadStrings(conn.Reader())
			if err != nil {
				return err
			}
		}
	}

	log.Printf("QueryMissing: %d paths", count)

	// Return: willBuild, willSubstitute, unknown, downloadSize, narSize
	conn.StopWork()
	WriteStrings(conn.Writer(), []string{}) // willBuild
	WriteStrings(conn.Writer(), []string{}) // willSubstitute
	WriteStrings(conn.Writer(), []string{}) // unknown
	WriteUint64(conn.Writer(), 0)           // downloadSize
	WriteUint64(conn.Writer(), 0)           // narSize
	return nil
}

// sanitizeFingerprint converts an SSH key fingerprint to a safe directory name.
// "SHA256:abc+d/ef==" → "abc+d_ef"  (strips prefix, replaces /, removes =)
func sanitizeFingerprint(fp string) string {
	fp = strings.TrimPrefix(fp, "SHA256:")
	fp = strings.NewReplacer("/", "_", "+", "-", "=", "").Replace(fp)
	return fp
}

// userStoreRoot returns the per-user store root path on the store host.
func (p *Proxy) userStoreRoot() string {
	root := p.config.UserStoreRoot
	if root == "" {
		root = "/nix/user-inputs"
	}
	return root + "/" + sanitizeFingerprint(p.config.UserFingerprint)
}

// importUserInputsToSharedStore copies all paths from the user's private store
// on the store host into the shared store so builder VMs can access them.
// This is a best-effort operation; errors are only logged.
func (p *Proxy) importUserInputsToSharedStore() {
	if !p.config.UserStoreEnabled || p.config.UserFingerprint == "" {
		return
	}
	if p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		return
	}

	storeRoot := p.userStoreRoot()

	sshCfg := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshCfg)
	if err != nil {
		log.Printf("importUserInputs: SSH connect failed: %v", err)
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		log.Printf("importUserInputs: SSH session failed: %v", err)
		return
	}
	defer session.Close()

	// nix copy --all copies every path from the source store into the destination.
	// --to daemon writes into the shared nix-daemon (default store).
	// --no-check-sigs: user inputs are unsigned (they originate from the client).
	cmd := fmt.Sprintf(
		"test -d %q/nix/store && nix copy --no-check-sigs --all --from 'local?root=%s' --to daemon 2>&1 || true",
		storeRoot, storeRoot,
	)
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		log.Printf("importUserInputs: command failed: %v: %s", err, out)
		return
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		log.Printf("importUserInputs: %s", strings.TrimSpace(string(out)))
	}
	log.Printf("importUserInputs: imported user inputs from %s", storeRoot)
}

// signAndRegisterOutputs signs each output store path from a successful build and
// registers the signatures with the store host via OpAddSignatures. Best-effort:
// errors are logged but do not fail the build. Signatures are also added to
// result.BuiltOutputs so the client sees them in the BuildResult.
func (p *Proxy) signAndRegisterOutputs(result *BuildResult) {
	if p.config.Signer == nil || p.config.StoreHostAddr == "" || p.config.StoreHostKey == nil {
		return
	}
	switch result.Status {
	case BuildResultBuilt, BuildResultSubstituted, BuildResultAlreadyValid:
		// continue
	default:
		return
	}

	type entry struct {
		id      string
		outPath string
	}
	var outputs []entry
	for id, real := range result.BuiltOutputs {
		if real.OutPath != "" {
			outputs = append(outputs, entry{id, real.OutPath})
		}
	}
	if len(outputs) == 0 {
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            p.config.StoreHostUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(p.config.StoreHostKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	client, err := ssh.Dial("tcp", p.config.StoreHostAddr, sshConfig)
	if err != nil {
		log.Printf("signing: failed to connect to store host: %v", err)
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		log.Printf("signing: failed to create session: %v", err)
		return
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		log.Printf("signing: failed to get stdin pipe: %v", err)
		return
	}
	defer stdin.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		log.Printf("signing: failed to get stdout pipe: %v", err)
		return
	}

	if err := session.Start("nix-daemon --stdio"); err != nil {
		log.Printf("signing: failed to start nix-daemon: %v", err)
		return
	}

	storeConn := NewConn(stdout, stdin)
	if err := handshakeWithBuilder(storeConn); err != nil {
		log.Printf("signing: store handshake failed: %v", err)
		return
	}
	if err := sendSetOptions(storeConn, nil); err != nil {
		log.Printf("signing: SetOptions failed: %v", err)
		return
	}

	for _, out := range outputs {
		pathInfo, err := p.queryPathInfoViaDaemon(storeConn, out.outPath)
		if err != nil {
			log.Printf("signing: QueryPathInfo failed for %s: %v", out.outPath, err)
			continue
		}
		if pathInfo == nil {
			log.Printf("signing: path not found on store: %s", out.outPath)
			continue
		}

		sig, err := p.config.Signer.SignPath(out.outPath, pathInfo.NarHash, pathInfo.NarSize, pathInfo.References)
		if err != nil {
			log.Printf("signing: failed to sign %s: %v", out.outPath, err)
			continue
		}

		if err := p.sendAddSignatures(storeConn, out.outPath, []string{sig}); err != nil {
			log.Printf("signing: AddSignatures failed for %s (ensure %s is in trusted-users): %v",
				out.outPath, p.config.StoreHostUser, err)
			continue
		}

		real := result.BuiltOutputs[out.id]
		real.Signatures = append(real.Signatures, sig)
		result.BuiltOutputs[out.id] = real
		log.Printf("signing: signed and registered %s", out.outPath)
	}
}

// queryPathInfoViaDaemon queries the store host nix-daemon for path info on an
// already-open connection. This is used during signing to get narHash, narSize,
// and references needed to compute the Nix fingerprint.
func (p *Proxy) queryPathInfoViaDaemon(storeConn *Conn, path string) (*PathInfo, error) {
	if err := storeConn.WriteOp(OpQueryPathInfo); err != nil {
		return nil, err
	}
	if err := WriteString(storeConn.Writer(), path); err != nil {
		return nil, err
	}
	if err := storeConn.Flush(); err != nil {
		return nil, err
	}
	if err := readStderrStream(storeConn, nil); err != nil {
		return nil, err
	}

	minor := storeConn.Version & 0xff
	if minor >= 17 {
		found, err := ReadBool(storeConn.Reader())
		if err != nil {
			return nil, fmt.Errorf("reading found flag: %w", err)
		}
		if !found {
			return nil, nil
		}
	}

	deriver, err := ReadString(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading deriver: %w", err)
	}
	narHash, err := ReadString(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading narHash: %w", err)
	}
	refs, err := ReadStrings(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading refs: %w", err)
	}
	regTime, err := ReadUint64(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading regTime: %w", err)
	}
	narSize, err := ReadUint64(storeConn.Reader())
	if err != nil {
		return nil, fmt.Errorf("reading narSize: %w", err)
	}

	info := &PathInfo{
		Deriver:          deriver,
		NarHash:          narHash,
		References:       refs,
		RegistrationTime: regTime,
		NarSize:          narSize,
	}

	if minor >= 16 {
		ultimate, err := ReadBool(storeConn.Reader())
		if err != nil {
			return nil, fmt.Errorf("reading ultimate: %w", err)
		}
		sigs, err := ReadStrings(storeConn.Reader())
		if err != nil {
			return nil, fmt.Errorf("reading sigs: %w", err)
		}
		ca, err := ReadString(storeConn.Reader())
		if err != nil {
			return nil, fmt.Errorf("reading ca: %w", err)
		}
		info.Ultimate = ultimate
		info.Sigs = sigs
		info.CA = ca
	}

	return info, nil
}

// sendAddSignatures sends OpAddSignatures on an already-open store connection.
// The store host must have the store user in trusted-users for this to succeed.
func (p *Proxy) sendAddSignatures(storeConn *Conn, path string, sigs []string) error {
	if err := storeConn.WriteOp(OpAddSignatures); err != nil {
		return err
	}
	if err := WriteString(storeConn.Writer(), path); err != nil {
		return err
	}
	if err := WriteStrings(storeConn.Writer(), sigs); err != nil {
		return err
	}
	if err := storeConn.Flush(); err != nil {
		return err
	}
	return readStderrStream(storeConn, nil)
}

// collectPeakMemory reads peak memory usage from a builder VM via SSH.
// Tries cgroup v2 memory.peak first, then falls back to MemTotal-MemAvailable from /proc/meminfo.
// Returns nil if collection fails (best-effort).
func collectPeakMemory(client *ssh.Client) *uint64 {
	session, err := client.NewSession()
	if err != nil {
		log.Printf("metrics: failed to open session for memory collection: %v", err)
		return nil
	}
	defer session.Close()

	// Try cgroup v2 memory.peak first (whole-VM peak since boot),
	// fall back to MemTotal - MemAvailable (current usage, not peak)
	cmd := `cat /sys/fs/cgroup/memory.peak 2>/dev/null || awk '/MemTotal/{t=$2} /MemAvailable/{a=$2} END{print (t-a)*1024}' /proc/meminfo`
	out, err := session.Output(cmd)
	if err != nil {
		log.Printf("metrics: failed to read memory stats: %v", err)
		return nil
	}

	var val uint64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &val); err != nil {
		log.Printf("metrics: failed to parse memory value %q: %v", strings.TrimSpace(string(out)), err)
		return nil
	}
	return &val
}

// DialSSH is a helper to establish an SSH connection to a builder
func DialSSH(addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	// Try to connect with retries
	var client *ssh.Client
	var err error

	for i := 0; i < 30; i++ {
		client, err = ssh.Dial("tcp", addr, config)
		if err == nil {
			return client, nil
		}

		if i < 29 {
			log.Printf("SSH connection to %s failed (attempt %d): %v, retrying...", addr, i+1, err)
			time.Sleep(2 * time.Second)
		}
	}

	return nil, fmt.Errorf("failed to connect to %s after 30 attempts: %w", addr, err)
}

// WaitForSSH waits for SSH to become available on a host
func WaitForSSH(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("SSH not available on %s after %v", addr, timeout)
}
