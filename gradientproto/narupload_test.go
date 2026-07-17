package gradientproto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/klauspost/compress/zstd"
	"nix-builder-provisioner/gradientproto/wire"
)

// recordingConn wraps a real *Conn (dialed against an in-process fake
// WebSocket server - see newFakeGradientServer in handshake_test.go) whose
// server side decodes every ClientMessage::NarPush it receives and
// records it, so pushOverWebSocket can be tested against real wire
// encode/decode without mocking Conn itself (Conn's methods are concrete,
// matching the rest of this package's style - no interface to fake here).
type recordingConn struct {
	*Conn
	mu     sync.Mutex
	chunks []narPushRecord
	// done closes once the server side has seen an is_final=true chunk (or
	// errored/closed) - pushOverWebSocket returning successfully only
	// means the client finished *writing*, not that this package's
	// server-side test goroutine has finished *reading and recording* the
	// last chunk yet (they run concurrently). reassembleNarPush and
	// finalFlags wait on this before inspecting chunks, or its absence is
	// exactly the race this comment is warning about.
	done chan struct{}
}

type narPushRecord struct {
	jobID     string
	storePath string
	data      []byte
	offset    uint64
	isFinal   bool
}

var narPushFieldOff = struct {
	JobID, StorePath, Data, Offset, IsFinal int
}{}

func init() {
	l := wire.TagField()
	narPushFieldOff.JobID = l.Field(wire.StringAlign, wire.StringSize)
	narPushFieldOff.StorePath = l.Field(wire.StringAlign, wire.StringSize)
	narPushFieldOff.Data = l.Field(wire.VecAlign, wire.VecSize)
	narPushFieldOff.Offset = l.Field(8, 8)
	narPushFieldOff.IsFinal = l.Field(1, 1)
}

func newRecordingConn(t *testing.T) (*recordingConn, func()) {
	t.Helper()
	rc := &recordingConn{done: make(chan struct{})}

	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		defer close(rc.done)
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				t.Logf("recordingConn server: Read error (may be expected at teardown): %v", err)
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			d := wire.NewDecoder(data)
			pos := len(data) - clientMessageSize
			if tag := uint32(d.Tag1(pos)); tag == clientTagNarPush {
				rec := narPushRecord{
					jobID:     d.String(pos + narPushFieldOff.JobID),
					storePath: d.String(pos + narPushFieldOff.StorePath),
					offset:    d.U64(pos + narPushFieldOff.Offset),
					isFinal:   d.Bool(pos + narPushFieldOff.IsFinal),
				}
				dataPos := pos + narPushFieldOff.Data
				rec.data = make([]byte, 0, d.VecLen(dataPos))
				d.VecEach(dataPos, 1, func(elemPos, index int) {
					rec.data = append(rec.data, d.Buf[elemPos])
				})
				rc.mu.Lock()
				rc.chunks = append(rc.chunks, rec)
				rc.mu.Unlock()
				if rec.isFinal {
					return
				}
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("Dial: %v", err)
	}
	rc.Conn = conn

	cleanup := func() {
		conn.CloseNow()
		cancel()
	}
	return rc, cleanup
}

// waitDone blocks until the server side has finished recording (an
// is_final chunk seen, or the connection closed/errored) - see done's doc
// comment on recordingConn for the race this guards against.
func (rc *recordingConn) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-rc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recordingConn's server side to finish")
	}
}

// reassembleNarPush concatenates all recorded NarPush chunks for
// (jobID, storePath) in offset order and returns the result, failing the
// test if any offset/is_final invariant is violated (offsets must be
// contiguous, exactly one final chunk, sent last).
func (rc *recordingConn) reassembleNarPush(t *testing.T, jobID, storePath string) []byte {
	t.Helper()
	rc.waitDone(t)
	rc.mu.Lock()
	defer rc.mu.Unlock()

	var out []byte
	var expectOffset uint64
	sawFinal := false
	for i, c := range rc.chunks {
		if c.jobID != jobID || c.storePath != storePath {
			continue
		}
		if sawFinal {
			t.Fatalf("chunk %d arrived after an is_final=true chunk", i)
		}
		if c.offset != expectOffset {
			t.Fatalf("chunk %d offset = %d, want %d (non-contiguous)", i, c.offset, expectOffset)
		}
		out = append(out, c.data...)
		expectOffset += uint64(len(c.data))
		sawFinal = c.isFinal
	}
	if !sawFinal {
		t.Fatal("no is_final=true chunk was recorded")
	}
	return out
}

func (rc *recordingConn) finalFlags(t *testing.T) []bool {
	t.Helper()
	rc.waitDone(t)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	flags := make([]bool, len(rc.chunks))
	for i, c := range rc.chunks {
		flags[i] = c.isFinal
	}
	return flags
}

func TestPushOverWebSocketChunking(t *testing.T) {
	// Build test data spanning multiple boundary cases in one run: a
	// multi-chunk stream whose length is NOT an exact multiple of
	// narPushChunkSize (exercises the common case) via a small chunk size
	// override is not available (narPushChunkSize is a package const), so
	// instead we validate against real narPushChunkSize with a stream a
	// few chunks long plus a partial remainder - large enough to matter,
	// small enough to keep the test fast.
	size := narPushChunkSize*2 + 12345
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}

	conn, cleanup := newRecordingConn(t)
	defer cleanup()

	result, err := pushOverWebSocket(context.Background(), conn.Conn, bytes.NewReader(data), "job-1", "/nix/store/x")
	if err != nil {
		t.Fatalf("pushOverWebSocket: %v", err)
	}

	wantHash := sha256.Sum256(data)
	if result.fileHash != "sha256:"+hex.EncodeToString(wantHash[:]) {
		t.Errorf("fileHash = %s, want sha256:%x", result.fileHash, wantHash)
	}
	if result.fileSize != uint64(size) {
		t.Errorf("fileSize = %d, want %d", result.fileSize, size)
	}

	got := conn.reassembleNarPush(t, "job-1", "/nix/store/x")
	if !bytes.Equal(got, data) {
		t.Errorf("reassembled NarPush chunks don't match original data (got %d bytes, want %d)", len(got), len(data))
	}
}

func TestPushOverWebSocketExactMultiple(t *testing.T) {
	// Regression test: a stream length that's an exact multiple of
	// narPushChunkSize must still produce exactly one is_final=true chunk
	// - see pushOverWebSocket's doc comment on why a naive single-read
	// approach gets this wrong.
	size := narPushChunkSize * 2
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 197)
	}

	conn, cleanup := newRecordingConn(t)
	defer cleanup()

	result, err := pushOverWebSocket(context.Background(), conn.Conn, bytes.NewReader(data), "job-1", "/nix/store/x")
	if err != nil {
		t.Fatalf("pushOverWebSocket: %v", err)
	}
	if result.fileSize != uint64(size) {
		t.Fatalf("fileSize = %d, want %d", result.fileSize, size)
	}

	finals := conn.finalFlags(t)
	finalCount := 0
	for _, f := range finals {
		if f {
			finalCount++
		}
	}
	if finalCount != 1 {
		t.Errorf("got %d is_final=true chunks, want exactly 1 (flags: %v)", finalCount, finals)
	}
	if !finals[len(finals)-1] {
		t.Error("the last chunk sent must be the is_final=true one")
	}

	got := conn.reassembleNarPush(t, "job-1", "/nix/store/x")
	if !bytes.Equal(got, data) {
		t.Error("reassembled data doesn't match original")
	}
}

func TestPushOverWebSocketEmptyStream(t *testing.T) {
	conn, cleanup := newRecordingConn(t)
	defer cleanup()

	result, err := pushOverWebSocket(context.Background(), conn.Conn, bytes.NewReader(nil), "job-1", "/nix/store/x")
	if err != nil {
		t.Fatalf("pushOverWebSocket: %v", err)
	}
	if result.fileSize != 0 {
		t.Errorf("fileSize = %d, want 0", result.fileSize)
	}

	finals := conn.finalFlags(t)
	if len(finals) != 1 || !finals[0] {
		t.Errorf("expected exactly one is_final=true chunk for an empty stream, got %v", finals)
	}
}

// TestCompressNarToPipeRoundTrip validates compressNarToPipe end to end
// against a fake Execer standing in for `nix-store --dump`: the emitted
// stream must be valid zstd that decompresses back to the exact bytes the
// fake command "dumped".
func TestCompressNarToPipeRoundTrip(t *testing.T) {
	original := bytes.Repeat([]byte("nar-content-bytes-"), 10000)

	exec := newFakeExecer(t)
	exec.onStream("nix-store --dump", original, nil)

	pr, pw := io.Pipe()
	go compressNarToPipe(context.Background(), exec, "/nix/store/x", pw)

	dec, err := zstd.NewReader(pr)
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer dec.Close()

	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decompressing: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("decompressed data doesn't match original (got %d bytes, want %d)", len(got), len(original))
	}
}

func TestPutPresigned(t *testing.T) {
	data := bytes.Repeat([]byte("presigned-upload-data"), 1000)

	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
		}
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result, err := putPresigned(context.Background(), srv.Client(), srv.URL, bytes.NewReader(data), -1)
	if err != nil {
		t.Fatalf("putPresigned: %v", err)
	}
	if !bytes.Equal(receivedBody, data) {
		t.Error("server did not receive the exact uploaded bytes")
	}
	wantHash := sha256.Sum256(data)
	if result.fileHash != "sha256:"+hex.EncodeToString(wantHash[:]) {
		t.Errorf("fileHash = %s, want sha256:%x", result.fileHash, wantHash)
	}
	if result.fileSize != uint64(len(data)) {
		t.Errorf("fileSize = %d, want %d", result.fileSize, len(data))
	}
}

func TestPutPresignedNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := putPresigned(context.Background(), srv.Client(), srv.URL, bytes.NewReader([]byte("x")), -1); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}

func TestNewQueryIDUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newQueryID()
		if seen[id] {
			t.Fatalf("newQueryID produced a duplicate: %s", id)
		}
		seen[id] = true
	}
}
