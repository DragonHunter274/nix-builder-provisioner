package gradientproto

import (
	"fmt"

	"nix-builder-provisioner/gradientproto/wire"
)

// DerivationOutput mirrors gradient_types::proto::DerivationOutput.
type DerivationOutput struct {
	Name string
	Path string
}

const derivationOutputSize = 2 * wire.StringSize // two Strings, align 4

func decodeDerivationOutput(d *wire.Decoder, pos int) DerivationOutput {
	return DerivationOutput{
		Name: d.String(pos + 0),
		Path: d.String(pos + wire.StringSize),
	}
}

// BuildTask mirrors gradient_types::proto::BuildTask. Field order and
// types (and therefore offsets, computed via wire.Layout) must match
// upstream exactly - see testdata/gen-fixtures/src/types.rs.
type BuildTask struct {
	BuildID        string
	DrvPath        string
	ExternalCached bool
	IsFixedOutput  bool
	Outputs        []DerivationOutput
	TimeoutSecs    *uint64
	MaxSilentSecs  *uint64
}

var buildTaskOff struct {
	BuildID, DrvPath, ExternalCached, IsFixedOutput, Outputs, TimeoutSecs, MaxSilentSecs int
	Align, Size                                                                          int
}

func init() {
	l := &wire.Layout{}
	buildTaskOff.BuildID = l.Field(wire.StringAlign, wire.StringSize)
	buildTaskOff.DrvPath = l.Field(wire.StringAlign, wire.StringSize)
	buildTaskOff.ExternalCached = l.Field(1, 1)
	buildTaskOff.IsFixedOutput = l.Field(1, 1)
	buildTaskOff.Outputs = l.Field(wire.VecAlign, wire.VecSize)
	buildTaskOff.TimeoutSecs = l.Field(8, wire.OptionSize(8, 8))
	buildTaskOff.MaxSilentSecs = l.Field(8, wire.OptionSize(8, 8))
	buildTaskOff.Align = l.Align()
	buildTaskOff.Size = l.Size()
	if buildTaskOff.Size != 64 {
		panic(fmt.Sprintf("gradientproto: BuildTask layout size = %d, want 64 (see testdata/gen-fixtures fixture BuildTask_empty_strings_none)", buildTaskOff.Size))
	}
}

func decodeBuildTask(d *wire.Decoder, pos int) BuildTask {
	t := BuildTask{
		BuildID:        d.String(pos + buildTaskOff.BuildID),
		DrvPath:        d.String(pos + buildTaskOff.DrvPath),
		ExternalCached: d.Bool(pos + buildTaskOff.ExternalCached),
		IsFixedOutput:  d.Bool(pos + buildTaskOff.IsFixedOutput),
	}
	outputsPos := pos + buildTaskOff.Outputs
	t.Outputs = make([]DerivationOutput, 0, d.VecLen(outputsPos))
	d.VecEach(outputsPos, derivationOutputSize, func(elemPos, index int) {
		t.Outputs = append(t.Outputs, decodeDerivationOutput(d, elemPos))
	})
	if optPos := pos + buildTaskOff.TimeoutSecs; d.OptionIsSome(optPos) {
		v := d.U64(wire.OptionValPos(optPos, 8))
		t.TimeoutSecs = &v
	}
	if optPos := pos + buildTaskOff.MaxSilentSecs; d.OptionIsSome(optPos) {
		v := d.U64(wire.OptionValPos(optPos, 8))
		t.MaxSilentSecs = &v
	}
	return t
}

// BuildJob mirrors gradient_types::proto::BuildJob.
type BuildJob struct {
	Builds []BuildTask
}

const buildJobSize = wire.VecSize // one Vec field, align 4

func decodeBuildJob(d *wire.Decoder, pos int) BuildJob {
	j := BuildJob{Builds: make([]BuildTask, 0, d.VecLen(pos))}
	d.VecEach(pos, buildTaskOff.Size, func(elemPos, index int) {
		j.Builds = append(j.Builds, decodeBuildTask(d, elemPos))
	})
	return j
}

// jobSize is Job's measured total fixed size (tag + union of Flake/Build
// variants) - see testdata/gen-fixtures/README.md. Job::Flake is the larger
// variant; we never need its layout since we only ever decode Job::Build
// (or reject non-Build jobs outright), but the total slot size still
// governs where sibling fields sit in an enclosing struct (ServerMessage's
// ClientMessage union), so it must be measured, not guessed.
const jobSize = 96

// jobTagFlake and jobTagBuild are Job's 0-indexed variant declaration
// order: `enum Job { Flake(FlakeJob), Build(BuildJob) }`.
const (
	jobTagFlake uint32 = 0
	jobTagBuild uint32 = 1
)

// DecodeJob decodes the Job at pos, returning an error if it's not a
// Job::Build - this worker only implements the build capability, so a
// Job::Flake here is either a server misconfiguration (assigning eval work
// to a build-only worker) or a bug; the caller should reject the
// assignment rather than attempt to interpret Flake's fields, which this
// package does not model.
func DecodeJob(d *wire.Decoder, pos int) (BuildJob, error) {
	switch tag := uint32(d.Tag1(pos)); tag {
	case jobTagBuild:
		return decodeBuildJob(d, pos+4), nil
	case jobTagFlake:
		return BuildJob{}, fmt.Errorf("gradientproto: received Job::Flake, but this worker only implements the build capability")
	default:
		return BuildJob{}, fmt.Errorf("gradientproto: unknown Job variant tag %d", tag)
	}
}
