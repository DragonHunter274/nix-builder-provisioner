package nixproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// buildAddMultipleToStoreRequest constructs the client-side bytes for an
// AddMultipleToStore op: repair flag, dontCheckSigs flag, a FramedSource
// with a couple of non-empty frames, and the terminating zero-length
// frame - followed by sentinelAfterHandshake, standing in for the next
// operation's opcode.
func buildAddMultipleToStoreRequest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("building test bytes: %v", err)
		}
	}

	must(WriteBool(&buf, false)) // repair
	must(WriteBool(&buf, false)) // dontCheckSigs

	// FramedSource: two data frames then the zero-length terminator.
	// Frame chunks are NOT padded (unlike strings).
	must(WriteUint64(&buf, 5))
	buf.WriteString("hello")
	must(WriteUint64(&buf, 3))
	buf.WriteString("abc")
	must(WriteUint64(&buf, 0)) // terminator

	must(WriteUint64(&buf, sentinelAfterHandshake))

	return buf.Bytes()
}

func generateTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrapping test key: %v", err)
	}
	return signer
}

// unreachableAddr returns a loopback address nothing is listening on, so
// ssh.Dial fails fast with connection-refused rather than hanging on a
// timeout.
func unreachableAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for unreachable addr: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// TestAddMultipleToStoreDrainsOnDialFailure is the regression test for the
// bug where a failure connecting to the Store Host (or any other failure
// before the framed source is consumed) left the client's already-in-flight
// FramedSource payload unread. The next conn.ReadOp() call would then read
// raw NAR bytes as if they were an opcode, corrupting the rest of the
// session - which is exactly the cascade of "Unknown(...)" operations seen
// in production after an AddMultipleToStore auth failure.
func TestAddMultipleToStoreDrainsOnDialFailure(t *testing.T) {
	input := bytes.NewReader(buildAddMultipleToStoreRequest(t))
	var output bytes.Buffer
	conn := NewConn(input, &output)

	proxy := NewProxy(ProxyConfig{
		StoreHostAddr: unreachableAddr(t),
		StoreHostUser: "nix-builder",
		StoreHostKey:  generateTestSigner(t),
	}, nil)

	if err := proxy.handleAddMultipleToStore(conn); err != nil {
		t.Fatalf("handleAddMultipleToStore returned an error (should report failure to the client instead): %v", err)
	}

	next, err := ReadUint64(conn.Reader())
	if err != nil {
		t.Fatalf("reading sentinel after handleAddMultipleToStore: %v", err)
	}
	if next != sentinelAfterHandshake {
		t.Fatalf("got %#x after handleAddMultipleToStore, want sentinel %#x - framed source was not drained, desyncing the rest of the session", next, sentinelAfterHandshake)
	}
}
