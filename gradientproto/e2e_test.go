package gradientproto

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"nix-builder-provisioner/gradientproto/wire"
)

// cacheQueryFieldOff mirrors the Layout computed locally inside
// EncodeCacheQuery (client_message.go) - see jobFailedFieldOff's doc
// comment in executor_dispatch_test.go for why tests need their own copy
// of offsets production code never exports.
var cacheQueryFieldOff = struct {
	JobID, QueryID, Paths, Mode int
}{}

func init() {
	l := wire.TagField()
	cacheQueryFieldOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	cacheQueryFieldOff.QueryID = l.Field(wire.StringAlign, wire.StringSize)
	cacheQueryFieldOff.Paths = l.Field(wire.VecAlign, wire.VecSize)
	cacheQueryFieldOff.Mode = l.Field(1, 1)
}

// TestFullWorkerLoopEndToEnd exercises the complete worker session in one
// continuous flow against a single fake Gradient server: dial, handshake,
// capability advertisement, pull-based job request, AssignJob dispatch,
// JobUpdate{Building}, a full per-output upload round trip (CacheQuery ->
// CacheStatus -> compressed NarPush chunks -> NarUploaded), JobUpdate
// {BuildOutput}, and JobCompleted.
//
// The one leg this can't exercise without a live builder VM is
// nixproto.ExecuteBuild's SSH-to-nix-daemon exchange (BuilderProvider.
// GetBuilder returns a concrete *ssh.Client, not something fakeable
// without a real SSH server speaking the nix-daemon wire protocol - well
// outside this package's scope, and already covered by nixproto's own
// tests). This test's JobHandler stands in for "the build already
// succeeded" and drives everything downstream of that for real: the
// actual Client, actual wire encode/decode, actual narupload.go
// compression/chunking - only the SSH-to-store and SSH-to-builder legs use
// a fake Execer instead of real SSH.
func TestFullWorkerLoopEndToEnd(t *testing.T) {
	const jobID = "j-e2e"
	const buildID = "b-e2e"
	const storePath = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12"
	narContent := []byte("simulated NAR bytes for the built output, repeated to exceed one chunk boundary in spirit")

	serverErrs := make(chan error, 1)
	completed := make(chan struct{})

	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		fail := func(format string, args ...any) {
			select {
			case serverErrs <- fmt.Errorf(format, args...):
			default:
			}
		}

		// InitConnection -> AuthChallenge (open mode).
		if _, _, err := ws.Read(ctx); err != nil {
			fail("reading InitConnection: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAuthChallenge(nil)); err != nil {
			fail("writing AuthChallenge: %v", err)
			return
		}

		// AuthResponse -> InitAck.
		if _, _, err := ws.Read(ctx); err != nil {
			fail("reading AuthResponse: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeInitAck(ProtoVersion, GradientCapabilities{Build: true}, nil)); err != nil {
			fail("writing InitAck: %v", err)
			return
		}

		// WorkerCapabilities.
		if _, data, err := ws.Read(ctx); err != nil || uint32(wire.NewDecoder(data).Tag1(len(data)-clientMessageSize)) != clientTagWorkerCapabilities {
			fail("expected WorkerCapabilities: err=%v", err)
			return
		}

		// RequestJob.
		if _, data, err := ws.Read(ctx); err != nil || uint32(wire.NewDecoder(data).Tag1(len(data)-clientMessageSize)) != clientTagRequestJob {
			fail("expected RequestJob: err=%v", err)
			return
		}

		// Assign the job.
		job := BuildJob{Builds: []BuildTask{{BuildID: buildID, DrvPath: "/nix/store/x.drv"}}}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAssignJob(jobID, job)); err != nil {
			fail("writing AssignJob: %v", err)
			return
		}

		// AssignJobResponse.
		if _, data, err := ws.Read(ctx); err != nil || uint32(wire.NewDecoder(data).Tag1(len(data)-clientMessageSize)) != clientTagAssignJobResponse {
			fail("expected AssignJobResponse: err=%v", err)
			return
		}

		// JobUpdate{Building}.
		typ, data, err := ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			fail("reading JobUpdate{Building}: %v", err)
			return
		}
		d := wire.NewDecoder(data)
		pos := len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagJobUpdate {
			fail("expected JobUpdate, got tag %d", tag)
			return
		}
		if updTag := uint32(d.Tag1(pos + jobUpdateFieldsOff.Update)); updTag != jobUpdateTagBuilding {
			fail("expected JobUpdateKind::Building, got tag %d", updTag)
			return
		}

		// CacheQuery -> respond uncached, no presigned URL (forces the
		// WebSocket NarPush path).
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			fail("reading CacheQuery: %v", err)
			return
		}
		d = wire.NewDecoder(data)
		pos = len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagCacheQuery {
			fail("expected CacheQuery, got tag %d", tag)
			return
		}
		queryID := d.String(pos + cacheQueryFieldOff.QueryID)
		if err := ws.Write(ctx, websocket.MessageBinary, fakeCacheStatus(queryID, []CachedPath{
			{Path: storePath, Cached: false},
		})); err != nil {
			fail("writing CacheStatus: %v", err)
			return
		}

		// NarPush chunk(s) followed by NarUploaded.
		var reassembled []byte
		for {
			typ, data, err = ws.Read(ctx)
			if err != nil {
				fail("reading NarPush/NarUploaded: %v", err)
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			d = wire.NewDecoder(data)
			pos = len(data) - clientMessageSize
			tag := uint32(d.Tag1(pos))
			if tag == clientTagNarPush {
				dataPos := pos + narPushFieldOff.Data
				n := d.VecLen(dataPos)
				d.VecEach(dataPos, 1, func(elemPos, index int) {
					reassembled = append(reassembled, d.Buf[elemPos])
				})
				_ = n
				continue
			}
			if tag == clientTagNarUploaded {
				break
			}
			fail("expected NarPush or NarUploaded, got tag %d", tag)
			return
		}

		// JobUpdate{BuildOutput}.
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			fail("reading JobUpdate{BuildOutput}: %v", err)
			return
		}
		d = wire.NewDecoder(data)
		pos = len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagJobUpdate {
			fail("expected JobUpdate{BuildOutput}, got tag %d", tag)
			return
		}
		if updTag := uint32(d.Tag1(pos + jobUpdateFieldsOff.Update)); updTag != jobUpdateTagBuildOutput {
			fail("expected JobUpdateKind::BuildOutput, got tag %d", updTag)
			return
		}

		// JobCompleted.
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			fail("reading JobCompleted: %v", err)
			return
		}
		if tag := uint32(wire.NewDecoder(data).Tag1(len(data) - clientMessageSize)); tag != clientTagJobCompleted {
			fail("expected JobCompleted, got tag %d", tag)
			return
		}

		close(completed)
	})

	exec := newFakeExecer(t)
	pathInfoJSON := fmt.Sprintf(`{%q:{"ca":null,"deriver":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-hello-2.12.drv","narHash":"sha256-EFUdrtf6Rn0LWIJufrmg8q99aT3jGfLvd1//zaJEufY=","narSize":120,"references":[],"registrationTime":1784276209,"signatures":[],"storeDir":"/nix/store","ultimate":false,"version":1}}`, storePath)
	exec.onOutput("nix path-info --json", []byte(pathInfoJSON), nil)
	exec.onStream("nix-store --dump", narContent, nil)

	handler := func(ctx context.Context, client *Client, conn *Conn, gotJobID string, job BuildJob) {
		if gotJobID != jobID || len(job.Builds) != 1 || job.Builds[0].BuildID != buildID {
			t.Errorf("handler got unexpected job: id=%q job=%+v", gotJobID, job)
		}
		if err := conn.SendClientMessage(ctx, EncodeJobUpdateBuilding(gotJobID, buildID)); err != nil {
			t.Errorf("sending JobUpdate{Building}: %v", err)
			return
		}

		info, err := uploadOutput(ctx, client, conn, exec, nil, gotJobID, storePath)
		if err != nil {
			t.Errorf("uploadOutput: %v", err)
			return
		}

		narSize := int64(info.NarSize)
		narHash := info.NarHash
		output := BuildOutput{
			Name:      "out",
			StorePath: storePath,
			Hash:      storePathHash(storePath),
			NarSize:   &narSize,
			NarHash:   &narHash,
		}
		if err := conn.SendClientMessage(ctx, EncodeJobUpdateBuildOutput(gotJobID, buildID, []BuildOutput{output}, nil, false)); err != nil {
			t.Errorf("sending JobUpdate{BuildOutput}: %v", err)
			return
		}
		if err := conn.SendClientMessage(ctx, EncodeJobCompleted(gotJobID)); err != nil {
			t.Errorf("sending JobCompleted: %v", err)
		}
	}

	client := NewClient(ClientConfig{
		ServerURL:           url,
		PeerID:              "worker-uuid",
		Architectures:       []string{"x86_64-linux"},
		MaxConcurrentBuilds: 1,
	}, handler)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go client.Run(ctx)

	select {
	case <-completed:
	case err := <-serverErrs:
		t.Fatalf("fake server error: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the full end-to-end session to complete")
	}
}
