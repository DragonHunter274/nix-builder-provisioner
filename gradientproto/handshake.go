package gradientproto

import (
	"context"
	"fmt"
)

// HandshakeResult is returned by Handshake on success.
type HandshakeResult struct {
	// Negotiated is the capability set the server accepted for this
	// session (may be a subset of what was advertised).
	Negotiated GradientCapabilities
	// AuthorizedPeers are the peer IDs whose tokens were accepted.
	AuthorizedPeers []string
	// FailedPeers are peers whose tokens were missing or invalid - log
	// these, don't treat them as fatal (open/discoverable mode tolerates
	// an empty peers file entirely).
	FailedPeers []FailedPeer
	// ServerVersion is the protocol version the server reported in InitAck.
	ServerVersion uint16
}

// Handshake runs the worker side of the /proto handshake on an
// already-dialed connection: InitConnection -> AuthChallenge ->
// AuthResponse -> InitAck. Mirrors gradient_proto::session::handshake::as_peer.
//
// peerID is this worker's persistent UUID (generated once and reused
// across reconnects - see the workerId option in
// services.gradient.worker.nix for why that matters). capabilities is
// this worker's advertised feature set (in v1, just Build). creds may be
// nil for open/discoverable mode, where the server accepts the connection
// without token validation.
func Handshake(ctx context.Context, conn *Conn, peerID string, capabilities GradientCapabilities, creds PeerCredentials) (HandshakeResult, error) {
	if err := conn.SendClientMessage(ctx, EncodeInitConnection(ProtoVersion, capabilities, peerID)); err != nil {
		return HandshakeResult{}, err
	}

	challenge, err := conn.RecvServerMessage(ctx)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: receiving AuthChallenge: %w", err)
	}
	switch challenge.Kind {
	case ServerMsgAuthChallenge:
		// proceed below
	case ServerMsgReject:
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: server rejected connection (code %d): %s", challenge.Code, challenge.Reason)
	default:
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: expected AuthChallenge, got message kind %v", challenge.Kind)
	}

	tokens := creds.TokensFor(challenge.Peers)
	if err := conn.SendClientMessage(ctx, EncodeAuthResponse(tokens)); err != nil {
		return HandshakeResult{}, err
	}

	ack, err := conn.RecvServerMessage(ctx)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: receiving InitAck: %w", err)
	}
	switch ack.Kind {
	case ServerMsgInitAck:
		return HandshakeResult{
			Negotiated:      ack.Capabilities,
			AuthorizedPeers: ack.AuthorizedPeers,
			FailedPeers:     ack.FailedPeers,
			ServerVersion:   ack.Version,
		}, nil
	case ServerMsgReject:
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: server rejected connection (code %d): %s", ack.Code, ack.Reason)
	default:
		return HandshakeResult{}, fmt.Errorf("gradientproto: handshake: expected InitAck, got message kind %v", ack.Kind)
	}
}
