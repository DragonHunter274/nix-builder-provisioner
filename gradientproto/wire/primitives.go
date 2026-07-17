package wire

import (
	"encoding/binary"
	"math"
)

// All rkyv primitives on this wire are little-endian (no big_endian feature
// enabled in Gradient's Cargo.toml), matching the host's native byte order
// with no additional transformation.

func PutBool(a *Arena, pos int, v bool) {
	if v {
		a.WriteByteAt(pos, 1)
	} else {
		a.WriteByteAt(pos, 0)
	}
}

func PutU16(a *Arena, pos int, v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	a.CopyAt(pos, b[:])
}

func PutU32(a *Arena, pos int, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	a.CopyAt(pos, b[:])
}

func PutI32(a *Arena, pos int, v int32) {
	PutU32(a, pos, uint32(v))
}

func PutU64(a *Arena, pos int, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	a.CopyAt(pos, b[:])
}

func PutF32(a *Arena, pos int, v float32) {
	PutU32(a, pos, math.Float32bits(v))
}

func PutI64(a *Arena, pos int, v int64) {
	PutU64(a, pos, uint64(v))
}

// PutTag1 writes a fieldless-enum discriminant (JobKind, QueryMode,
// BuildFailureKind, CredentialKind, EvalMessageLevel, ...): a single byte
// equal to the variant's 0-indexed declaration order. Data-carrying enums
// (ClientMessage, ServerMessage, Job, JobUpdateKind) use a wider 4-byte tag
// instead - see EncodeRoot/PrepareTaggedUnion.
func PutTag1(a *Arena, pos int, v uint8) {
	a.WriteByteAt(pos, v)
}

// PrepareBool, PrepareU16, PrepareU32, PrepareU64, and PrepareF32 wrap the
// corresponding Put* function as a FieldWriter, for use as struct fields in
// PrepareStruct calls. Primitives need no arena writes at prepare time -
// there's no out-of-line content - so these are trivial closures.

func PrepareBool(a *Arena, v bool) FieldWriter {
	return func(pos int) { PutBool(a, pos, v) }
}

func PrepareU16(a *Arena, v uint16) FieldWriter {
	return func(pos int) { PutU16(a, pos, v) }
}

func PrepareU32(a *Arena, v uint32) FieldWriter {
	return func(pos int) { PutU32(a, pos, v) }
}

func PrepareU64(a *Arena, v uint64) FieldWriter {
	return func(pos int) { PutU64(a, pos, v) }
}

func PrepareF32(a *Arena, v float32) FieldWriter {
	return func(pos int) { PutF32(a, pos, v) }
}

func PrepareI64(a *Arena, v int64) FieldWriter {
	return func(pos int) { PutI64(a, pos, v) }
}

func PrepareTag1(a *Arena, v uint8) FieldWriter {
	return func(pos int) { PutTag1(a, pos, v) }
}

// Decoder reads an rkyv archive from a fixed byte slice. All reads are by
// absolute position; callers resolve relative offsets themselves (target =
// fieldPos + offset) since offset fields' meaning differs slightly between
// String (packed length + offset) and Vec (plain offset + length).
type Decoder struct {
	Buf []byte
}

func NewDecoder(buf []byte) *Decoder {
	return &Decoder{Buf: buf}
}

func (d *Decoder) Bool(pos int) bool {
	return d.Buf[pos] != 0
}

func (d *Decoder) U16(pos int) uint16 {
	return binary.LittleEndian.Uint16(d.Buf[pos : pos+2])
}

func (d *Decoder) U32(pos int) uint32 {
	return binary.LittleEndian.Uint32(d.Buf[pos : pos+4])
}

func (d *Decoder) I32(pos int) int32 {
	return int32(d.U32(pos))
}

func (d *Decoder) U64(pos int) uint64 {
	return binary.LittleEndian.Uint64(d.Buf[pos : pos+8])
}

func (d *Decoder) F32(pos int) float32 {
	return math.Float32frombits(d.U32(pos))
}

func (d *Decoder) I64(pos int) int64 {
	return int64(d.U64(pos))
}

// Tag1 reads an enum discriminant byte at pos: both fieldless enums
// (JobKind, QueryMode, ...) and enum-with-data variant tags (ClientMessage,
// ServerMessage, Job, JobUpdateKind, ...) use the same single-byte
// discriminant - see TagField's doc in arena.go for how that was
// discovered (a naively-4-byte-wide reader, e.g. U32, happens to read the
// right numeric value too, since the following padding bytes are always
// zero, but relying on that obscures where the field really starts on
// encode).
func (d *Decoder) Tag1(pos int) uint8 {
	return d.Buf[pos]
}
