package gradientproto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPeersFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peers")
	content := "# comment\n\npeer-1:token-1\n*:wildcard-token\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := LoadPeersFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if creds["peer-1"] != "token-1" {
		t.Errorf("peer-1 token = %q, want %q", creds["peer-1"], "token-1")
	}
	if creds["*"] != "wildcard-token" {
		t.Errorf("wildcard token = %q, want %q", creds["*"], "wildcard-token")
	}
}

func TestLoadPeersFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peers")
	if err := os.WriteFile(path, []byte("not-a-valid-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPeersFile(path); err == nil {
		t.Fatal("expected an error for a malformed line")
	}
}

func TestTokensFor(t *testing.T) {
	t.Run("exact match preferred over wildcard", func(t *testing.T) {
		creds := PeerCredentials{"peer-1": "exact-token", "*": "wildcard-token"}
		tokens := creds.TokensFor([]string{"peer-1"})
		if len(tokens) != 1 || tokens[0].Token != "exact-token" {
			t.Errorf("got %v", tokens)
		}
	})

	t.Run("falls back to wildcard", func(t *testing.T) {
		creds := PeerCredentials{"*": "wildcard-token"}
		tokens := creds.TokensFor([]string{"peer-2"})
		if len(tokens) != 1 || tokens[0].PeerID != "peer-2" || tokens[0].Token != "wildcard-token" {
			t.Errorf("got %v", tokens)
		}
	})

	t.Run("skips unmatched peers", func(t *testing.T) {
		creds := PeerCredentials{"peer-1": "token-1"}
		tokens := creds.TokensFor([]string{"peer-1", "peer-unknown"})
		if len(tokens) != 1 || tokens[0].PeerID != "peer-1" {
			t.Errorf("got %v", tokens)
		}
	})

	t.Run("nil credentials (open/discoverable mode)", func(t *testing.T) {
		var creds PeerCredentials
		tokens := creds.TokensFor([]string{"peer-1"})
		if len(tokens) != 0 {
			t.Errorf("got %v, want empty", tokens)
		}
	})
}
