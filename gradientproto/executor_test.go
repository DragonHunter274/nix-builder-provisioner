package gradientproto

import (
	"testing"

	"nix-builder-provisioner/nixproto"
)

func TestResolveOutputPath(t *testing.T) {
	t.Run("input-addressed uses the known path directly", func(t *testing.T) {
		out := nixproto.DerivationOutput{Path: "/nix/store/aaaa-foo"}
		got, err := resolveOutputPath("out", out, &nixproto.BuildResult{})
		if err != nil {
			t.Fatalf("resolveOutputPath: %v", err)
		}
		if got != "/nix/store/aaaa-foo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("content-addressed resolves via BuiltOutputs suffix match", func(t *testing.T) {
		out := nixproto.DerivationOutput{HashAlgo: "r:sha256", Hash: "AAAA"}
		result := &nixproto.BuildResult{
			BuiltOutputs: map[string]nixproto.Realisation{
				"sha256:deadbeef!out": {OutPath: "/nix/store/bbbb-foo"},
			},
		}
		got, err := resolveOutputPath("out", out, result)
		if err != nil {
			t.Fatalf("resolveOutputPath: %v", err)
		}
		if got != "/nix/store/bbbb-foo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("content-addressed with no matching realisation errors", func(t *testing.T) {
		out := nixproto.DerivationOutput{HashAlgo: "r:sha256", Hash: "AAAA"}
		result := &nixproto.BuildResult{BuiltOutputs: map[string]nixproto.Realisation{}}
		if _, err := resolveOutputPath("out", out, result); err == nil {
			t.Fatal("expected an error when no realisation matches")
		}
	})

	t.Run("multi-output derivation matches the correct suffix, not a prefix collision", func(t *testing.T) {
		// "out" must not match an entry for "devout" (a different output
		// name that happens to end with the same substring minus the "!").
		out := nixproto.DerivationOutput{HashAlgo: "r:sha256", Hash: "AAAA"}
		result := &nixproto.BuildResult{
			BuiltOutputs: map[string]nixproto.Realisation{
				"sha256:deadbeef!devout": {OutPath: "/nix/store/wrong"},
				"sha256:deadbeef!out":    {OutPath: "/nix/store/right"},
			},
		}
		got, err := resolveOutputPath("out", out, result)
		if err != nil {
			t.Fatalf("resolveOutputPath: %v", err)
		}
		if got != "/nix/store/right" {
			t.Errorf("got %q, want /nix/store/right", got)
		}
	})
}

func TestClassifyBuildFailure(t *testing.T) {
	cases := []struct {
		status nixproto.BuildResultStatus
		want   BuildFailureKind
	}{
		{nixproto.BuildResultTransientFailure, BuildFailureTransient},
		{nixproto.BuildResultCachedFailure, BuildFailureTransient},
		{nixproto.BuildResultTimedOut, BuildFailureTimeout},
		{nixproto.BuildResultPermanentFailure, BuildFailurePermanent},
		{nixproto.BuildResultInputRejected, BuildFailurePermanent},
		{nixproto.BuildResultMiscFailure, BuildFailurePermanent},
	}
	for _, c := range cases {
		if got := classifyBuildFailure(c.status); got != c.want {
			t.Errorf("classifyBuildFailure(%v) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestIsBuildSuccessStatus(t *testing.T) {
	success := []nixproto.BuildResultStatus{
		nixproto.BuildResultBuilt, nixproto.BuildResultSubstituted, nixproto.BuildResultAlreadyValid,
	}
	for _, s := range success {
		if !isBuildSuccessStatus(s) {
			t.Errorf("isBuildSuccessStatus(%v) = false, want true", s)
		}
	}
	failure := []nixproto.BuildResultStatus{
		nixproto.BuildResultPermanentFailure, nixproto.BuildResultTimedOut, nixproto.BuildResultTransientFailure,
	}
	for _, s := range failure {
		if isBuildSuccessStatus(s) {
			t.Errorf("isBuildSuccessStatus(%v) = true, want false", s)
		}
	}
}

func TestStorePathHash(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/nix/store/ifbychscpzma0mx3x89r2jsg7ak0iwri-test-drv", "ifbychscpzma0mx3x89r2jsg7ak0iwri"},
		{"/nix/store/z4asv3j07d89ywjf8fxkn7sg6mf5s9q5-dep", "z4asv3j07d89ywjf8fxkn7sg6mf5s9q5"},
		{"/nix/store/basenamewithnohyphen", "basenamewithnohyphen"},
	}
	for _, c := range cases {
		if got := storePathHash(c.path); got != c.want {
			t.Errorf("storePathHash(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
