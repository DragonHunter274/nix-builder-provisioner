package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// FailedPeer mirrors gradient_proto::messages::server::FailedPeer: a peer
// that failed authentication during the handshake challenge-response,
// carried on ServerMessage::InitAck.
type FailedPeer struct {
	PeerID string
	Reason string
}

const failedPeerSize = 2 * wire.StringSize // two Strings, align 4

func decodeFailedPeer(d *wire.Decoder, pos int) FailedPeer {
	return FailedPeer{
		PeerID: d.String(pos + 0),
		Reason: d.String(pos + wire.StringSize),
	}
}
