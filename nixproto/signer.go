package nixproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// StoreSigner holds an Ed25519 keypair used to sign Nix store paths.
// The public key must be added to the store host's nix.conf trusted-public-keys.
type StoreSigner struct {
	keyName    string
	privateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

// PublicKeyString returns the public key in Nix format: "<name>:<base64(pubkey)>"
func (s *StoreSigner) PublicKeyString() string {
	return s.keyName + ":" + base64.StdEncoding.EncodeToString(s.PublicKey)
}

// SignPath computes the Nix fingerprint for a store path and signs it.
// narHash must be in sha256:<nix32> or sha256-<base64> format.
// Returns a signature string in "<name>:<base64(sig)>" format.
func (s *StoreSigner) SignPath(path, narHash string, narSize uint64, refs []string) (string, error) {
	normalizedHash, err := normalizeNarHashToNix32(narHash)
	if err != nil {
		return "", fmt.Errorf("normalizing narHash %q: %w", narHash, err)
	}
	fp := makeFingerprint(path, normalizedHash, narSize, refs)
	sig := ed25519.Sign(s.privateKey, []byte(fp))
	return s.keyName + ":" + base64.StdEncoding.EncodeToString(sig), nil
}

// makeFingerprint builds the canonical fingerprint string that Nix uses for signatures.
// Format: "1;<storepath>;<narHash>;<narSize>;<comma-separated-sorted-refs>"
func makeFingerprint(path, narHash string, narSize uint64, refs []string) string {
	sorted := make([]string, len(refs))
	copy(sorted, refs)
	sort.Strings(sorted)
	return "1;" + path + ";" + narHash + ";" + strconv.FormatUint(narSize, 10) + ";" + strings.Join(sorted, ",")
}

// normalizeNarHashToNix32 converts a NAR hash to "sha256:<nix32>" format.
// The nix-daemon wire protocol always sends narHash in this format, but
// nix path-info --json may emit SRI "sha256-<base64>" on newer Nix versions.
func normalizeNarHashToNix32(narHash string) (string, error) {
	if strings.HasPrefix(narHash, "sha256:") {
		rest := narHash[7:]
		if len(rest) == 52 {
			return narHash, nil
		}
		// Possibly base64 after the colon (uncommon)
		decoded, err := base64.StdEncoding.DecodeString(rest)
		if err == nil && len(decoded) == 32 {
			return "sha256:" + bytesToNix32(decoded), nil
		}
		return "", fmt.Errorf("unrecognized sha256: hash length %d", len(rest))
	}
	if strings.HasPrefix(narHash, "sha256-") {
		// SRI format: add standard padding if missing
		b64 := narHash[7:]
		switch len(b64) % 4 {
		case 2:
			b64 += "=="
		case 3:
			b64 += "="
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("decoding SRI narHash: %w", err)
		}
		if len(decoded) != 32 {
			return "", fmt.Errorf("unexpected SHA256 length: %d bytes", len(decoded))
		}
		return "sha256:" + bytesToNix32(decoded), nil
	}
	return "", fmt.Errorf("unsupported narHash format: %q", narHash)
}

// bytesToNix32 encodes a byte slice using Nix's base32 alphabet.
// The algorithm matches Nix's Hash::to_string_base32 in src/libutil/hash.cc.
func bytesToNix32(data []byte) string {
	outLen := (len(data)*8 + 4) / 5
	result := make([]byte, outLen)

	for n := outLen - 1; n >= 0; n-- {
		b := n * 5
		i := b / 8
		j := uint(b % 8)

		var c byte
		if i < len(data) {
			c = data[i] >> j
		}
		if i+1 < len(data) {
			c |= data[i+1] << (8 - j)
		}
		result[outLen-1-n] = nix32Alphabet[c&0x1f]
	}

	return string(result)
}

// GenerateOrLoadStoreSigner loads an existing Nix Ed25519 signing key from keyFile,
// or generates a new one if the file does not exist.
// The file format is Nix's binary cache key format: "<name>:<base64(privkey)>"
func GenerateOrLoadStoreSigner(keyFile, keyName string) (*StoreSigner, error) {
	if data, err := os.ReadFile(keyFile); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
		if len(parts) == 2 {
			decoded, err := base64.StdEncoding.DecodeString(parts[1])
			if err == nil && len(decoded) == ed25519.PrivateKeySize {
				priv := ed25519.PrivateKey(decoded)
				return &StoreSigner{
					keyName:    parts[0],
					privateKey: priv,
					PublicKey:  priv.Public().(ed25519.PublicKey),
				}, nil
			}
		}
		return nil, fmt.Errorf("invalid key format in %s", keyFile)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating Ed25519 key: %w", err)
	}

	content := keyName + ":" + base64.StdEncoding.EncodeToString(priv) + "\n"
	if err := os.WriteFile(keyFile, []byte(content), 0600); err != nil {
		return nil, fmt.Errorf("saving signing key to %s: %w", keyFile, err)
	}

	return &StoreSigner{
		keyName:    keyName,
		privateKey: priv,
		PublicKey:  pub,
	}, nil
}
