package wire

// VecSize is the fixed size in bytes of an ArchivedVec<T> slot: a signed
// offset to the backing array plus an element count. Unlike String, Vec has
// no inline representation - it is always out-of-line, even when empty.
const VecSize = 8

// VecAlign is ArchivedVec<T>'s alignment (matches its widest field).
const VecAlign = 4

// PrepareVec returns a FieldWriter for a vec of n elements (each elemSize
// bytes, aligned to elemAlign). prepareElem is called once per element,
// index-order, to obtain that element's own FieldWriter (this is where an
// element's out-of-line content, if any, gets written to the arena) -
// PrepareVec then reserves a contiguous array block and finalizes each
// element into its slot before returning the vec's own descriptor writer:
// {offset: i32, len: u32} at the field's position - note the field order
// is offset-then-len, the reverse of ArchivedString's len-then-offset.
func PrepareVec(a *Arena, n int, elemAlign, elemSize int, prepareElem func(index int) FieldWriter) FieldWriter {
	// Mirrors ArchivedVec::serialize_from_iter exactly: every element's own
	// out-of-line content is written first (index order - this is where a
	// nested struct's long strings, etc. end up in the arena), and only
	// after all of that is the contiguous array block itself reserved and
	// filled. rkyv's bytecheck validation is sensitive to this ordering
	// (see the n==0 case below and the wire package doc), so this isn't
	// just cosmetic - reserving the array block earlier makes downstream
	// siblings' validation ranges disagree with what a real Gradient
	// server's decoder expects.
	elemWriters := make([]FieldWriter, n)
	for i := 0; i < n; i++ {
		elemWriters[i] = prepareElem(i)
	}

	// Even for n == 0 - no element data to write - the offset is NOT 0:
	// real rkyv still computes it from wherever the arena's write cursor
	// sits at this point (`serializer.align_for::<T>()`, which advances/
	// aligns the position without writing bytes). A hardcoded offset of 0
	// (pointing the slice descriptor at itself) can fall outside the
	// validator's current allowed subtree range, which shrinks as sibling
	// fields are validated in declaration order - bytecheck then rejects
	// an otherwise-harmless empty vec. Reserving 0 bytes here reproduces
	// align_for's "capture the current position" behavior exactly.
	arrPos := a.Reserve(elemAlign, n*elemSize)
	for i, w := range elemWriters {
		w(arrPos + i*elemSize)
	}

	return func(pos int) {
		offset := int32(arrPos - pos)
		PutI32(a, pos, offset)
		PutU32(a, pos+4, uint32(n))
	}
}

// VecLen returns the element count of the ArchivedVec<T> at pos.
func (d *Decoder) VecLen(pos int) int {
	return int(d.U32(pos + 4))
}

// VecEach decodes the ArchivedVec<T> at pos, calling decodeElem once per
// element with that element's absolute position.
func (d *Decoder) VecEach(pos int, elemSize int, decodeElem func(elemPos, index int)) {
	n := d.VecLen(pos)
	if n == 0 {
		return
	}
	offset := int(d.I32(pos))
	arrPos := pos + offset
	for i := 0; i < n; i++ {
		decodeElem(arrPos+i*elemSize, i)
	}
}
