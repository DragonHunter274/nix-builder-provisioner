package gradientproto

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
)

// ProtoVersion is the wire protocol version this package implements,
// matching gradient_proto::messages::PROTO_VERSION = 7 (v7: CacheQuery/
// CacheStatus/CacheError carry a per-query query_id; NarUploaded carries
// the path's content address).
const ProtoVersion uint16 = 7

// MaxMessageSize mirrors Gradient's session/frame.rs MAX_PROTO_MESSAGE_SIZE:
// comfortably above the largest legitimate frame (a NarPush chunk, 4 MiB
// plus rkyv overhead) while bounding how much a misbehaving peer can make
// us buffer for one message.
const MaxMessageSize = 8 * 1024 * 1024

// Conn wraps a /proto WebSocket connection, sending/receiving whole rkyv
// archives as binary frames with no additional framing - matching
// Gradient's own wire format exactly (see gradient-proto/src/session/frame.rs).
type Conn struct {
	ws *websocket.Conn
}

// Dial opens a /proto WebSocket connection to a Gradient server.
// serverURL should use the wss:// (or ws://, for local testing) scheme,
// e.g. "wss://gradient.example.com/proto". httpClient, if non-nil,
// overrides the HTTP client used for the handshake (e.g. to set a custom
// TLS config); nil uses http.DefaultClient.
func Dial(ctx context.Context, serverURL string, httpClient *http.Client) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, serverURL, &websocket.DialOptions{
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("gradientproto: dialing %s: %w", serverURL, err)
	}
	ws.SetReadLimit(MaxMessageSize)
	return &Conn{ws: ws}, nil
}

// SendClientMessage writes a pre-encoded ClientMessage (from one of the
// EncodeX functions in client_message.go) as a single binary WebSocket frame.
func (c *Conn) SendClientMessage(ctx context.Context, buf []byte) error {
	if err := c.ws.Write(ctx, websocket.MessageBinary, buf); err != nil {
		return fmt.Errorf("gradientproto: sending message: %w", err)
	}
	return nil
}

// RecvServerMessage reads and decodes the next ServerMessage. Returns an
// error on transport failure, non-binary frames (Gradient never sends
// text), or a malformed archive.
func (c *Conn) RecvServerMessage(ctx context.Context) (ServerMessage, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return ServerMessage{}, fmt.Errorf("gradientproto: reading message: %w", err)
	}
	if typ != websocket.MessageBinary {
		return ServerMessage{}, fmt.Errorf("gradientproto: unexpected WebSocket message type %v, want binary", typ)
	}
	msg, err := DecodeServerMessage(data)
	if err != nil {
		return ServerMessage{}, fmt.Errorf("gradientproto: decoding message: %w", err)
	}
	return msg, nil
}

// Close closes the connection with a normal closure code.
func (c *Conn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// CloseNow closes the connection immediately without a close handshake -
// use on error paths where a clean close isn't possible or worth waiting for.
func (c *Conn) CloseNow() error {
	return c.ws.CloseNow()
}
