package gradientproto

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"nix-builder-provisioner/nixproto"
)

// Executor bridges assigned Gradient BuildJobs to the existing VM-per-
// derivation builder pool (nixproto.BuilderProvider, e.g.
// *provisioner.Pool) and nixproto.ExecuteBuild - the same build-execution
// core the ssh-ng:// proxy path uses. Executor.HandleJob has the
// signature of a JobHandler (see client.go) and is meant to be passed
// directly to NewClient.
type Executor struct {
	// Pool provisions/releases builder VMs, routed by architecture -
	// typically a *provisioner.Pool.
	Pool nixproto.BuilderProvider
	// HTTPClient is used for presigned S3 PUT uploads. Defaults to
	// http.DefaultClient if nil.
	HTTPClient *http.Client

	// storeHostExec backs SetStoreHostExec/storeHostExecer. An
	// atomic.Value, not a plain field, because a caller that redials the
	// store host across Client reconnects (e.g. main.go's
	// startGradientWorker, which redials per session rather than holding
	// one SSH connection for the process lifetime) must be able to swap
	// it out while a JobHandler goroutine from a *previous* session may
	// still be running - Client.Run's own doc notes it "does not block
	// waiting for them" to finish before returning.
	storeHostExec atomic.Value // Execer
}

// NewExecutor returns an Executor ready to use as a JobHandler.
// storeHostExec may be nil if it will be provided later via
// SetStoreHostExec (e.g. because the caller dials the store host lazily,
// only once a session is about to start).
func NewExecutor(pool nixproto.BuilderProvider, storeHostExec Execer, httpClient *http.Client) *Executor {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	e := &Executor{Pool: pool, HTTPClient: httpClient}
	if storeHostExec != nil {
		e.SetStoreHostExec(storeHostExec)
	}
	return e
}

// SetStoreHostExec sets (or replaces) the store-host connection used to
// read derivation content - see storeHostExec's doc for why this is safe
// to call concurrently with in-flight HandleJob calls.
func (e *Executor) SetStoreHostExec(exec Execer) {
	e.storeHostExec.Store(&exec)
}

// storeHostExecer returns the current store-host Execer, or nil if none
// has been set yet.
func (e *Executor) storeHostExecer() Execer {
	v, _ := e.storeHostExec.Load().(*Execer)
	if v == nil {
		return nil
	}
	return *v
}

// HandleJob implements JobHandler: runs job's tasks in order, stopping at
// the first failure (a Job is "an ordered sequence of tasks - if any task
// fails, the rest are skipped and the job is reported as failed", per
// gradient_types::proto::Job's doc) and reporting JobCompleted only if
// every task succeeded.
func (e *Executor) HandleJob(ctx context.Context, client *Client, conn *Conn, jobID string, job BuildJob) {
	for i, task := range job.Builds {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !e.runTask(ctx, client, conn, jobID, uint32(i), task) {
			return // runTask already reported JobFailed
		}
	}
	if err := conn.SendClientMessage(ctx, EncodeJobCompleted(jobID)); err != nil {
		log.Printf("gradientproto: sending JobCompleted for job %s: %v", jobID, err)
	}
}

// runTask executes one BuildTask and reports its outcome, returning
// whether it succeeded (false means JobFailed was already sent and the
// caller should stop processing the job's remaining tasks).
func (e *Executor) runTask(ctx context.Context, client *Client, conn *Conn, jobID string, taskIndex uint32, task BuildTask) bool {
	if err := conn.SendClientMessage(ctx, EncodeJobUpdateBuilding(jobID, task.BuildID)); err != nil {
		log.Printf("gradientproto: sending JobUpdate{Building} for %s: %v", task.BuildID, err)
		return false
	}

	if task.ExternalCached {
		// gradient_types::proto::BuildTask.external_cached's documented
		// alternate flow (re-query the cache for an upstream URL per
		// output, download, recompress, upload - no daemon build, no
		// input prefetch) is a distinct code path this v1 doesn't
		// implement - see this package's plan doc. Reported as a
		// transient failure (rather than silently attempting - and
		// failing - a normal build) so the scheduler can retry on another
		// worker rather than poisoning the build attempt budget on
		// something this worker can never succeed at.
		e.failTask(ctx, conn, jobID, task.BuildID, "external_cached build tasks are not implemented by this worker", BuildFailureTransient, nil)
		return false
	}

	storeExec := e.storeHostExecer()
	if storeExec == nil {
		e.failTask(ctx, conn, jobID, task.BuildID, "no store host connection available", BuildFailureTransient, nil)
		return false
	}
	drv, system, err := readDerivation(ctx, storeExec, task.DrvPath)
	if err != nil {
		e.failTask(ctx, conn, jobID, task.BuildID, err.Error(), BuildFailureTransient, nil)
		return false
	}

	builderSSH, builderID, err := e.Pool.GetBuilder(task.DrvPath, system)
	if err != nil {
		e.failTask(ctx, conn, jobID, task.BuildID, fmt.Sprintf("provisioning a builder: %v", err), BuildFailureTransient, nil)
		return false
	}
	defer e.Pool.ReleaseBuilder(builderID)

	sink := &logChunkSink{ctx: ctx, conn: conn, jobID: jobID, taskIndex: taskIndex}
	result, err := nixproto.ExecuteBuild(ctx, builderSSH, task.DrvPath, drv, nixproto.BuildModeNormal, sink)
	if err != nil {
		e.failTask(ctx, conn, jobID, task.BuildID, err.Error(), BuildFailureTransient, nil)
		return false
	}
	if !isBuildSuccessStatus(result.Status) {
		e.failTask(ctx, conn, jobID, task.BuildID, result.ErrorMsg, classifyBuildFailure(result.Status), nil)
		return false
	}
	substituted := result.Status != nixproto.BuildResultBuilt

	builderExec := NewSSHExecer(builderSSH)
	outputs := make([]BuildOutput, 0, len(drv.Outputs))
	for name, out := range drv.Outputs {
		outPath, err := resolveOutputPath(name, out, result)
		if err != nil {
			e.failTask(ctx, conn, jobID, task.BuildID, err.Error(), BuildFailureTransient, nil)
			return false
		}

		info, err := uploadOutput(ctx, client, conn, builderExec, e.HTTPClient, jobID, outPath)
		if err != nil {
			e.failTask(ctx, conn, jobID, task.BuildID, err.Error(), BuildFailureTransient, nil)
			return false
		}

		narSize := int64(info.NarSize)
		narHash := info.NarHash
		outputs = append(outputs, BuildOutput{
			Name:      name,
			StorePath: outPath,
			Hash:      storePathHash(outPath),
			NarSize:   &narSize,
			NarHash:   &narHash,
		})
	}

	if err := conn.SendClientMessage(ctx, EncodeJobUpdateBuildOutput(jobID, task.BuildID, outputs, nil, substituted)); err != nil {
		log.Printf("gradientproto: sending JobUpdate{BuildOutput} for %s: %v", task.BuildID, err)
		return false
	}
	return true
}

// failTask sends ClientMessage::JobFailed for the job. ClientMessage::
// JobFailed has no build_id field (only job_id), so buildID is folded into
// errMsg instead - a job can have multiple tasks, and without it the
// server-side error message wouldn't say which one failed.
func (e *Executor) failTask(ctx context.Context, conn *Conn, jobID, buildID, errMsg string, kind BuildFailureKind, missingPaths []string) {
	msg := fmt.Sprintf("build %s: %s", buildID, errMsg)
	if err := conn.SendClientMessage(ctx, EncodeJobFailed(jobID, msg, kind, missingPaths)); err != nil {
		log.Printf("gradientproto: sending JobFailed for job %s: %v", jobID, err)
	}
}

// isBuildSuccessStatus mirrors nixproto/proxy.go's own isSuccess check
// (executeBuildOnBuilder's local `isSuccess` variable) for consistency
// with the ssh-ng path's definition of "the build produced usable
// outputs."
func isBuildSuccessStatus(status nixproto.BuildResultStatus) bool {
	return status == nixproto.BuildResultBuilt ||
		status == nixproto.BuildResultSubstituted ||
		status == nixproto.BuildResultAlreadyValid
}

// classifyBuildFailure maps a nix-daemon BuildResultStatus to Gradient's
// BuildFailureKind, which drives the scheduler's retry decision (see
// BuildFailureKind's doc in client_message.go). CachedFailure and
// TransientFailure are the only statuses nix-daemon itself expects a
// caller to retry; everything else here represents a build that ran and
// failed on its own terms, which is terminal from this worker's
// perspective (retrying the identical derivation on the identical inputs
// would fail identically).
func classifyBuildFailure(status nixproto.BuildResultStatus) BuildFailureKind {
	switch status {
	case nixproto.BuildResultTransientFailure, nixproto.BuildResultCachedFailure:
		return BuildFailureTransient
	case nixproto.BuildResultTimedOut:
		return BuildFailureTimeout
	default:
		return BuildFailurePermanent
	}
}

// resolveOutputPath returns name's final store path after a successful
// build. Input-addressed outputs already carry their (deterministic,
// known-before-build) path directly on the DerivationOutput read from the
// store (see readDerivation). Content-addressed/floating outputs have no
// path until after the build - result.BuiltOutputs (keyed by DrvOutput ID,
// "<hash-algo>:<hash>!<output-name>" - see nixproto.MakeDrvOutputID) is
// searched for an entry whose ID ends in "!<name>" rather than
// reconstructing the exact ID, since the ID's hash component depends on
// derivation-hashing details (ComputeDerivationHash) this package doesn't
// need to duplicate - the output name suffix alone is unambiguous within
// one derivation's BuiltOutputs.
func resolveOutputPath(name string, out nixproto.DerivationOutput, result *nixproto.BuildResult) (string, error) {
	if out.Path != "" {
		return out.Path, nil
	}
	suffix := "!" + name
	for id, real := range result.BuiltOutputs {
		if strings.HasSuffix(id, suffix) {
			return real.OutPath, nil
		}
	}
	return "", fmt.Errorf("could not resolve store path for content-addressed output %q: not present in BuiltOutputs", name)
}

// storePathHash extracts the nix32 hash component from a store path's
// basename (e.g. "ifbychscpzma0mx3x89r2jsg7ak0iwri" from
// "/nix/store/ifbychscpzma0mx3x89r2jsg7ak0iwri-test-drv"), for
// BuildOutput.hash. Gradient's own proto.rs carries no doc comment
// distinguishing BuildOutput.hash from BuildOutput.nar_hash; the store
// path's own embedded hash is the only other well-defined "hash"
// associated with a build output (Hydra's buildoutputs table uses the
// same convention), so that's what's sent here absent a live server to
// confirm the exact expected semantics against.
func storePathHash(path string) string {
	base := nixproto.StorePathBasename(path)
	if i := strings.IndexByte(base, '-'); i >= 0 {
		return base[:i]
	}
	return base
}

// logChunkSink adapts nixproto.StderrSink (the ssh-ng path's build-log
// callback interface) to Gradient's ClientMessage::LogChunk: only
// WriteStderrLog has a Gradient equivalent, so the activity-tracking
// methods (StartActivity/StopActivity/Result - nix's structured progress
// reporting, e.g. download/build phase markers) are no-ops. Errors from
// SendClientMessage are logged, not propagated - a dropped log line
// should never abort an otherwise-successful build.
type logChunkSink struct {
	ctx       context.Context
	conn      *Conn
	jobID     string
	taskIndex uint32
}

func (s *logChunkSink) WriteStderrLog(msg string) error {
	if err := s.conn.SendClientMessage(s.ctx, EncodeLogChunk(s.jobID, s.taskIndex, []byte(msg))); err != nil {
		log.Printf("gradientproto: sending LogChunk for job %s task %d: %v", s.jobID, s.taskIndex, err)
	}
	return nil
}

func (s *logChunkSink) WriteStderrStartActivity(act, level, type_ uint64, text string, fields []nixproto.ActivityField, parent uint64) error {
	return nil
}

func (s *logChunkSink) WriteStderrStopActivity(act uint64) error { return nil }

func (s *logChunkSink) WriteStderrResult(act, type_ uint64, fields []nixproto.ActivityField) error {
	return nil
}
