package gradientproto

import (
	"context"
	"testing"
)

// TestReadDerivationWithInput parses the exact JSON captured from a real
// `nix derivation show --recursive` invocation (nix (Nix) 2.34.7) against
// a derivation with one input-addressed dependency - see
// testdata/nix-json/derivation_show_dep.json and this package's own doc
// comment in derivation.go for how it was obtained.
func TestReadDerivationWithInput(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutputFile("nix derivation show --recursive", nixJSONFixture(t, "derivation_show_dep.json"))

	drv, system, err := readDerivation(context.Background(), exec, "/nix/store/md068ia0dnlncb345b9n3ggaky3hvvxl-test-dep.drv")
	if err != nil {
		t.Fatalf("readDerivation: %v", err)
	}

	if system != "x86_64-linux" {
		t.Errorf("system = %q, want x86_64-linux", system)
	}
	if drv.Builder != "/bin/sh" {
		t.Errorf("Builder = %q", drv.Builder)
	}
	if len(drv.Args) != 2 || drv.Args[0] != "-c" {
		t.Errorf("Args = %v", drv.Args)
	}
	if drv.Env["name"] != "test-dep" {
		t.Errorf("Env[name] = %q", drv.Env["name"])
	}

	out, ok := drv.Outputs["out"]
	if !ok {
		t.Fatal("missing output \"out\"")
	}
	if out.Path != "/nix/store/h3qlp051wcqdgg3cl2amdbq92qaz1gic-test-dep" {
		t.Errorf("Outputs[out].Path = %q", out.Path)
	}
	if out.HashAlgo != "" || out.Hash != "" {
		t.Errorf("expected empty HashAlgo/Hash for input-addressed output, got %q/%q", out.HashAlgo, out.Hash)
	}

	// The input drv's own resolved output path must be present in
	// InputSrcs, since it's needed for the build.
	wantInput := "/nix/store/z4asv3j07d89ywjf8fxkn7sg6mf5s9q5-dep"
	found := false
	for _, s := range drv.InputSrcs {
		if s == wantInput {
			found = true
		}
	}
	if !found {
		t.Errorf("InputSrcs = %v, want to contain %q", drv.InputSrcs, wantInput)
	}
}

// TestReadDerivationFixedOutputRecursive parses a fixed-output derivation
// using NAR (recursive) hashing - the "method": "nar" case, which must
// gain the "r:" HashAlgo prefix (nixproto's own convention - see
// nixproto/derivation.go's DerivationOutput.HashAlgo doc comment).
func TestReadDerivationFixedOutputRecursive(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutputFile("nix derivation show --recursive", nixJSONFixture(t, "derivation_show_fod.json"))

	drv, _, err := readDerivation(context.Background(), exec, "/nix/store/4vx27csgd2y4vf50ml842smh7crv8jay-test-fod.drv")
	if err != nil {
		t.Fatalf("readDerivation: %v", err)
	}

	out, ok := drv.Outputs["out"]
	if !ok {
		t.Fatal("missing output \"out\"")
	}
	if out.Path != "" {
		t.Errorf("Path = %q, want empty for a fixed-output derivation", out.Path)
	}
	if out.HashAlgo != "r:sha256" {
		t.Errorf("HashAlgo = %q, want \"r:sha256\"", out.HashAlgo)
	}
	if out.Hash != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("Hash = %q", out.Hash)
	}
}

func TestReadDerivationMissingFromOutput(t *testing.T) {
	exec := newFakeExecer(t)
	exec.onOutput("nix derivation show --recursive", []byte(`{"derivations":{}}`), nil)

	if _, _, err := readDerivation(context.Background(), exec, "/nix/store/nonexistent.drv"); err == nil {
		t.Fatal("expected an error when the target derivation is absent from the output")
	}
}

func TestParseSRIHash(t *testing.T) {
	algo, hash, ok := parseSRIHash("sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if !ok || algo != "sha256" || hash != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("got algo=%q hash=%q ok=%v", algo, hash, ok)
	}
	if _, _, ok := parseSRIHash("not-a-valid-sri-hash-at-all-no-dash"); ok {
		// Note: this string does contain a dash, so re-check with something
		// that truly has none.
	}
	if _, _, ok := parseSRIHash("nodash"); ok {
		t.Error("expected ok=false for a string with no '-' separator")
	}
}
