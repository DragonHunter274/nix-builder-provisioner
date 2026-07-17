package gradientproto

import (
	"testing"

	"nix-builder-provisioner/gradientproto/wire"
)

func TestDecodeCachedPath(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		fixture := loadFixture(t, "CachedPath_minimal")
		d := wire.NewDecoder(fixture)
		pos := len(fixture) - cachedPathOff.Size
		cp := decodeCachedPath(d, pos)
		if cp.Path != "/nix/store/dddddddddddddddddddddddddddddddd-foo" {
			t.Errorf("Path = %q", cp.Path)
		}
		if cp.Cached {
			t.Error("Cached = true, want false")
		}
		if cp.FileSize != nil || cp.NarSize != nil || cp.URL != nil || cp.NarHash != nil ||
			cp.FileHash != nil || cp.References != nil || cp.Signatures != nil ||
			cp.Deriver != nil || cp.CA != nil {
			t.Errorf("expected all-nil optionals, got %+v", cp)
		}
	})

	t.Run("full", func(t *testing.T) {
		fixture := loadFixture(t, "CachedPath_full")
		d := wire.NewDecoder(fixture)
		pos := len(fixture) - cachedPathOff.Size
		cp := decodeCachedPath(d, pos)
		if !cp.Cached {
			t.Error("Cached = false, want true")
		}
		if cp.FileSize == nil || *cp.FileSize != 1000 {
			t.Errorf("FileSize = %v, want 1000", cp.FileSize)
		}
		if cp.NarSize == nil || *cp.NarSize != 2000 {
			t.Errorf("NarSize = %v, want 2000", cp.NarSize)
		}
		if cp.URL == nil || *cp.URL != "https://s3.example.com/presigned?sig=abc" {
			t.Errorf("URL = %v", cp.URL)
		}
		if cp.NarHash == nil || *cp.NarHash != "sha256:aaa" {
			t.Errorf("NarHash = %v", cp.NarHash)
		}
		if cp.FileHash == nil || *cp.FileHash != "sha256:bbb" {
			t.Errorf("FileHash = %v", cp.FileHash)
		}
		if cp.References == nil || len(*cp.References) != 1 || (*cp.References)[0] != "/nix/store/eeee...-bar" {
			t.Errorf("References = %v", cp.References)
		}
		if cp.Signatures == nil || len(*cp.Signatures) != 1 || (*cp.Signatures)[0] != "cache.nixos.org-1:abcd==" {
			t.Errorf("Signatures = %v", cp.Signatures)
		}
		if cp.Deriver == nil || *cp.Deriver != "/nix/store/ffff...-foo.drv" {
			t.Errorf("Deriver = %v", cp.Deriver)
		}
		if cp.CA == nil || *cp.CA != "fixed:r:sha256:ccc" {
			t.Errorf("CA = %v", cp.CA)
		}
	})
}
