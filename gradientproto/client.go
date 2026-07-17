package gradientproto

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// requestJobHeartbeat matches Gradient's own worker: RequestJob is re-sent
// every 10s per free slot as a heartbeat, in case the server restarted and
// lost the pending request (see gradient-worker/src/worker/dispatch.rs's
// RequestJob doc, quoted in messages/client.rs).
const requestJobHeartbeat = 10 * time.Second

// ClientConfig configures one worker session (see Client.Run).
type ClientConfig struct {
	// ServerURL is the Gradient server's /proto WebSocket endpoint, e.g.
	// "wss://gradient.example.com/proto".
	ServerURL string
	// PeerID is this worker's persistent UUID - generated once and reused
	// across reconnects (see services.gradient.worker.nix's workerId option
	// for why that matters: it's how the server recognizes returning workers
	// and any pre-registered peer tokens).
	PeerID string
	// Credentials authenticates this worker to peers that challenge it
	// during the handshake. May be nil for open/discoverable mode.
	Credentials PeerCredentials
	// HTTPClient overrides the HTTP client used for the WebSocket
	// handshake (e.g. custom TLS config). Nil uses http.DefaultClient.
	HTTPClient *http.Client

	// Architectures are the Nix system strings this worker can build for
	// (e.g. "x86_64-linux"). At least one is required.
	Architectures []string
	// SystemFeatures are Nix system features this worker advertises
	// (e.g. "big-parallel"). May be empty.
	SystemFeatures []string
	// MaxConcurrentBuilds caps how many BuildTasks this worker runs at
	// once, and how many RequestJob{kind: Build} signals it keeps
	// outstanding.
	MaxConcurrentBuilds uint32
	// CPUCount and RAMTotalMB are advertised in WorkerCapabilities for the
	// server's scheduler to use; they don't affect this package's own
	// behavior.
	CPUCount     uint32
	RAMTotalMB   uint64
	CPUCoreScore uint32
}

// JobHandler runs one assigned BuildJob's tasks. Implementations own all
// protocol interaction for the job's lifetime: streaming JobUpdate/LogChunk
// as work progresses, and finishing with JobCompleted or JobFailed (see
// client_message.go's EncodeJobUpdate*/EncodeJobCompleted/EncodeJobFailed).
// The context is cancelled if the server sends AbortJob for this job ID, or
// if the session ends; handlers must check ctx and stop promptly.
//
// conn is safe to call concurrently from multiple JobHandler invocations
// and from Client's own dispatch loop - see Conn's doc and
// github.com/coder/websocket's concurrency guarantee (all Conn methods
// except Read may be called concurrently; Client owns the only reader).
// client is the same Client running this handler - passed through so
// handlers that need a request/response exchange (e.g. CacheQuery, which
// this loop's single reader can't let a handler wait on directly - see
// QueryCache) can use it.
type JobHandler func(ctx context.Context, client *Client, conn *Conn, jobID string, job BuildJob)

// Client runs one worker session against a Gradient server: handshake,
// capability advertisement, pull-based job requests, and dispatching
// assigned jobs to a JobHandler.
type Client struct {
	cfg     ClientConfig
	handler JobHandler

	mu           sync.Mutex
	cancels      map[string]context.CancelFunc // jobID -> cancel, for AbortJob
	running      int                           // count of in-flight JobHandler calls
	cacheWaiters map[string]chan ServerMessage // query_id -> waiter, see QueryCache
}

// NewClient returns a Client ready to Run. handler is called once per
// assigned job, in its own goroutine, respecting cfg.MaxConcurrentBuilds.
func NewClient(cfg ClientConfig, handler JobHandler) *Client {
	return &Client{
		cfg:          cfg,
		handler:      handler,
		cancels:      make(map[string]context.CancelFunc),
		cacheWaiters: make(map[string]chan ServerMessage),
	}
}

// Run dials the server, completes the handshake, advertises capabilities,
// and processes messages until the connection drops, the server sends
// Reject/Error, or ctx is cancelled - whichever happens first - returning
// the resulting error. It does not retry: callers that want
// reconnect-with-backoff should call Run again in a loop (see
// testdata/gen-fixtures's plan doc; kept separate so retry policy - jitter,
// max backoff, giving up - is the caller's decision, not baked in here).
//
// Any JobHandler goroutines still running when Run returns are left
// running with a cancelled context (derived from ctx) - Run does not block
// waiting for them to finish; callers that need to know when all work has
// actually stopped should track that via their own JobHandler.
func (c *Client) Run(ctx context.Context) error {
	conn, err := Dial(ctx, c.cfg.ServerURL, c.cfg.HTTPClient)
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	caps := GradientCapabilities{Build: true}
	result, err := Handshake(ctx, conn, c.cfg.PeerID, caps, c.cfg.Credentials)
	if err != nil {
		return err
	}
	if !result.Negotiated.Build {
		return fmt.Errorf("gradientproto: server did not negotiate the build capability")
	}
	for _, fp := range result.FailedPeers {
		log.Printf("gradientproto: peer %s auth failed: %s", fp.PeerID, fp.Reason)
	}

	if err := conn.SendClientMessage(ctx, EncodeWorkerCapabilities(
		c.cfg.Architectures, c.cfg.SystemFeatures, c.cfg.MaxConcurrentBuilds,
		c.cfg.CPUCount, c.cfg.RAMTotalMB, c.cfg.CPUCoreScore,
	)); err != nil {
		return err
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()

	for i := uint32(0); i < c.cfg.MaxConcurrentBuilds; i++ {
		if err := conn.SendClientMessage(ctx, EncodeRequestJob(JobKindBuild)); err != nil {
			return err
		}
	}

	heartbeat := time.NewTicker(requestJobHeartbeat)
	defer heartbeat.Stop()
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-heartbeat.C:
				if c.spareCapacity() {
					if err := conn.SendClientMessage(sessionCtx, EncodeRequestJob(JobKindBuild)); err != nil {
						return
					}
				}
			}
		}
	}()

	for {
		msg, err := conn.RecvServerMessage(ctx)
		if err != nil {
			return err
		}

		switch msg.Kind {
		case ServerMsgAssignJob:
			c.dispatch(sessionCtx, conn, msg.JobID, msg.Job)

		case ServerMsgAbortJob:
			c.abort(msg.JobID)

		case ServerMsgDraining:
			log.Printf("gradientproto: server draining, no longer requesting new jobs")

		case ServerMsgReject:
			return fmt.Errorf("gradientproto: server rejected session (code %d): %s", msg.Code, msg.Reason)

		case ServerMsgError:
			log.Printf("gradientproto: protocol error from server (code %d): %s", msg.Code, msg.Message)

		case ServerMsgUnknown:
			// Forward-compatible: a variant this package doesn't implement
			// yet. Log and ignore rather than treat it as fatal.
			log.Printf("gradientproto: received unimplemented ServerMessage variant (tag %d), ignoring", msg.UnknownTag)

		case ServerMsgCacheStatus, ServerMsgCacheError:
			c.routeCacheResponse(msg)

		default:
			// NarPushResume is intentionally unhandled: v1 doesn't
			// implement upload resume (see narupload.go), so the server
			// never has a reason to send it in response to anything this
			// worker does.
		}
	}
}

// dispatch accepts (always, in v1 - see AssignJobResponse's reason param
// for future capacity-based rejection) an assigned job, registers its
// cancellation, and runs the handler in a new goroutine.
func (c *Client) dispatch(ctx context.Context, conn *Conn, jobID string, job BuildJob) {
	jobCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancels[jobID] = cancel
	c.running++
	c.mu.Unlock()

	if err := conn.SendClientMessage(ctx, EncodeAssignJobResponse(jobID, true, "")); err != nil {
		log.Printf("gradientproto: sending AssignJobResponse for %s: %v", jobID, err)
	}

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.cancels, jobID)
			c.running--
			hasCapacity := uint32(c.running) < c.cfg.MaxConcurrentBuilds
			c.mu.Unlock()
			cancel()

			if hasCapacity {
				if err := conn.SendClientMessage(ctx, EncodeRequestJob(JobKindBuild)); err != nil {
					log.Printf("gradientproto: sending RequestJob after completing %s: %v", jobID, err)
				}
			}
		}()
		c.handler(jobCtx, c, conn, jobID, job)
	}()
}

// QueryCache sends a ClientMessage::CacheQuery and blocks until the
// matching CacheStatus or CacheError response arrives (correlated by
// query_id), or ctx is done. Safe to call concurrently from multiple
// JobHandler goroutines: Client.Run owns the connection's sole reader (see
// JobHandler's doc) and routes responses to the right caller via
// cacheWaiters, keyed by the caller-chosen queryID - callers must pass a
// value unique across all outstanding calls for the lifetime of the
// session (e.g. a fresh UUID per call).
func (c *Client) QueryCache(ctx context.Context, conn *Conn, jobID, queryID string, paths []string, mode QueryMode) (ServerMessage, error) {
	ch := make(chan ServerMessage, 1)
	c.mu.Lock()
	c.cacheWaiters[queryID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.cacheWaiters, queryID)
		c.mu.Unlock()
	}()

	if err := conn.SendClientMessage(ctx, EncodeCacheQuery(jobID, queryID, paths, mode)); err != nil {
		return ServerMessage{}, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-ctx.Done():
		return ServerMessage{}, ctx.Err()
	}
}

// routeCacheResponse delivers a CacheStatus/CacheError message to the
// QueryCache call awaiting it, matched by query_id. If no call is waiting
// (already timed out, or a response for an unrecognized query_id - a
// server bug, since query_id is worker-generated), it's logged and
// dropped rather than blocking Run's single reader loop.
func (c *Client) routeCacheResponse(msg ServerMessage) {
	c.mu.Lock()
	ch, ok := c.cacheWaiters[msg.QueryID]
	c.mu.Unlock()
	if !ok {
		log.Printf("gradientproto: received cache response for unknown query_id %s, ignoring", msg.QueryID)
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// abort cancels a running job's context in response to ServerMessage::AbortJob.
// The job's own JobHandler is responsible for noticing ctx.Done(), cleaning
// up, and reporting JobFailed - abort here only signals it to stop.
func (c *Client) abort(jobID string) {
	c.mu.Lock()
	cancel, ok := c.cancels[jobID]
	c.mu.Unlock()
	if ok {
		cancel()
	} else {
		log.Printf("gradientproto: AbortJob for unknown job %s (already completed?)", jobID)
	}
}

func (c *Client) spareCapacity() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return uint32(c.running) < c.cfg.MaxConcurrentBuilds
}
