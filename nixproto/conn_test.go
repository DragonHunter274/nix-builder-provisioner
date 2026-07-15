package nixproto

import (
	"bytes"
	"testing"
)

// sentinelAfterHandshake is an arbitrary marker written right after the
// handshake bytes, standing in for the next thing a real session would read
// (e.g. the first operation's opcode). If the handshake consumes the wrong
// number of bytes, the next read comes back as something other than this
// value instead of an I/O error - bufio.Reader fills its internal buffer
// ahead of what Handshake() logically consumes, so checking the underlying
// reader's remaining byte count does NOT detect a desync; reading the next
// logical field and comparing it to this sentinel does.
const sentinelAfterHandshake uint64 = 0xdeadbeefcafebabe

// buildClientHandshakeBytes constructs the bytes a real Nix client sends
// during Conn.Handshake(), with a configurable CPU affinity value, followed
// by sentinelAfterHandshake. When affinity is non-zero, a companion
// affinity-mask uint64 must also be sent (see BasicServerConnection::
// postHandshake in Nix's own C++ source: it reads one uint64 flag and only
// reads a second uint64 if that flag is non-zero). Regression coverage for
// a bug where this repo's handshake unconditionally read exactly two
// uint64s here, desyncing the entire rest of the session whenever a client
// actually sets CPU affinity.
func buildClientHandshakeBytes(t *testing.T, affinity uint64) []byte {
	t.Helper()
	var buf bytes.Buffer

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("building test bytes: %v", err)
		}
	}

	must(WriteUint64(&buf, ClientMagic))
	must(WriteUint64(&buf, ProtocolVersion))
	must(WriteStrings(&buf, []string{})) // client features (empty)

	must(WriteUint64(&buf, affinity))
	if affinity != 0 {
		must(WriteUint64(&buf, 0xFF)) // affinity mask
	}
	must(WriteUint64(&buf, 0)) // reserveSpace
	must(WriteUint64(&buf, sentinelAfterHandshake))

	return buf.Bytes()
}

func TestHandshakeZeroCPUAffinity(t *testing.T) {
	input := bytes.NewReader(buildClientHandshakeBytes(t, 0))
	var output bytes.Buffer
	conn := NewConn(input, &output)

	if err := conn.Handshake(); err != nil {
		t.Fatalf("Handshake() with affinity=0 failed: %v", err)
	}

	next, err := ReadUint64(conn.Reader())
	if err != nil {
		t.Fatalf("reading sentinel after handshake: %v", err)
	}
	if next != sentinelAfterHandshake {
		t.Fatalf("got %#x after handshake, want sentinel %#x - handshake consumed the wrong number of bytes", next, sentinelAfterHandshake)
	}
}

// TestHandshakeNonZeroCPUAffinity is the regression test: a client that
// sends a non-zero CPU affinity flag also sends a companion affinity-mask
// uint64 before reserveSpace. The handshake must consume it, not
// misinterpret it as reserveSpace and desync every subsequent read.
func TestHandshakeNonZeroCPUAffinity(t *testing.T) {
	input := bytes.NewReader(buildClientHandshakeBytes(t, 1))
	var output bytes.Buffer
	conn := NewConn(input, &output)

	if err := conn.Handshake(); err != nil {
		t.Fatalf("Handshake() with non-zero affinity failed: %v", err)
	}

	next, err := ReadUint64(conn.Reader())
	if err != nil {
		t.Fatalf("reading sentinel after handshake: %v", err)
	}
	if next != sentinelAfterHandshake {
		t.Fatalf("got %#x after handshake, want sentinel %#x - affinity mask was not consumed, desyncing the rest of the session", next, sentinelAfterHandshake)
	}
}
