package gradientproto

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// PeerCredentials maps a peer ID (or the wildcard "*") to the plaintext
// token this worker presents when that peer challenges it during the
// handshake. Loaded from a peers file matching
// services.gradient.worker.peersFile's format: one `peer_id:token` pair
// per line, `#`-prefixed lines and blank lines ignored. The special peer
// ID "*" matches any UUID the server challenges, so a single token can
// authorize against any org.
type PeerCredentials map[string]string

// LoadPeersFile parses a peers file at path.
func LoadPeersFile(path string) (PeerCredentials, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gradientproto: opening peers file: %w", err)
	}
	defer f.Close()

	creds := PeerCredentials{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		peerID, token, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("gradientproto: peers file %s line %d: expected \"peer_id:token\", got %q", path, lineNo, line)
		}
		creds[peerID] = token
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gradientproto: reading peers file: %w", err)
	}
	return creds, nil
}

// PeerToken pairs a peer ID with the plaintext token to present for it, in
// the shape EncodeAuthResponse expects.
type PeerToken = struct{ PeerID, Token string }

// TokensFor returns the tokens to present for each challenged peer ID:
// an exact match in the credentials file, falling back to the wildcard
// "*" entry if present. Peer IDs with neither are silently skipped - the
// server reports them back as failed_peers in InitAck, which the caller
// should log.
func (c PeerCredentials) TokensFor(challenged []string) []PeerToken {
	tokens := make([]PeerToken, 0, len(challenged))
	for _, peerID := range challenged {
		if tok, ok := c[peerID]; ok {
			tokens = append(tokens, PeerToken{PeerID: peerID, Token: tok})
		} else if tok, ok := c["*"]; ok {
			tokens = append(tokens, PeerToken{PeerID: peerID, Token: tok})
		}
	}
	return tokens
}
