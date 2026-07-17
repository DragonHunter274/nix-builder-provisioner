package gradientproto

import (
	"testing"

	"nix-builder-provisioner/gradientproto/wire"
)

func TestPrepareBuildMetrics(t *testing.T) {
	u64 := func(v uint64) *uint64 { return &v }
	f32 := func(v float32) *float32 { return &v }

	t.Run("default (all None/false)", func(t *testing.T) {
		fixture := loadFixture(t, "BuildMetrics_default")
		a := wire.NewArena()
		w := prepareBuildMetrics(a, BuildMetrics{})
		p := a.Reserve(buildMetricsOff.Align, buildMetricsOff.Size)
		w(p)
		got := a.Bytes()[p : p+buildMetricsOff.Size]
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})

	t.Run("populated", func(t *testing.T) {
		// Matches testdata/gen-fixtures/src/main.rs's build_metrics value.
		fixture := loadFixture(t, "BuildMetrics_populated")
		m := BuildMetrics{
			PeakRamMB:       u64(512),
			CPUTimeMs:       u64(9000),
			AvgCPUPct:       f32(87.5),
			DiskReadBytes:   u64(1024),
			DiskWriteBytes:  u64(2048),
			OOMKilled:       false,
			BuildTimeMs:     u64(12345),
			PeakNetworkMbps: nil,
		}
		a := wire.NewArena()
		w := prepareBuildMetrics(a, m)
		p := a.Reserve(buildMetricsOff.Align, buildMetricsOff.Size)
		w(p)
		got := a.Bytes()[p : p+buildMetricsOff.Size]
		if string(got) != string(fixture) {
			t.Errorf("byte-exact mismatch:\ngot  % x\nwant % x", got, fixture)
		}
	})
}
