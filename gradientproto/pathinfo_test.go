package gradientproto

import (
	"context"
	"testing"
)

func TestQueryPathInfoSimple(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutputFile("nix path-info --json", nixJSONFixture(t, "path_info_simple.json"))

	info, err := queryPathInfo(context.Background(), exec, "/nix/store/ifbychscpzma0mx3x89r2jsg7ak0iwri-test-drv")
	if err != nil {
		t.Fatalf("queryPathInfo: %v", err)
	}
	if info.NarHash != "sha256-EFUdrtf6Rn0LWIJufrmg8q99aT3jGfLvd1//zaJEufY=" {
		t.Errorf("NarHash = %q", info.NarHash)
	}
	if info.NarSize != 120 {
		t.Errorf("NarSize = %d, want 120", info.NarSize)
	}
	if info.CA != nil {
		t.Errorf("CA = %v, want nil", info.CA)
	}
	if len(info.References) != 0 {
		t.Errorf("References = %v, want empty", info.References)
	}
	if info.Deriver == nil || *info.Deriver != "/nix/store/nasavqjd54603j6n6in52kwiaz57lb16-test-drv.drv" {
		t.Errorf("Deriver = %v", info.Deriver)
	}
}

func TestQueryPathInfoWithReferences(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutputFile("nix path-info --json", nixJSONFixture(t, "path_info_with_refs.json"))

	info, err := queryPathInfo(context.Background(), exec, "/nix/store/000mr3jfk81q751lljdskn2mk6g76rnr-libva-2.22.0")
	if err != nil {
		t.Fatalf("queryPathInfo: %v", err)
	}
	if len(info.References) != 9 {
		t.Fatalf("References len = %d, want 9", len(info.References))
	}
	// References must be stripped of the /nix/store/ prefix - see
	// EncodeNarUploaded's doc.
	for _, r := range info.References {
		if len(r) > 0 && r[0] == '/' {
			t.Errorf("reference %q still has a leading slash, want bare basename", r)
		}
	}
	want := "000mr3jfk81q751lljdskn2mk6g76rnr-libva-2.22.0"
	found := false
	for _, r := range info.References {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Errorf("References = %v, want to contain self-reference %q", info.References, want)
	}
}

func TestQueryPathInfoNoDeriver(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutputFile("nix path-info --json", nixJSONFixture(t, "path_info_no_deriver.json"))

	info, err := queryPathInfo(context.Background(), exec, "/nix/store/hbzs9rv5mnr0x2iy5d71m329v1mc6z36-plainfile.txt")
	if err != nil {
		t.Fatalf("queryPathInfo: %v", err)
	}
	if info.Deriver != nil {
		t.Errorf("Deriver = %v, want nil", info.Deriver)
	}
	if info.CA == nil || *info.CA != "fixed:r:sha256:04zwf782yjwnh3q6hz5izfd6jyip8kgw6g6yj43fiqhbyhdd0dqw" {
		t.Errorf("CA = %v", info.CA)
	}
}

func TestQueryPathInfoMissingPath(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutput("nix path-info --json", []byte(`{"/nix/store/missing":null}`), nil)

	if _, err := queryPathInfo(context.Background(), exec, "/nix/store/missing"); err == nil {
		t.Fatal("expected an error for a missing (null) path-info entry")
	}
}
