package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// jobUpdateKindSize is JobUpdateKind's measured total fixed size (tag +
// union of all 11 variants) - see testdata/gen-fixtures/README.md. Only
// Building and BuildOutput are implemented (the build capability doesn't
// use the eval/flake variants); the union size is still governed by
// whichever variant is largest across the full type, so it's measured, not
// derived from just the two we use.
const jobUpdateKindSize = 144

// 0-indexed declaration order from
// `enum JobUpdateKind { Fetching, FetchResult{..}, EvaluatingFlake,
// EvaluatingDerivations, EvalResult{..}, Building{..}, BuildOutput{..},
// Compressing, EvalStats(..), InputUpdateResult{..}, InputUpdateExpansion{..} }`.
const (
	jobUpdateTagBuilding    uint32 = 5
	jobUpdateTagBuildOutput uint32 = 6
)

var jobUpdateBuildingOff struct {
	BuildID int
}

var jobUpdateBuildOutputOff struct {
	BuildID, Outputs, Metrics, Substituted int
}

func init() {
	// TagField() reserves the enum discriminant's own 4-byte slot as field
	// 0 before any variant field, so alignment padding for a field wider
	// than 4 bytes (Metrics, containing u64s, needs 8-byte alignment) is
	// computed correctly - see PrepareTaggedUnion's and TagField's doc
	// comments in wire/arena.go. This bit me: without it, Metrics landed 4
	// bytes early, and every field read after it (Substituted) came out
	// wrong despite validating as "structurally fine" to both bytecheck
	// and this package's own decoder, since both were consistently wrong
	// in the same way - only cross-checking against a real rkyv-decoded
	// value with distinguishable field values caught it.
	lb := wire.TagField()
	jobUpdateBuildingOff.BuildID = lb.Field(wire.StringAlign, wire.StringSize)

	lo := wire.TagField()
	jobUpdateBuildOutputOff.BuildID = lo.Field(wire.StringAlign, wire.StringSize)
	jobUpdateBuildOutputOff.Outputs = lo.Field(wire.VecAlign, wire.VecSize)
	jobUpdateBuildOutputOff.Metrics = lo.Field(buildMetricsOff.Align, wire.OptionSize(buildMetricsOff.Align, buildMetricsOff.Size))
	jobUpdateBuildOutputOff.Substituted = lo.Field(1, 1)
}

// prepareJobUpdateBuilding returns a FieldWriter for
// JobUpdateKind::Building{build_id}.
func prepareJobUpdateBuilding(a *wire.Arena, buildID string) wire.FieldWriter {
	fields := wire.PrepareStruct(
		wire.Field{Offset: jobUpdateBuildingOff.BuildID, Write: wire.PrepareString(a, buildID)},
	)
	return wire.PrepareTaggedUnion(a, jobUpdateTagBuilding, fields)
}

// prepareJobUpdateBuildOutput returns a FieldWriter for
// JobUpdateKind::BuildOutput{build_id, outputs, metrics, substituted}.
// Fields are sequenced in strict declaration order - see
// prepareBuildOutput's doc comment (build_output.go) for why.
func prepareJobUpdateBuildOutput(a *wire.Arena, buildID string, outputs []BuildOutput, metrics *BuildMetrics, substituted bool) wire.FieldWriter {
	buildIDField := wire.PrepareString(a, buildID)

	outputsField := wire.PrepareVec(a, len(outputs), buildOutputOff.Align, buildOutputOff.Size, func(index int) wire.FieldWriter {
		return prepareBuildOutput(a, outputs[index])
	})

	metricsField := wire.PrepareOptionNone()
	if metrics != nil {
		metricsField = wire.PrepareOptionSome(a, buildMetricsOff.Align, prepareBuildMetrics(a, *metrics))
	}

	substitutedField := wire.PrepareBool(a, substituted)

	fields := wire.PrepareStruct(
		wire.Field{Offset: jobUpdateBuildOutputOff.BuildID, Write: buildIDField},
		wire.Field{Offset: jobUpdateBuildOutputOff.Outputs, Write: outputsField},
		wire.Field{Offset: jobUpdateBuildOutputOff.Metrics, Write: metricsField},
		wire.Field{Offset: jobUpdateBuildOutputOff.Substituted, Write: substitutedField},
	)
	return wire.PrepareTaggedUnion(a, jobUpdateTagBuildOutput, fields)
}
