package wire

// Layout incrementally computes C-style struct field offsets as fields are
// declared in declaration order, mirroring the alignment/padding rules real
// rkyv-derived structs use (see arena.go's package doc): each field starts
// at the next offset satisfying its own alignment, and the struct's overall
// alignment is the max alignment across all fields. Use one Layout per
// struct type to get each field's offset via Field, then Size for the
// (padded) total.
//
// This exists so struct offsets for the ~20 message types in this package
// are computed mechanically instead of by hand - a single off-by-one in a
// manually computed offset is exactly the kind of bug the golden fixture
// tests in wire/fixtures_test.go are meant to catch, but getting it right
// the first time via Layout is cheaper than debugging it.
type Layout struct {
	offset int
	align  int
}

// Field reserves size bytes aligned to align and returns their offset.
func (l *Layout) Field(align, size int) int {
	if align > l.align {
		l.align = align
	}
	if align > 1 {
		if rem := l.offset % align; rem != 0 {
			l.offset += align - rem
		}
	}
	pos := l.offset
	l.offset += size
	return pos
}

// Size returns the struct's total size, padded up to its own alignment
// (the max alignment of any field reserved so far).
func (l *Layout) Size() int {
	if l.align <= 1 {
		return l.offset
	}
	if rem := l.offset % l.align; rem != 0 {
		return l.offset + (l.align - rem)
	}
	return l.offset
}

// Align returns the struct's own alignment (the max alignment of any field
// reserved so far), for use when this struct is itself embedded as a field
// of another struct/enum.
func (l *Layout) Align() int {
	if l.align == 0 {
		return 1
	}
	return l.align
}
