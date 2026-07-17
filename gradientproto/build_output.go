package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// BuildProduct mirrors gradient_types::proto::BuildProduct. We always send
// an empty products list in v1 (hydra-build-products parsing is out of
// scope), but the type is modeled fully for completeness and forward
// compatibility.
type BuildProduct struct {
	FileType string
	Subtype  string
	Name     string
	Path     string
	Size     *uint64
}

var buildProductOff struct {
	FileType, Subtype, Name, Path, Size int
	Alloc                               struct{ Align, Size int }
}

func init() {
	l := &wire.Layout{}
	buildProductOff.FileType = l.Field(wire.StringAlign, wire.StringSize)
	buildProductOff.Subtype = l.Field(wire.StringAlign, wire.StringSize)
	buildProductOff.Name = l.Field(wire.StringAlign, wire.StringSize)
	buildProductOff.Path = l.Field(wire.StringAlign, wire.StringSize)
	buildProductOff.Size = l.Field(8, wire.OptionSize(8, 8))
	buildProductOff.Alloc.Align = l.Align()
	buildProductOff.Alloc.Size = l.Size()
}

// prepareBuildProduct sequences fields in declaration order - see
// prepareBuildOutput's doc comment for why this matters.
func prepareBuildProduct(a *wire.Arena, p BuildProduct) wire.FieldWriter {
	fileTypeField := wire.PrepareString(a, p.FileType)
	subtypeField := wire.PrepareString(a, p.Subtype)
	nameField := wire.PrepareString(a, p.Name)
	pathField := wire.PrepareString(a, p.Path)

	sizeField := wire.PrepareOptionNone()
	if p.Size != nil {
		sizeField = wire.PrepareOptionSome(a, 8, wire.PrepareU64(a, *p.Size))
	}

	return wire.PrepareStruct(
		wire.Field{Offset: buildProductOff.FileType, Write: fileTypeField},
		wire.Field{Offset: buildProductOff.Subtype, Write: subtypeField},
		wire.Field{Offset: buildProductOff.Name, Write: nameField},
		wire.Field{Offset: buildProductOff.Path, Write: pathField},
		wire.Field{Offset: buildProductOff.Size, Write: sizeField},
	)
}

// BuildOutput mirrors gradient_types::proto::BuildOutput: one output of a
// successfully-built derivation, reported via
// ClientMessage::JobUpdate{update: JobUpdateKind::BuildOutput{outputs}}.
type BuildOutput struct {
	Name      string
	StorePath string
	Hash      string
	NarSize   *int64
	NarHash   *string
	Products  []BuildProduct
}

var buildOutputOff struct {
	Name, StorePath, Hash, NarSize, NarHash, Products int
	Align, Size                                       int
}

func init() {
	l := &wire.Layout{}
	buildOutputOff.Name = l.Field(wire.StringAlign, wire.StringSize)
	buildOutputOff.StorePath = l.Field(wire.StringAlign, wire.StringSize)
	buildOutputOff.Hash = l.Field(wire.StringAlign, wire.StringSize)
	buildOutputOff.NarSize = l.Field(8, wire.OptionSize(8, 8))
	buildOutputOff.NarHash = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	buildOutputOff.Products = l.Field(wire.VecAlign, wire.VecSize)
	buildOutputOff.Align = l.Align()
	buildOutputOff.Size = l.Size()
}

// prepareBuildOutput prepares each field in strict struct-declaration
// order (name, store_path, hash, nar_size, nar_hash, products) via
// sequential statements, deliberately not as a single PrepareStruct(...)
// call with inline field expressions. Go evaluates a function call's
// arguments left to right, so inlining would usually get the order right
// too - but any field prepared as a hoisted variable *before* the call
// (as several were here, for the Option/Vec conditionals) silently writes
// its out-of-line arena content earlier than fields textually listed above
// it. That's not just a cosmetic difference from Rust's own layout: rkyv's
// bytecheck validator walks a struct's out-of-line fields in declaration
// order expecting each one's content to sit in a shrinking window relative
// to the one before it (see wire/vec.go's PrepareVec doc and
// testdata/gen-fixtures/README.md) - writing them out of order produces
// bytes a real Gradient server's decoder would reject as invalid, even
// though our own decoder reads them back fine. Every multi-field Prepare*
// function in this package must sequence field preparation this way.
func prepareBuildOutput(a *wire.Arena, o BuildOutput) wire.FieldWriter {
	nameField := wire.PrepareString(a, o.Name)
	storePathField := wire.PrepareString(a, o.StorePath)
	hashField := wire.PrepareString(a, o.Hash)

	narSizeField := wire.PrepareOptionNone()
	if o.NarSize != nil {
		narSizeField = wire.PrepareOptionSome(a, 8, wire.PrepareI64(a, *o.NarSize))
	}

	narHashField := wire.PrepareOptionNone()
	if o.NarHash != nil {
		narHashField = wire.PrepareOptionSome(a, wire.StringAlign, wire.PrepareString(a, *o.NarHash))
	}

	productsField := wire.PrepareVec(a, len(o.Products), buildProductOff.Alloc.Align, buildProductOff.Alloc.Size, func(index int) wire.FieldWriter {
		return prepareBuildProduct(a, o.Products[index])
	})

	return wire.PrepareStruct(
		wire.Field{Offset: buildOutputOff.Name, Write: nameField},
		wire.Field{Offset: buildOutputOff.StorePath, Write: storePathField},
		wire.Field{Offset: buildOutputOff.Hash, Write: hashField},
		wire.Field{Offset: buildOutputOff.NarSize, Write: narSizeField},
		wire.Field{Offset: buildOutputOff.NarHash, Write: narHashField},
		wire.Field{Offset: buildOutputOff.Products, Write: productsField},
	)
}

// BuildMetrics mirrors gradient_types::proto::BuildMetrics: per-build
// resource usage, carried inline on JobUpdateKind::BuildOutput.
type BuildMetrics struct {
	PeakRamMB       *uint64
	CPUTimeMs       *uint64
	AvgCPUPct       *float32
	DiskReadBytes   *uint64
	DiskWriteBytes  *uint64
	OOMKilled       bool
	BuildTimeMs     *uint64
	PeakNetworkMbps *float32
}

var buildMetricsOff struct {
	PeakRamMB, CPUTimeMs, AvgCPUPct, DiskReadBytes, DiskWriteBytes, OOMKilled, BuildTimeMs, PeakNetworkMbps int
	Align, Size                                                                                             int
}

func init() {
	l := &wire.Layout{}
	buildMetricsOff.PeakRamMB = l.Field(8, wire.OptionSize(8, 8))
	buildMetricsOff.CPUTimeMs = l.Field(8, wire.OptionSize(8, 8))
	buildMetricsOff.AvgCPUPct = l.Field(4, wire.OptionSize(4, 4))
	buildMetricsOff.DiskReadBytes = l.Field(8, wire.OptionSize(8, 8))
	buildMetricsOff.DiskWriteBytes = l.Field(8, wire.OptionSize(8, 8))
	buildMetricsOff.OOMKilled = l.Field(1, 1)
	buildMetricsOff.BuildTimeMs = l.Field(8, wire.OptionSize(8, 8))
	buildMetricsOff.PeakNetworkMbps = l.Field(4, wire.OptionSize(4, 4))
	buildMetricsOff.Align = l.Align()
	buildMetricsOff.Size = l.Size()
	if buildMetricsOff.Size != 104 {
		panic("gradientproto: BuildMetrics layout size mismatch, want 104 (see testdata fixture BuildMetrics_default)")
	}
}

func prepareOptU64(a *wire.Arena, v *uint64) wire.FieldWriter {
	if v == nil {
		return wire.PrepareOptionNone()
	}
	return wire.PrepareOptionSome(a, 8, wire.PrepareU64(a, *v))
}

func prepareOptF32(a *wire.Arena, v *float32) wire.FieldWriter {
	if v == nil {
		return wire.PrepareOptionNone()
	}
	return wire.PrepareOptionSome(a, 4, wire.PrepareF32(a, *v))
}

func prepareBuildMetrics(a *wire.Arena, m BuildMetrics) wire.FieldWriter {
	return wire.PrepareStruct(
		wire.Field{Offset: buildMetricsOff.PeakRamMB, Write: prepareOptU64(a, m.PeakRamMB)},
		wire.Field{Offset: buildMetricsOff.CPUTimeMs, Write: prepareOptU64(a, m.CPUTimeMs)},
		wire.Field{Offset: buildMetricsOff.AvgCPUPct, Write: prepareOptF32(a, m.AvgCPUPct)},
		wire.Field{Offset: buildMetricsOff.DiskReadBytes, Write: prepareOptU64(a, m.DiskReadBytes)},
		wire.Field{Offset: buildMetricsOff.DiskWriteBytes, Write: prepareOptU64(a, m.DiskWriteBytes)},
		wire.Field{Offset: buildMetricsOff.OOMKilled, Write: wire.PrepareBool(a, m.OOMKilled)},
		wire.Field{Offset: buildMetricsOff.BuildTimeMs, Write: prepareOptU64(a, m.BuildTimeMs)},
		wire.Field{Offset: buildMetricsOff.PeakNetworkMbps, Write: prepareOptF32(a, m.PeakNetworkMbps)},
	)
}
