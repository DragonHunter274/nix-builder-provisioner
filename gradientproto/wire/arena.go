// Package wire implements the rkyv 0.8 binary archive format used on
// Gradient's /proto WebSocket wire, reverse-engineered from rkyv-0.8.17's
// source (see gradientproto/testdata/gen-fixtures/README.md for the format
// notes and the golden fixtures this package is tested against).
//
// rkyv has no official cross-language spec: it is a zero-copy format tied to
// a specific Rust derive-macro-generated memory layout. This package
// reimplements just enough of it - primitives, strings (with small-string
// optimization), vecs, options, plain structs, and the fixed-size "C-union"
// layout rkyv uses for enums with data - to encode/decode Gradient's worker
// protocol messages without depending on Rust at runtime.
package wire

// Arena is an append-only byte builder used while encoding a message.
//
// Encoding uses a two-phase "prepare, then finalize" pattern captured by
// FieldWriter: preparing a value (PrepareString, PrepareVec, ...) writes any
// out-of-line content it owns (a long string's bytes, a vec's backing
// array) to the arena tail immediately and returns a closure that, once
// told the value's own final position, writes that value's fixed-size
// representation there (including offsets computed from the content
// positions captured when it was prepared).
//
// This ordering exists for one reason: rkyv's from_bytes requires the
// outermost message value (a ClientMessage or ServerMessage) to occupy the
// *last* bytes of the buffer. Preparing bottom-up before reserving the
// root's own slot guarantees that: every other value's content lands in
// the arena first, and the root is reserved (and thus positioned) last.
// Nested composite values (a struct field, a vec element) have no such
// constraint - the same two-phase pattern works for them uniformly whether
// they end up before or after their own children in the buffer, since
// rkyv's decoder is order-agnostic and only ever follows offsets.
type Arena struct {
	buf []byte
}

// FieldWriter writes a value's fixed-size representation at pos, once pos
// (the value's final position in the arena) is known. Any out-of-line
// content the value needed has already been appended to the arena by the
// time the FieldWriter is obtained - see the Prepare* functions.
type FieldWriter func(pos int)

// NewArena returns an empty Arena.
func NewArena() *Arena {
	return &Arena{}
}

// Bytes returns the underlying buffer. Valid only after all writes are
// complete: further Reserve calls may reallocate the backing array,
// invalidating any slice obtained from an earlier call to Bytes.
func (a *Arena) Bytes() []byte {
	return a.buf
}

// Reserve pads the arena to align bytes, appends size zero bytes, and
// returns the absolute position of the first of those bytes.
func (a *Arena) Reserve(align, size int) int {
	a.padTo(align)
	pos := len(a.buf)
	a.buf = append(a.buf, make([]byte, size)...)
	return pos
}

// padTo appends zero bytes until len(a.buf) is a multiple of align.
func (a *Arena) padTo(align int) {
	if align <= 1 {
		return
	}
	rem := len(a.buf) % align
	if rem == 0 {
		return
	}
	a.buf = append(a.buf, make([]byte, align-rem)...)
}

// CopyAt copies data into the arena at pos, which must already be reserved.
func (a *Arena) CopyAt(pos int, data []byte) {
	copy(a.buf[pos:pos+len(data)], data)
}

// WriteByteAt sets a single byte at an already-reserved position.
func (a *Arena) WriteByteAt(pos int, b byte) {
	a.buf[pos] = b
}

// Field pairs a FieldWriter with its byte offset within an enclosing
// struct's fixed-size slot.
type Field struct {
	Offset int
	Write  FieldWriter
}

// PrepareStruct combines already-prepared fields into a single FieldWriter
// for the enclosing struct: once given the struct's own position, it calls
// each field's writer at pos+field.Offset.
func PrepareStruct(fields ...Field) FieldWriter {
	return func(pos int) {
		for _, f := range fields {
			f.Write(pos + f.Offset)
		}
	}
}

// PrepareTaggedUnion returns a FieldWriter for a non-root enum-with-data
// value (e.g. a Job embedded as a struct field): writes the 1-byte variant
// tag at pos, then calls fields at pos - NOT pos+1 or pos+4. This is
// deliberate: the tag is field 0 of the variant's own Layout (see
// TagField), so a field after it may need more alignment padding than the
// tag's own single byte provides (e.g. a field containing a u64 needs
// 8-byte alignment, pushing it to offset 8), or none at all (a field
// needing only 1-byte alignment packs immediately at offset 1 - see
// TagField's doc for how this was discovered). `fields`'s offsets must
// already be computed by a Layout that reserved the tag itself first via
// TagField, so they're correct absolute-from-variant-start offsets -
// passing pos here (not an adjusted offset) is what makes that consistent.
// Unlike EncodeRoot, this does not need to defer reservation - a nested
// tagged union has no "must be last" constraint, it's simply written into
// whatever slot its parent already reserved for it.
func PrepareTaggedUnion(a *Arena, tag uint32, fields FieldWriter) FieldWriter {
	return func(pos int) {
		PutTag1(a, pos, uint8(tag))
		fields(pos)
	}
}

// TagField reserves the enum discriminant's own slot as field 0 of a
// Layout, and returns the Layout so the variant's own fields can be
// reserved immediately after via further Field calls. Every
// enum-with-data variant's field-offset Layout must start with this call.
//
// The discriminant is a single byte (align 1), not the 4-byte tag this
// package originally assumed. That assumption produced correct results by
// accident for every variant whose first field needed exactly 4-byte
// alignment (a String, the common case) - the 4-byte-tag overpadding just
// happened to land in the same place a 1-byte tag would after correct
// alignment padding to 4. It broke silently for anything else: a field
// needing only 1-byte alignment (e.g. RequestJob{kind: JobKind}, a single
// enum byte) ended up 3 bytes later than real rkyv puts it, decodable by
// this package's own (equally wrong) reader but not by a real Gradient
// server. Found via the same real-decoder cross-validation workflow
// described in testdata/gen-fixtures/README.md - always check newly
// implemented variants against it, especially ones whose first field isn't
// a String.
func TagField() *Layout {
	l := &Layout{}
	l.Field(1, 1)
	return l
}

// EncodeRoot finalizes a root-level enum-with-data message: reserves the
// fixed-size slot (fixedSize = the enum's constant total size, e.g. 160 for
// ClientMessage; align = that enum's own alignment, e.g. 8 if any variant
// contains a u64/i64/f64 - see testdata/gen-fixtures/README.md), writes the
// 1-byte variant tag (see TagField's doc), fills the active variant's
// fields via `fields` (offsets already computed relative to the variant's
// own start via TagField - see PrepareTaggedUnion), and returns the
// completed buffer. Must be called only after every other value in the
// message has already been prepared (written to a), so the root ends up
// last, per rkyv's from_bytes root-placement convention.
func EncodeRoot(a *Arena, align, fixedSize int, tag uint32, fields FieldWriter) []byte {
	rootPos := a.Reserve(align, fixedSize)
	PutTag1(a, rootPos, uint8(tag))
	fields(rootPos)
	return a.Bytes()
}
