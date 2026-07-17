package gradientproto

import (
	"testing"

	"nix-builder-provisioner/gradientproto/wire"
)

func TestPrepareJobUpdateKind(t *testing.T) {
	t.Run("Building", func(t *testing.T) {
		// Single-field variant: no sibling out-of-line ordering ambiguity,
		// so byte-exact comparison against the real fixture is meaningful.
		fixture := loadFixture(t, "JobUpdateKind_Building")
		a := wire.NewArena()
		w := prepareJobUpdateBuilding(a, "b-1")
		p := a.Reserve(jobUpdateKindAlign, jobUpdateKindSize)
		w(p)
		got := a.Bytes()[p : p+jobUpdateKindSize]
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})

	// BuildOutput has several sibling out-of-line fields (strings, a vec,
	// an Option<BuildMetrics>); our encoder doesn't guarantee byte-for-byte
	// identical output to rkyv's own writer for those (see wire package
	// doc - only correct offsets matter, not physical ordering), so these
	// cases check decode-correctness via our own decoder at known field
	// offsets instead of raw byte comparison. Cross-validation against the
	// real rkyv bytecheck validator (not just our own decoder) happened
	// separately via testdata/gen-fixtures's verify binary - see
	// testdata/gen-fixtures/README.md.
	t.Run("BuildOutput no metrics", func(t *testing.T) {
		output := BuildOutput{
			Name:      "out",
			StorePath: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12",
			Hash:      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			NarSize:   ptrI64(123456),
			NarHash:   ptrStr("sha256:abc"),
		}
		a := wire.NewArena()
		w := prepareJobUpdateBuildOutput(a, "b-1", []BuildOutput{output}, nil, false)
		p := a.Reserve(jobUpdateKindAlign, jobUpdateKindSize)
		w(p)

		d := wire.NewDecoder(a.Bytes())
		if got := uint32(d.Tag1(p)); got != jobUpdateTagBuildOutput {
			t.Fatalf("tag = %d, want %d", got, jobUpdateTagBuildOutput)
		}
		if got := d.String(p + jobUpdateBuildOutputOff.BuildID); got != "b-1" {
			t.Errorf("build_id = %q, want %q", got, "b-1")
		}
		outputsPos := p + jobUpdateBuildOutputOff.Outputs
		if got := d.VecLen(outputsPos); got != 1 {
			t.Fatalf("outputs len = %d, want 1", got)
		}
		d.VecEach(outputsPos, buildOutputOff.Size, func(elemPos, index int) {
			if got := d.String(elemPos + buildOutputOff.StorePath); got != output.StorePath {
				t.Errorf("outputs[0].store_path = %q, want %q", got, output.StorePath)
			}
		})
		if d.OptionIsSome(p + jobUpdateBuildOutputOff.Metrics) {
			t.Error("metrics = Some, want None")
		}
		if got := d.Bool(p + jobUpdateBuildOutputOff.Substituted); got != false {
			t.Errorf("substituted = %v, want false", got)
		}
	})

	t.Run("BuildOutput with metrics", func(t *testing.T) {
		output := BuildOutput{
			Name:      "out",
			StorePath: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12",
			Hash:      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			NarSize:   ptrI64(123456),
			NarHash:   ptrStr("sha256:abc"),
		}
		metrics := BuildMetrics{
			PeakRamMB:      ptrU64(512),
			CPUTimeMs:      ptrU64(9000),
			AvgCPUPct:      ptrF32(87.5),
			DiskReadBytes:  ptrU64(1024),
			DiskWriteBytes: ptrU64(2048),
			BuildTimeMs:    ptrU64(12345),
		}
		a := wire.NewArena()
		w := prepareJobUpdateBuildOutput(a, "b-1", []BuildOutput{output, output}, &metrics, true)
		p := a.Reserve(jobUpdateKindAlign, jobUpdateKindSize)
		w(p)

		d := wire.NewDecoder(a.Bytes())
		outputsPos := p + jobUpdateBuildOutputOff.Outputs
		if got := d.VecLen(outputsPos); got != 2 {
			t.Fatalf("outputs len = %d, want 2", got)
		}
		metricsPos := p + jobUpdateBuildOutputOff.Metrics
		if !d.OptionIsSome(metricsPos) {
			t.Fatal("metrics = None, want Some")
		}
		valPos := wire.OptionValPos(metricsPos, buildMetricsOff.Align)
		if got := d.U64(wire.OptionValPos(valPos+buildMetricsOff.PeakRamMB, 8)); got != 512 {
			t.Errorf("metrics.peak_ram_mb = %d, want 512", got)
		}
		if got := d.Bool(p + jobUpdateBuildOutputOff.Substituted); got != true {
			t.Errorf("substituted = %v, want true", got)
		}
	})
}

func ptrI64(v int64) *int64     { return &v }
func ptrU64(v uint64) *uint64   { return &v }
func ptrF32(v float32) *float32 { return &v }
func ptrStr(v string) *string   { return &v }
