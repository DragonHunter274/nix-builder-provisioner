package gradientproto

import (
	"os"
	"path/filepath"
	"testing"

	"nix-builder-provisioner/gradientproto/wire"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixtures", name+".bin"))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func TestDecodeJob(t *testing.T) {
	t.Run("Build empty", func(t *testing.T) {
		fixture := loadFixture(t, "Job_Build_empty")
		d := wire.NewDecoder(fixture)
		pos := len(fixture) - jobSize
		job, err := DecodeJob(d, pos)
		if err != nil {
			t.Fatalf("DecodeJob: %v", err)
		}
		if len(job.Builds) != 0 {
			t.Errorf("Builds = %v, want empty", job.Builds)
		}
	})

	t.Run("Build three", func(t *testing.T) {
		fixture := loadFixture(t, "Job_Build_three")
		d := wire.NewDecoder(fixture)
		pos := len(fixture) - jobSize
		job, err := DecodeJob(d, pos)
		if err != nil {
			t.Fatalf("DecodeJob: %v", err)
		}
		if len(job.Builds) != 3 {
			t.Fatalf("Builds = %d, want 3", len(job.Builds))
		}
		// builds[1] is the "populated" BuildTask (see
		// testdata/gen-fixtures/src/main.rs: build_task_long).
		bt := job.Builds[1]
		if bt.BuildID != "b-0000000000000000000000000000000000000001" {
			t.Errorf("BuildID = %q", bt.BuildID)
		}
		if bt.DrvPath != "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-hello-2.12.drv" {
			t.Errorf("DrvPath = %q", bt.DrvPath)
		}
		if !bt.ExternalCached || !bt.IsFixedOutput {
			t.Errorf("ExternalCached=%v IsFixedOutput=%v, want true,true", bt.ExternalCached, bt.IsFixedOutput)
		}
		if len(bt.Outputs) != 2 {
			t.Fatalf("Outputs = %d, want 2", len(bt.Outputs))
		}
		if bt.Outputs[0].Name != "out" || bt.Outputs[0].Path != "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12" {
			t.Errorf("Outputs[0] = %+v", bt.Outputs[0])
		}
		if bt.Outputs[1].Name != "dev" || bt.Outputs[1].Path != "/nix/store/cccccccccccccccccccccccccccccccc-hello-2.12-dev" {
			t.Errorf("Outputs[1] = %+v", bt.Outputs[1])
		}
		if bt.TimeoutSecs == nil || *bt.TimeoutSecs != 14400 {
			t.Errorf("TimeoutSecs = %v, want 14400", bt.TimeoutSecs)
		}
		if bt.MaxSilentSecs == nil || *bt.MaxSilentSecs != 3600 {
			t.Errorf("MaxSilentSecs = %v, want 3600", bt.MaxSilentSecs)
		}
		// builds[0] and builds[2] are the "empty" BuildTask.
		for _, i := range []int{0, 2} {
			empty := job.Builds[i]
			if empty.BuildID != "" || empty.DrvPath != "" {
				t.Errorf("Builds[%d] not empty: %+v", i, empty)
			}
			if empty.ExternalCached || empty.IsFixedOutput {
				t.Errorf("Builds[%d] flags not false: %+v", i, empty)
			}
			if len(empty.Outputs) != 0 {
				t.Errorf("Builds[%d].Outputs not empty: %v", i, empty.Outputs)
			}
			if empty.TimeoutSecs != nil || empty.MaxSilentSecs != nil {
				t.Errorf("Builds[%d] timeouts not nil: %v %v", i, empty.TimeoutSecs, empty.MaxSilentSecs)
			}
		}
	})

	t.Run("Flake rejected", func(t *testing.T) {
		fixture := loadFixture(t, "Job_Flake")
		d := wire.NewDecoder(fixture)
		pos := len(fixture) - jobSize
		if _, err := DecodeJob(d, pos); err == nil {
			t.Fatal("expected error decoding Job::Flake, got nil")
		}
	})
}
