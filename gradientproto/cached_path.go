package gradientproto

import "nix-builder-provisioner/gradientproto/wire"

// CachedPath mirrors gradient_types::proto::CachedPath: a store path entry
// returned in ServerMessage::CacheStatus, responding to a
// ClientMessage::CacheQuery. Only decoded, never encoded - we only ever
// receive these from the server.
type CachedPath struct {
	Path       string
	Cached     bool
	FileSize   *uint64
	NarSize    *uint64
	URL        *string
	NarHash    *string
	FileHash   *string
	References *[]string
	Signatures *[]string
	Deriver    *string
	CA         *string
}

var cachedPathOff struct {
	Path, Cached, FileSize, NarSize, URL, NarHash, FileHash, References, Signatures, Deriver, CA int
	Align, Size                                                                                  int
}

func init() {
	l := &wire.Layout{}
	cachedPathOff.Path = l.Field(wire.StringAlign, wire.StringSize)
	cachedPathOff.Cached = l.Field(1, 1)
	cachedPathOff.FileSize = l.Field(8, wire.OptionSize(8, 8))
	cachedPathOff.NarSize = l.Field(8, wire.OptionSize(8, 8))
	cachedPathOff.URL = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	cachedPathOff.NarHash = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	cachedPathOff.FileHash = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	cachedPathOff.References = l.Field(wire.VecAlign, wire.OptionSize(wire.VecAlign, wire.VecSize))
	cachedPathOff.Signatures = l.Field(wire.VecAlign, wire.OptionSize(wire.VecAlign, wire.VecSize))
	cachedPathOff.Deriver = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	cachedPathOff.CA = l.Field(wire.StringAlign, wire.OptionSize(wire.StringAlign, wire.StringSize))
	cachedPathOff.Align = l.Align()
	cachedPathOff.Size = l.Size()
}

func decodeOptString(d *wire.Decoder, pos int) *string {
	if !d.OptionIsSome(pos) {
		return nil
	}
	v := d.String(wire.OptionValPos(pos, wire.StringAlign))
	return &v
}

func decodeOptU64(d *wire.Decoder, pos int) *uint64 {
	if !d.OptionIsSome(pos) {
		return nil
	}
	v := d.U64(wire.OptionValPos(pos, 8))
	return &v
}

func decodeOptStringVec(d *wire.Decoder, pos int) *[]string {
	if !d.OptionIsSome(pos) {
		return nil
	}
	vecPos := wire.OptionValPos(pos, wire.VecAlign)
	out := make([]string, 0, d.VecLen(vecPos))
	d.VecEach(vecPos, wire.StringSize, func(elemPos, index int) {
		out = append(out, d.String(elemPos))
	})
	return &out
}

func decodeCachedPath(d *wire.Decoder, pos int) CachedPath {
	return CachedPath{
		Path:       d.String(pos + cachedPathOff.Path),
		Cached:     d.Bool(pos + cachedPathOff.Cached),
		FileSize:   decodeOptU64(d, pos+cachedPathOff.FileSize),
		NarSize:    decodeOptU64(d, pos+cachedPathOff.NarSize),
		URL:        decodeOptString(d, pos+cachedPathOff.URL),
		NarHash:    decodeOptString(d, pos+cachedPathOff.NarHash),
		FileHash:   decodeOptString(d, pos+cachedPathOff.FileHash),
		References: decodeOptStringVec(d, pos+cachedPathOff.References),
		Signatures: decodeOptStringVec(d, pos+cachedPathOff.Signatures),
		Deriver:    decodeOptString(d, pos+cachedPathOff.Deriver),
		CA:         decodeOptString(d, pos+cachedPathOff.CA),
	}
}
