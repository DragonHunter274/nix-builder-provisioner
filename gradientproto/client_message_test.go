package gradientproto

import (
	"testing"

	"nix-builder-provisioner/gradientproto/wire"
)

// These variants have at most one out-of-line field, so byte-exact
// comparison against the real fixtures is meaningful (see
// testdata/gen-fixtures/README.md on why multi-out-of-line-field ordering
// isn't otherwise guaranteed to match byte-for-byte).

func TestEncodeDraining(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_Draining")
	got := EncodeDraining()
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

func TestEncodeJobCompleted(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_JobCompleted")
	got := EncodeJobCompleted("j-1")
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

func TestEncodeRequestJob(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_RequestJob_Build")
	got := EncodeRequestJob(JobKindBuild)
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

func TestEncodeAssignJobResponse(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_AssignJobResponse_accept")
		got := EncodeAssignJobResponse("j-1", true, "")
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})
	t.Run("reject", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_AssignJobResponse_reject")
		got := EncodeAssignJobResponse("j-1", false, "no capacity")
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})
}

func TestEncodeJobFailed(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_JobFailed")
	got := EncodeJobFailed("j-1", "build failed", BuildFailurePermanent, nil)
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

// JobFailed has multiple sibling out-of-line fields (error string and
// missing_paths' element string), so - like the JobUpdateKind::BuildOutput
// cases in job_update_test.go - byte-exact comparison against the fixture
// isn't guaranteed (our encoder packs adjacent string content differently
// than rkyv's own writer, though both are valid; see wire package doc).
// Verified instead via our own decoder at known offsets, matching
// EncodeJobFailed's own TagField()-based layout, plus real bytecheck
// cross-validation performed manually during development (see
// testdata/gen-fixtures/README.md) - not repeated here since it requires
// the Rust toolchain this repo doesn't otherwise depend on.
func TestEncodeJobFailedInputsUnavailable(t *testing.T) {
	l := wire.TagField()
	jobIDOff := l.Field(wire.StringAlign, wire.StringSize)
	errorOff := l.Field(wire.StringAlign, wire.StringSize)
	kindOff := l.Field(1, 1)
	missingOff := l.Field(wire.VecAlign, wire.VecSize)

	got := EncodeJobFailed("j-1", "missing inputs", BuildFailureInputsUnavailable, []string{"/nix/store/iiii...-missing"})
	pos := len(got) - clientMessageSize
	d := wire.NewDecoder(got)

	if got := d.Tag1(pos); uint32(got) != clientTagJobFailed {
		t.Fatalf("tag = %d, want %d", got, clientTagJobFailed)
	}
	if got := d.String(pos + jobIDOff); got != "j-1" {
		t.Errorf("job_id = %q, want %q", got, "j-1")
	}
	if got := d.String(pos + errorOff); got != "missing inputs" {
		t.Errorf("error = %q, want %q", got, "missing inputs")
	}
	if got := d.Tag1(pos + kindOff); BuildFailureKind(got) != BuildFailureInputsUnavailable {
		t.Errorf("kind = %d, want %d", got, BuildFailureInputsUnavailable)
	}
	missingPos := pos + missingOff
	if n := d.VecLen(missingPos); n != 1 {
		t.Fatalf("missing_paths len = %d, want 1", n)
	}
	d.VecEach(missingPos, wire.StringSize, func(elemPos, index int) {
		if got := d.String(elemPos); got != "/nix/store/iiii...-missing" {
			t.Errorf("missing_paths[0] = %q", got)
		}
	})
}

func TestEncodeLogChunk(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_LogChunk")
		got := EncodeLogChunk("j-1", 0, []byte("building hello-2.12...\n"))
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})
	t.Run("empty", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_LogChunk_empty")
		got := EncodeLogChunk("j-1", 2, nil)
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})
}

func TestEncodeCacheQuery(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_CacheQuery")
	got := EncodeCacheQuery("j-1", "q-1", []string{"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12"}, QueryModePush)
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

func TestEncodeInitConnection(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_InitConnection")
	got := EncodeInitConnection(1, GradientCapabilities{Build: true}, "550e8400-e29b-41d4-a716-446655440000")
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}

func TestEncodeWorkerCapabilities(t *testing.T) {
	fixture := loadFixture(t, "ClientMessage_WorkerCapabilities")
	got := EncodeWorkerCapabilities(
		[]string{"x86_64-linux", "aarch64-linux"},
		[]string{"big-parallel"},
		5, 16, 65536, 1200,
	)
	if string(got) != string(fixture) {
		t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
	}
}
