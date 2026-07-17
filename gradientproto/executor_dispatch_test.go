package gradientproto

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"nix-builder-provisioner/gradientproto/wire"
)

// fakeBuilderProvider is a nixproto.BuilderProvider test double. It never
// needs to produce a real *ssh.Client for these tests: every case here
// exercises a failure path that returns before nixproto.ExecuteBuild would
// need to actually use the connection (GetBuilder itself erroring, or
// readDerivation failing even earlier) - see executor_dispatch_test.go's
// package doc for why a success-path test isn't feasible without a live
// SSH server.
type fakeBuilderProvider struct {
	mu       sync.Mutex
	calls    []struct{ drvPath, platform string }
	getErr   error
	released []string
}

func (f *fakeBuilderProvider) GetBuilder(drvPath, platform string) (*ssh.Client, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, struct{ drvPath, platform string }{drvPath, platform})
	f.mu.Unlock()
	if f.getErr != nil {
		return nil, "", f.getErr
	}
	return nil, "", fmt.Errorf("fakeBuilderProvider: no real *ssh.Client available in tests")
}

func (f *fakeBuilderProvider) ReleaseBuilder(builderID string) {
	f.mu.Lock()
	f.released = append(f.released, builderID)
	f.mu.Unlock()
}

func (f *fakeBuilderProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// jobFailedFieldOff/jobUpdateBuildingFieldOff mirror the Layout computed
// locally inside EncodeJobFailed/prepareJobUpdateBuilding (client_message.go
// /job_update.go) - those functions don't export their offsets since
// production code never needs to decode its own messages, but tests
// decoding a captured ClientMessage need the same layout to read fields
// back out.
var jobFailedFieldOff = struct {
	JobID, Error, Kind, Missing int
}{}

func init() {
	l := wire.TagField()
	jobFailedFieldOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	jobFailedFieldOff.Error = l.Field(wire.StringAlign, wire.StringSize)
	jobFailedFieldOff.Kind = l.Field(1, 1)
	jobFailedFieldOff.Missing = l.Field(wire.VecAlign, wire.VecSize)
}

type decodedMessage struct {
	tag     uint32
	jobID   string
	errMsg  string
	kind    BuildFailureKind
	buildID string // JobUpdate{Building} only
}

// messageRecorder is a JobHandler-agnostic recorder of every ClientMessage
// a session sends, decoding just enough of JobUpdate{Building}/JobFailed/
// JobCompleted to assert on in tests - a generalized version of
// narupload_test.go's recordingConn, since executor tests need to observe
// several different message kinds rather than just NarPush.
type messageRecorder struct {
	mu       sync.Mutex
	messages []decodedMessage
	done     chan struct{}
}

func newMessageRecorderServer(t *testing.T, job BuildJob) (*messageRecorder, string) {
	t.Helper()
	rec := &messageRecorder{done: make(chan struct{})}

	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		defer close(rec.done)
		if _, _, err := ws.Read(ctx); err != nil { // InitConnection
			return
		}
		ws.Write(ctx, websocket.MessageBinary, fakeAuthChallenge(nil))
		if _, _, err := ws.Read(ctx); err != nil { // AuthResponse
			return
		}
		ws.Write(ctx, websocket.MessageBinary, fakeInitAck(ProtoVersion, GradientCapabilities{Build: true}, nil))
		if _, _, err := ws.Read(ctx); err != nil { // WorkerCapabilities
			return
		}
		if _, _, err := ws.Read(ctx); err != nil { // RequestJob
			return
		}

		if err := ws.Write(ctx, websocket.MessageBinary, fakeAssignJob("j-1", job)); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil { // AssignJobResponse
			return
		}

		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			pos := len(data) - clientMessageSize
			d := wire.NewDecoder(data)
			tag := uint32(d.Tag1(pos))

			msg := decodedMessage{tag: tag}
			switch tag {
			case clientTagJobFailed:
				msg.jobID = d.String(pos + jobFailedFieldOff.JobID)
				msg.errMsg = d.String(pos + jobFailedFieldOff.Error)
				msg.kind = BuildFailureKind(d.Tag1(pos + jobFailedFieldOff.Kind))
			case clientTagJobUpdate:
				msg.jobID = d.String(pos + jobUpdateFieldsOff.JobID)
				updatePos := pos + jobUpdateFieldsOff.Update
				if updateTag := uint32(d.Tag1(updatePos)); updateTag == jobUpdateTagBuilding {
					msg.buildID = d.String(updatePos + jobUpdateBuildingOff.BuildID)
				}
			case clientTagJobCompleted:
				msg.jobID = d.String(pos) // JobCompleted{job_id} - single field at offset 0 (no TagField-derived struct needed)
			}

			rec.mu.Lock()
			rec.messages = append(rec.messages, msg)
			rec.mu.Unlock()

			if tag == clientTagJobFailed || tag == clientTagJobCompleted {
				return
			}
		}
	})
	return rec, url
}

func (r *messageRecorder) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for messageRecorder's server side to finish")
	}
}

func (r *messageRecorder) messagesWithTag(tag uint32) []decodedMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []decodedMessage
	for _, m := range r.messages {
		if m.tag == tag {
			out = append(out, m)
		}
	}
	return out
}

func runExecutorTest(t *testing.T, job BuildJob, storeExec Execer, pool *fakeBuilderProvider) *messageRecorder {
	t.Helper()
	rec, url := newMessageRecorderServer(t, job)

	exec := NewExecutor(pool, storeExec, nil)
	client := NewClient(ClientConfig{
		ServerURL:           url,
		PeerID:              "worker-uuid",
		Architectures:       []string{"x86_64-linux"},
		MaxConcurrentBuilds: 1,
	}, exec.HandleJob)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go client.Run(ctx)
	rec.waitDone(t)
	cancel()
	return rec
}

func TestExecutorExternalCachedNotImplemented(t *testing.T) {
	job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/x.drv", ExternalCached: true}}}
	pool := &fakeBuilderProvider{}

	rec := runExecutorTest(t, job, newFakeExecer(t), pool)

	if pool.callCount() != 0 {
		t.Errorf("GetBuilder was called %d times, want 0 (external_cached must fail before requesting a builder)", pool.callCount())
	}
	failed := rec.messagesWithTag(clientTagJobFailed)
	if len(failed) != 1 {
		t.Fatalf("got %d JobFailed messages, want 1", len(failed))
	}
	if failed[0].kind != BuildFailureTransient {
		t.Errorf("kind = %v, want BuildFailureTransient", failed[0].kind)
	}
	if failed[0].jobID != "j-1" {
		t.Errorf("jobID = %q", failed[0].jobID)
	}
}

func TestExecutorReadDerivationFailure(t *testing.T) {
	job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/x.drv"}}}
	pool := &fakeBuilderProvider{}

	exec := newFakeExecer(t)
	exec.onOutput("nix derivation show --recursive", nil, fmt.Errorf("simulated SSH failure"))

	rec := runExecutorTest(t, job, exec, pool)

	if pool.callCount() != 0 {
		t.Errorf("GetBuilder was called %d times, want 0 (must not request a builder before the derivation is readable)", pool.callCount())
	}
	failed := rec.messagesWithTag(clientTagJobFailed)
	if len(failed) != 1 {
		t.Fatalf("got %d JobFailed messages, want 1", len(failed))
	}
	if failed[0].kind != BuildFailureTransient {
		t.Errorf("kind = %v, want BuildFailureTransient", failed[0].kind)
	}
}

func TestExecutorGetBuilderFailure(t *testing.T) {
	job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/md068ia0dnlncb345b9n3ggaky3hvvxl-test-dep.drv"}}}
	pool := &fakeBuilderProvider{getErr: fmt.Errorf("no capacity")}

	exec := newFakeExecer(t)
	exec.onOutputFile("nix derivation show --recursive", nixJSONFixture(t, "derivation_show_dep.json"))

	rec := runExecutorTest(t, job, exec, pool)

	if pool.callCount() != 1 {
		t.Fatalf("GetBuilder was called %d times, want 1", pool.callCount())
	}
	if pool.calls[0].platform != "x86_64-linux" {
		t.Errorf("GetBuilder platform = %q, want x86_64-linux (from the derivation's own system field)", pool.calls[0].platform)
	}
	failed := rec.messagesWithTag(clientTagJobFailed)
	if len(failed) != 1 {
		t.Fatalf("got %d JobFailed messages, want 1", len(failed))
	}
	if failed[0].kind != BuildFailureTransient {
		t.Errorf("kind = %v, want BuildFailureTransient", failed[0].kind)
	}
}

// TestExecutorMultiTaskStopsOnFirstFailure verifies Job semantics ("an
// ordered sequence of tasks - if any task fails, the rest are skipped" -
// gradient_types::proto::Job's doc): a job with two tasks where the first
// fails must never attempt the second.
func TestExecutorMultiTaskStopsOnFirstFailure(t *testing.T) {
	job := BuildJob{Builds: []BuildTask{
		{BuildID: "b-1", DrvPath: "/nix/store/first.drv", ExternalCached: true},
		{BuildID: "b-2", DrvPath: "/nix/store/second.drv", ExternalCached: true},
	}}
	pool := &fakeBuilderProvider{}

	rec := runExecutorTest(t, job, newFakeExecer(t), pool)

	building := rec.messagesWithTag(clientTagJobUpdate)
	if len(building) != 1 {
		t.Fatalf("got %d JobUpdate{Building} messages, want exactly 1 (only the first task should start)", len(building))
	}
	if building[0].buildID != "b-1" {
		t.Errorf("first Building update was for %q, want b-1", building[0].buildID)
	}

	failed := rec.messagesWithTag(clientTagJobFailed)
	if len(failed) != 1 {
		t.Fatalf("got %d JobFailed messages, want exactly 1 (job fails once, not once per task)", len(failed))
	}
}
