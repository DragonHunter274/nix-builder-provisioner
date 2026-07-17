package wire

// OptionSize returns the fixed size of ArchivedOption<T> given T's
// alignment and size: a 1-byte tag, padded up to elemAlign, followed by
// T's own bytes. ArchivedOption<T> is #[repr(u8)] with the discriminant
// (0 = None, 1 = Some) occupying byte 0; the payload always starts at
// elemAlign (padding up from 1), even for None (the payload region is
// zeroed, not omitted).
func OptionSize(elemAlign, elemSize int) int {
	return padUp(1, elemAlign) + elemSize
}

// PrepareOptionNone returns a FieldWriter for a None value. No arena writes
// are needed at prepare time - Arena.Reserve already zero-initializes the
// slot, and tag 0 is the None discriminant - but the tag byte is written
// explicitly for clarity.
func PrepareOptionNone() FieldWriter {
	return func(pos int) {
		// Tag byte and payload are already zero from Reserve; nothing to do.
	}
}

// PrepareOptionSome returns a FieldWriter for a Some value: writes tag 1,
// then finalizes the already-prepared payload writer at the payload
// position (pos padded up to elemAlign).
func PrepareOptionSome(a *Arena, elemAlign int, val FieldWriter) FieldWriter {
	return func(pos int) {
		a.WriteByteAt(pos, 1)
		val(pos + padUp(1, elemAlign))
	}
}

// OptionIsSome reports whether the ArchivedOption<T> at pos is Some.
func (d *Decoder) OptionIsSome(pos int) bool {
	return d.Buf[pos] != 0
}

// OptionValPos returns the absolute position of the payload of an
// ArchivedOption<T> at pos, given T's alignment. Only meaningful when
// OptionIsSome(pos) is true.
func OptionValPos(pos, elemAlign int) int {
	return pos + padUp(1, elemAlign)
}
