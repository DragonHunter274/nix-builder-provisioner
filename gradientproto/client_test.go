package gradientproto

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"nix-builder-provisioner/gradientproto/wire"
)

func fakeAssignJob(jobID string, job BuildJob) []byte {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	jobOff := l.Field(jobAlign, jobSize)

	a := wire.NewArena()
	jobIDField := wire.PrepareString(a, jobID)

	tasksField := wire.PrepareVec(a, len(job.Builds), buildTaskOff.Align, buildTaskOff.Size, func(index int) wire.FieldWriter {
		return prepareFakeBuildTask(a, job.Builds[index])
	})
	// BuildJob's own Vec field must be offset via TagField() too, matching
	// DecodeJob's pos+4 (tag@0, then align4 padding pushes the Vec to 4) -
	// PrepareTaggedUnion calls buildJobFields at the Job's own position with
	// no adjustment, trusting the offsets passed in already account for the
	// tag (see TagField's doc in wire/arena.go).
	bjl := wire.TagField()
	buildsOff := bjl.Field(wire.VecAlign, wire.VecSize)
	buildJobFields := wire.PrepareStruct(wire.Field{Offset: buildsOff, Write: tasksField})
	jobField := wire.PrepareTaggedUnion(a, jobTagBuild, buildJobFields)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobIDOff, Write: jobIDField},
		wire.Field{Offset: jobOff, Write: jobField},
	)
	return wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagAssignJob, fields)
}

// prepareFakeBuildTask is test-only: production code never encodes
// BuildTask (the server sends it to us), so there's no prepareBuildTask in
// build_types.go to reuse here.
func prepareFakeBuildTask(a *wire.Arena, t BuildTask) wire.FieldWriter {
	buildIDField := wire.PrepareString(a, t.BuildID)
	drvPathField := wire.PrepareString(a, t.DrvPath)
	externalCachedField := wire.PrepareBool(a, t.ExternalCached)
	isFixedOutputField := wire.PrepareBool(a, t.IsFixedOutput)
	outputsField := wire.PrepareVec(a, len(t.Outputs), wire.StringAlign, derivationOutputSize, func(index int) wire.FieldWriter {
		out := t.Outputs[index]
		nameField := wire.PrepareString(a, out.Name)
		pathField := wire.PrepareString(a, out.Path)
		return wire.PrepareStruct(
			wire.Field{Offset: 0, Write: nameField},
			wire.Field{Offset: wire.StringSize, Write: pathField},
		)
	})
	timeoutField := wire.PrepareOptionNone()
	if t.TimeoutSecs != nil {
		timeoutField = wire.PrepareOptionSome(a, 8, wire.PrepareU64(a, *t.TimeoutSecs))
	}
	maxSilentField := wire.PrepareOptionNone()
	if t.MaxSilentSecs != nil {
		maxSilentField = wire.PrepareOptionSome(a, 8, wire.PrepareU64(a, *t.MaxSilentSecs))
	}
	return wire.PrepareStruct(
		wire.Field{Offset: buildTaskOff.BuildID, Write: buildIDField},
		wire.Field{Offset: buildTaskOff.DrvPath, Write: drvPathField},
		wire.Field{Offset: buildTaskOff.ExternalCached, Write: externalCachedField},
		wire.Field{Offset: buildTaskOff.IsFixedOutput, Write: isFixedOutputField},
		wire.Field{Offset: buildTaskOff.Outputs, Write: outputsField},
		wire.Field{Offset: buildTaskOff.TimeoutSecs, Write: timeoutField},
		wire.Field{Offset: buildTaskOff.MaxSilentSecs, Write: maxSilentField},
	)
}

func TestClientRunFullSession(t *testing.T) {
	const maxBuilds = 2
	requestJobCount := 0
	var mu sync.Mutex
	assignJobResponseReceived := make(chan bool, 1)
	jobCompletedSent := make(chan struct{})
	serverDone := make(chan struct{})

	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		// InitConnection -> AuthChallenge (open mode: empty challenge).
		if _, _, err := ws.Read(ctx); err != nil {
			t.Errorf("reading InitConnection: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAuthChallenge(nil)); err != nil {
			t.Errorf("writing AuthChallenge: %v", err)
			return
		}
		// AuthResponse -> InitAck.
		if _, _, err := ws.Read(ctx); err != nil {
			t.Errorf("reading AuthResponse: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeInitAck(ProtoVersion, GradientCapabilities{Build: true}, nil)); err != nil {
			t.Errorf("writing InitAck: %v", err)
			return
		}

		// WorkerCapabilities.
		typ, data, err := ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading WorkerCapabilities: %v", err)
			return
		}
		if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag != clientTagWorkerCapabilities {
			t.Errorf("expected WorkerCapabilities tag, got %d", tag)
		}

		// maxBuilds RequestJob signals.
		for i := 0; i < maxBuilds; i++ {
			typ, data, err := ws.Read(ctx)
			if err != nil || typ != websocket.MessageBinary {
				t.Errorf("reading RequestJob %d: %v", i, err)
				return
			}
			if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag != clientTagRequestJob {
				t.Errorf("expected RequestJob tag, got %d", tag)
				return
			}
			mu.Lock()
			requestJobCount++
			mu.Unlock()
		}

		// Assign one job.
		job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/x.drv"}}}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAssignJob("j-1", job)); err != nil {
			t.Errorf("writing AssignJob: %v", err)
			return
		}

		// Expect AssignJobResponse{accepted: true}.
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading AssignJobResponse: %v", err)
			return
		}
		if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag != clientTagAssignJobResponse {
			t.Errorf("expected AssignJobResponse tag, got %d", tag)
		}
		assignJobResponseReceived <- true

		// Expect JobCompleted from the handler, then one more RequestJob
		// (capacity freed up).
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading JobCompleted: %v", err)
			return
		}
		if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag != clientTagJobCompleted {
			t.Errorf("expected JobCompleted tag, got %d", tag)
		}
		close(jobCompletedSent)

		// The trailing RequestJob is sent from dispatch's deferred cleanup
		// goroutine, which runs concurrently with (not synchronized to)
		// the JobCompleted read above - block here, not just on
		// jobCompletedSent, so the test doesn't cancel the session before
		// that goroutine gets a chance to send it (a real race: cancelling
		// too early turns this into a flaky "context canceled" send error
		// instead of a clean read).
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			close(serverDone)
			return
		}
		if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag == clientTagRequestJob {
			mu.Lock()
			requestJobCount++
			mu.Unlock()
		}
		close(serverDone)
	})

	handlerCalled := make(chan struct{})
	handler := func(ctx context.Context, client *Client, conn *Conn, jobID string, job BuildJob) {
		close(handlerCalled)
		if jobID != "j-1" || len(job.Builds) != 1 || job.Builds[0].BuildID != "b-1" {
			t.Errorf("handler got jobID=%q job=%+v", jobID, job)
		}
		conn.SendClientMessage(ctx, EncodeJobCompleted(jobID))
	}

	client := NewClient(ClientConfig{
		ServerURL:           url,
		PeerID:              "worker-uuid",
		Architectures:       []string{"x86_64-linux"},
		MaxConcurrentBuilds: maxBuilds,
	}, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	select {
	case <-handlerCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for JobHandler to be called")
	}
	select {
	case <-assignJobResponseReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for AssignJobResponse")
	}
	select {
	case <-jobCompletedSent:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for JobCompleted")
	}
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the fake server to finish reading the trailing RequestJob")
	}

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if requestJobCount != maxBuilds+1 {
		t.Errorf("requestJobCount = %d, want %d (initial %d + 1 after completion)", requestJobCount, maxBuilds+1, maxBuilds)
	}
}

func TestClientAbortJob(t *testing.T) {
	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		ws.Write(ctx, websocket.MessageBinary, fakeAuthChallenge(nil))
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		ws.Write(ctx, websocket.MessageBinary, fakeInitAck(ProtoVersion, GradientCapabilities{Build: true}, nil))
		if _, _, err := ws.Read(ctx); err != nil { // WorkerCapabilities
			return
		}
		if _, _, err := ws.Read(ctx); err != nil { // RequestJob
			return
		}

		job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/x.drv"}}}
		ws.Write(ctx, websocket.MessageBinary, fakeAssignJob("j-1", job))
		if _, _, err := ws.Read(ctx); err != nil { // AssignJobResponse
			return
		}

		l := wire.TagField()
		jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
		reasonOff := l.Field(wire.StringAlign, wire.StringSize)
		a := wire.NewArena()
		fields := wire.PrepareStruct(
			wire.Field{Offset: jobIDOff, Write: wire.PrepareString(a, "j-1")},
			wire.Field{Offset: reasonOff, Write: wire.PrepareString(a, "cancelled")},
		)
		abortJob := wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagAbortJob, fields)
		ws.Write(ctx, websocket.MessageBinary, abortJob)

		// Wait for the handler to notice cancellation and stop (best-effort
		// drain so the write above isn't racing connection teardown).
		ws.Read(ctx)
	})

	ctxCancelled := make(chan struct{})
	handler := func(ctx context.Context, client *Client, conn *Conn, jobID string, job BuildJob) {
		<-ctx.Done()
		close(ctxCancelled)
	}

	client := NewClient(ClientConfig{
		ServerURL:           url,
		PeerID:              "worker-uuid",
		Architectures:       []string{"x86_64-linux"},
		MaxConcurrentBuilds: 1,
	}, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go client.Run(ctx)

	select {
	case <-ctxCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for AbortJob to cancel the job context")
	}
}

func fakeCacheStatus(queryID string, cached []CachedPath) []byte {
	l := wire.TagField()
	queryIDOff := l.Field(wire.StringAlign, wire.StringSize)
	cachedOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	queryIDField := wire.PrepareString(a, queryID)
	cachedField := wire.PrepareVec(a, len(cached), cachedPathOff.Align, cachedPathOff.Size, func(index int) wire.FieldWriter {
		return prepareFakeCachedPath(a, cached[index])
	})
	fields := wire.PrepareStruct(
		wire.Field{Offset: queryIDOff, Write: queryIDField},
		wire.Field{Offset: cachedOff, Write: cachedField},
	)
	return wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagCacheStatus, fields)
}

func prepareFakeCachedPath(a *wire.Arena, cp CachedPath) wire.FieldWriter {
	pathField := wire.PrepareString(a, cp.Path)
	cachedField := wire.PrepareBool(a, cp.Cached)
	fileSizeField := prepareOptU64(a, cp.FileSize)
	narSizeField := prepareOptU64(a, cp.NarSize)
	urlField := wire.PrepareOptionNone()
	if cp.URL != nil {
		urlField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *cp.URL))
	}
	narHashField := wire.PrepareOptionNone()
	if cp.NarHash != nil {
		narHashField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *cp.NarHash))
	}
	fileHashField := wire.PrepareOptionNone()
	if cp.FileHash != nil {
		fileHashField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *cp.FileHash))
	}
	refsField := wire.PrepareOptionNone()
	if cp.References != nil {
		refsField = wire.PrepareOptionSome(a, wire.VecAlign, prepareStringVec(a, *cp.References))
	}
	sigsField := wire.PrepareOptionNone()
	if cp.Signatures != nil {
		sigsField = wire.PrepareOptionSome(a, wire.VecAlign, prepareStringVec(a, *cp.Signatures))
	}
	deriverField := wire.PrepareOptionNone()
	if cp.Deriver != nil {
		deriverField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *cp.Deriver))
	}
	caField := wire.PrepareOptionNone()
	if cp.CA != nil {
		caField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *cp.CA))
	}
	return wire.PrepareStruct(
		wire.Field{Offset: cachedPathOff.Path, Write: pathField},
		wire.Field{Offset: cachedPathOff.Cached, Write: cachedField},
		wire.Field{Offset: cachedPathOff.FileSize, Write: fileSizeField},
		wire.Field{Offset: cachedPathOff.NarSize, Write: narSizeField},
		wire.Field{Offset: cachedPathOff.URL, Write: urlField},
		wire.Field{Offset: cachedPathOff.NarHash, Write: narHashField},
		wire.Field{Offset: cachedPathOff.FileHash, Write: fileHashField},
		wire.Field{Offset: cachedPathOff.References, Write: refsField},
		wire.Field{Offset: cachedPathOff.Signatures, Write: sigsField},
		wire.Field{Offset: cachedPathOff.Deriver, Write: deriverField},
		wire.Field{Offset: cachedPathOff.CA, Write: caField},
	)
}

// TestClientQueryCache exercises the CacheQuery/CacheStatus waiter
// registry (client.go's QueryCache/routeCacheResponse) via a real assigned
// job, validating that Run's single reader loop correctly routes a
// CacheStatus response back to the JobHandler goroutine blocked waiting
// for it - the concurrency case this registry exists for (see
// JobHandler's doc comment on why a handler can't just call
// conn.RecvServerMessage itself).
func TestClientQueryCache(t *testing.T) {
	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
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

		job := BuildJob{Builds: []BuildTask{{BuildID: "b-1", DrvPath: "/nix/store/x.drv"}}}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAssignJob("j-1", job)); err != nil {
			t.Errorf("writing AssignJob: %v", err)
			return
		}
		if _, _, err := ws.Read(ctx); err != nil { // AssignJobResponse
			return
		}

		typ, data, err := ws.Read(ctx) // CacheQuery
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading CacheQuery: %v", err)
			return
		}
		d := wire.NewDecoder(data)
		pos := len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagCacheQuery {
			t.Errorf("expected CacheQuery tag, got %d", tag)
			return
		}

		resp := fakeCacheStatus("q-1", []CachedPath{
			{Path: "/nix/store/xxx", Cached: true, URL: strPtr("https://s3.example.com/put")},
		})
		if err := ws.Write(ctx, websocket.MessageBinary, resp); err != nil {
			t.Errorf("writing CacheStatus: %v", err)
		}

		// Drain until the connection closes.
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	})

	resultCh := make(chan ServerMessage, 1)
	errCh := make(chan error, 1)
	handler := func(ctx context.Context, client *Client, conn *Conn, jobID string, job BuildJob) {
		msg, err := client.QueryCache(ctx, conn, jobID, "q-1", []string{"/nix/store/xxx"}, QueryModePush)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- msg
	}

	client := NewClient(ClientConfig{
		ServerURL:           url,
		PeerID:              "worker-uuid",
		Architectures:       []string{"x86_64-linux"},
		MaxConcurrentBuilds: 1,
	}, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go client.Run(ctx)

	select {
	case msg := <-resultCh:
		if msg.Kind != ServerMsgCacheStatus || msg.QueryID != "q-1" {
			t.Errorf("got %+v", msg)
		}
		if len(msg.Cached) != 1 || !msg.Cached[0].Cached || msg.Cached[0].URL == nil {
			t.Errorf("Cached = %+v", msg.Cached)
		}
	case err := <-errCh:
		t.Fatalf("QueryCache error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for QueryCache result")
	}
}

func strPtr(s string) *string { return &s }
