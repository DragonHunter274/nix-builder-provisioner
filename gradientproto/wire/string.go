package wire

// StringSize is the fixed size in bytes of an ArchivedString slot,
// regardless of whether the string ends up inline or out-of-line.
const StringSize = 8

// StringAlign is ArchivedString's alignment (matches its widest field,
// a u32 length/offset word).
const StringAlign = 4

// inlineCapacity is the maximum string length rkyv stores inline (SSO).
const inlineCapacity = 8

// PrepareString returns a FieldWriter for s. Strings of length <= 8 are
// stored inline: all 8 bytes default to 0xFF (never a valid leading byte
// of UTF-8, so safe as an end-of-content sentinel) and the content
// overwrites bytes[0:len(s)] - this needs no arena writes at prepare time.
//
// Longer strings are stored out-of-line: the content is written to the
// arena tail immediately (padded up to a 4-byte boundary) and its position
// captured, so the returned FieldWriter can compute a relative offset once
// it learns its own final position. The 8-byte descriptor is a packed
// length word (low 6 bits + bit 7 flag + shifted high bits - see
// packOutOfLineLen) and a signed 4-byte relative offset.
func PrepareString(a *Arena, s string) FieldWriter {
	if len(s) <= inlineCapacity {
		var b [8]byte
		for i := range b {
			b[i] = 0xFF
		}
		copy(b[:], s)
		return func(pos int) { a.CopyAt(pos, b[:]) }
	}

	contentPos := a.Reserve(4, padUp(len(s), 4))
	a.CopyAt(contentPos, []byte(s))

	return func(pos int) {
		offset := int32(contentPos - pos)
		PutU32(a, pos, packOutOfLineLen(uint32(len(s))))
		PutI32(a, pos+4, offset)
	}
}

// packOutOfLineLen encodes a string length for the out-of-line
// ArchivedString repr: low 6 bits of len, bit 7 set as the out-of-line
// flag, remaining high bits of len shifted left by 2 (leaving bit 6 always
// clear). Mirrors rkyv-0.8.17's ArchivedStringRepr::try_emplace_out_of_line
// (little-endian branch).
func packOutOfLineLen(l uint32) uint32 {
	return (l & 0x3F) | 0x80 | ((l &^ 0x3F) << 2)
}

// unpackOutOfLineLen is the inverse of packOutOfLineLen. Mirrors
// ArchivedStringRepr::len's little-endian branch.
func unpackOutOfLineLen(raw uint32) uint32 {
	return (raw & 0x3F) | ((raw &^ 0xFF) >> 2)
}

// padUp rounds n up to the next multiple of align.
func padUp(n, align int) int {
	rem := n % align
	if rem == 0 {
		return n
	}
	return n + (align - rem)
}

// String decodes the ArchivedString at pos.
func (d *Decoder) String(pos int) string {
	b0 := d.Buf[pos]
	if b0&0xC0 != 0x80 {
		// Inline: scan for the first 0xFF sentinel; absent means all 8
		// bytes are content.
		end := inlineCapacity
		for i := 0; i < inlineCapacity; i++ {
			if d.Buf[pos+i] == 0xFF {
				end = i
				break
			}
		}
		return string(d.Buf[pos : pos+end])
	}

	length := int(unpackOutOfLineLen(d.U32(pos)))
	offset := int(d.I32(pos + 4))
	start := pos + offset
	return string(d.Buf[start : start+length])
}
