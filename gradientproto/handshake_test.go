package gradientproto

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"nix-builder-provisioner/gradientproto/wire"
)

// fakeAuthChallenge and fakeInitAck hand-build ServerMessage bytes with the
// low-level wire package directly, since production code has no
// ServerMessage *encoder* (Gradient only ever sends these to us, we never
// need to produce them) - this is test-only scaffolding to exercise the
// real Handshake function against a real WebSocket connection end to end.

func fakeAuthChallenge(peers []string) []byte {
	l := wire.TagField()
	peersOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	peersField := wire.PrepareVec(a, len(peers), wire.StringAlign, wire.StringSize, func(index int) wire.FieldWriter {
		return wire.PrepareString(a, peers[index])
	})
	fields := wire.PrepareStruct(wire.Field{Offset: peersOff, Write: peersField})
	return wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagAuthChallenge, fields)
}

func fakeInitAck(version uint16, caps GradientCapabilities, authorized []string) []byte {
	l := wire.TagField()
	versionOff := l.Field(2, 2)
	capsOff := l.Field(1, capabilitiesSize)
	authorizedOff := l.Field(wire.VecAlign, wire.VecSize)
	failedOff := l.Field(wire.VecAlign, wire.VecSize)

	a := wire.NewArena()
	versionField := wire.PrepareU16(a, version)
	capsField := prepareCapabilities(a, caps)
	authorizedField := prepareStringVec(a, authorized)
	failedField := wire.PrepareVec(a, 0, wire.StringAlign, failedPeerSize, nil)

	fields := wire.PrepareStruct(
		wire.Field{Offset: versionOff, Write: versionField},
		wire.Field{Offset: capsOff, Write: capsField},
		wire.Field{Offset: authorizedOff, Write: authorizedField},
		wire.Field{Offset: failedOff, Write: failedField},
	)
	return wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagInitAck, fields)
}

func fakeReject(code uint16, reason string) []byte {
	l := wire.TagField()
	codeOff := l.Field(2, 2)
	reasonOff := l.Field(wire.StringAlign, wire.StringSize)

	a := wire.NewArena()
	codeField := wire.PrepareU16(a, code)
	reasonField := wire.PrepareString(a, reason)

	fields := wire.PrepareStruct(
		wire.Field{Offset: codeOff, Write: codeField},
		wire.Field{Offset: reasonOff, Write: reasonField},
	)
	return wire.EncodeRoot(a, serverMessageAlign, serverMessageSize, serverTagReject, fields)
}

// newFakeGradientServer starts an httptest server that accepts one /proto
// WebSocket connection and calls handle with the accepted *websocket.Conn.
// Returns the ws:// URL to dial.
func newFakeGradientServer(t *testing.T, handle func(ctx context.Context, ws *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(MaxMessageSize)
		handle(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return "ws" + srv.URL[len("http"):]
}

func TestHandshakeSuccess(t *testing.T) {
	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		// InitConnection in.
		typ, data, err := ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading InitConnection: %v", err)
			return
		}
		d := wire.NewDecoder(data)
		pos := len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagInitConnection {
			t.Errorf("expected InitConnection tag %d, got %d", clientTagInitConnection, tag)
			return
		}

		// AuthChallenge out.
		if err := ws.Write(ctx, websocket.MessageBinary, fakeAuthChallenge([]string{"peer-1"})); err != nil {
			t.Errorf("writing AuthChallenge: %v", err)
			return
		}

		// AuthResponse in.
		typ, data, err = ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Errorf("reading AuthResponse: %v", err)
			return
		}
		d = wire.NewDecoder(data)
		pos = len(data) - clientMessageSize
		if tag := uint32(d.Tag1(pos)); tag != clientTagAuthResponse {
			t.Errorf("expected AuthResponse tag %d, got %d", clientTagAuthResponse, tag)
			return
		}

		// InitAck out.
		caps := GradientCapabilities{Core: true, Build: true}
		if err := ws.Write(ctx, websocket.MessageBinary, fakeInitAck(ProtoVersion, caps, []string{"peer-1"})); err != nil {
			t.Errorf("writing InitAck: %v", err)
			return
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	creds := PeerCredentials{"peer-1": "token-1"}
	result, err := Handshake(ctx, conn, "worker-uuid", GradientCapabilities{Build: true}, creds)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if result.ServerVersion != ProtoVersion {
		t.Errorf("ServerVersion = %d, want %d", result.ServerVersion, ProtoVersion)
	}
	if !result.Negotiated.Core || !result.Negotiated.Build {
		t.Errorf("Negotiated = %+v", result.Negotiated)
	}
	if len(result.AuthorizedPeers) != 1 || result.AuthorizedPeers[0] != "peer-1" {
		t.Errorf("AuthorizedPeers = %v", result.AuthorizedPeers)
	}
}

func TestHandshakeRejected(t *testing.T) {
	url := newFakeGradientServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		ws.Write(ctx, websocket.MessageBinary, fakeReject(401, "unknown worker"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	_, err = Handshake(ctx, conn, "worker-uuid", GradientCapabilities{Build: true}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
