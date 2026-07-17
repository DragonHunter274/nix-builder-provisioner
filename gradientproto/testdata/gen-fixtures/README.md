# rkyv golden fixture generator

Disposable Rust crate that serializes representative Gradient protocol
messages with the real `rkyv = "0.8"` crate (pinned to the version Gradient's
`backend/Cargo.toml` workspace uses) so the Go codec in `gradientproto/wire`
can be tested against byte-exact ground truth instead of a hand-derived
guess at rkyv's archive format.

This is **dev-only tooling** - never a build or runtime dependency of
`nix-builder-provisioner`. The generated `.bin` files under
`gradientproto/testdata/fixtures/` are committed to the repo; normal
`go test` runs never need Rust. Only regenerate fixtures if Gradient's
upstream `gradient-types`/`gradient-proto` struct/enum definitions change in
a way that could affect wire layout (new field, reordered field, new enum
variant inserted before existing ones, etc.).

`src/types.rs` and `src/messages.rs` are verbatim copies of
[wavelens/gradient](https://github.com/wavelens/gradient)'s
`backend/gradient-types/src/proto.rs` and
`backend/gradient-proto/src/messages/{client,server}.rs`, with only the
`serde` derives stripped (irrelevant to the rkyv wire format) and imports
flattened into one crate. Keep them byte-for-byte faithful to upstream aside
from that - field order and enum variant order determine the archive layout.

## Regenerating

Requires a Rust toolchain (not otherwise present in this repo/environment):

```
nix shell nixpkgs#cargo nixpkgs#rustc --command cargo run           # writes fixtures/*.bin
nix shell nixpkgs#cargo nixpkgs#rustc --command cargo run --bin probe  # writes fixtures/probe/*.bin, prints enum sizes
cp fixtures/*.bin ../fixtures/
```

## rkyv 0.8 wire format notes (reverse-engineered from `rkyv-0.8.17` source)

No official cross-language spec exists; this is what decoding
`rkyv-0.8.17/src/{string/repr.rs,vec.rs,option.rs,rel_ptr.rs}` plus empirical
probing (`src/bin/probe.rs`) established. Everything is little-endian on this
build (no `big_endian` feature enabled).

- **Primitives**: `bool` = 1 byte (0/1). `u16/u32/u64/f32/f64` = native-width
  little-endian, no special treatment.
- **Struct**: fields laid out in declaration order, C-style alignment/padding
  between fields (each field starts at the next offset satisfying its own
  alignment), trailing padding to round the struct size up to its own
  alignment (= max field alignment).
- **String** (`ArchivedString`, always 8 bytes for the fixed slot,
  `INLINE_CAPACITY = 8`, `align = 4`):
  - **Inline (SSO)** when `len <= 8`: all 8 bytes default to `0xFF`
    (impossible as the leading byte of valid UTF-8, hence safe as a
    sentinel), then the content bytes overwrite `bytes[0..len]`. Decode: scan
    for the first `0xFF` byte; its position is the length, or `8` if none
    found (full 8 content bytes, no terminator fits).
  - **Out-of-line** when `len > 8`: content is written elsewhere in the
    buffer (padded up to a 4-byte boundary), and the 8-byte slot holds
    `[len_word: u32 LE][offset: i32 LE]`:
    - `offset` = simple relative offset, `content_absolute_pos -
      slot_absolute_pos` (typically negative - content precedes the slot).
    - `len_word` packs the real length using the low 6 bits + bit 7 as an
      "out-of-line" flag, with the rest of the length shifted up 2 bits:
      `len_word = (len & 0x3F) | 0x80 | ((len & !0x3F) << 2)`. Decode:
      `len = (len_word & 0x3F) | ((len_word & !0xFF) >> 2)`.
    - Discriminant check: `is_inline = (bytes[0] & 0xC0) != 0x80` (relies on
      the same fact - a valid inline string's first byte, if UTF-8, never has
      the bit pattern `10xxxxxx`).
- **Vec\<T\>** (`ArchivedVec<T>`, always 8 bytes, `align = 4`, always
  out-of-line - no inlining, unlike String): `[ptr_offset: i32 LE][len: u32
  LE]` - **note field order is ptr then len**, opposite of String's
  `len`-then-`offset`. `ptr_offset` is relative from the slot to the first
  element; elements are laid out contiguously per `T`'s own layout, padded up
  to `T`'s alignment. **Empty vec is NOT `offset=0, len=0`.** rkyv's own
  `serialize_from_iter` still calls `serializer.align_for::<T>()` for zero
  elements - which writes nothing but still advances/aligns the write
  cursor - and uses *that* position as the pointer target, i.e. `offset =
  (arena position at the time this field was serialized) - (this field's own
  slot position)`, same formula as the non-empty case just with zero
  elements written there. A hardcoded `offset=0` (pointing the descriptor at
  itself) round-trips fine through this package's own decoder but fails real
  `bytecheck` validation once anything precedes it in declaration order,
  because the position it should have pointed to has already been claimed by
  an earlier sibling field - see the next bullet.
- **Option\<T\>** (`ArchivedOption<T>`, `#[repr(u8)]`): 1-byte tag
  (`0=None, 1=Some`) + padding to `align_of(T)` + `T`'s bytes (zeroed when
  `None`, not just uninitialized - verified empirically).
- **Fieldless enum** (all unit variants, e.g. `JobKind`, `QueryMode`,
  `BuildFailureKind`): single byte, value = declaration order (0-indexed).
- **Enum with data** (e.g. `ClientMessage`, `ServerMessage`, `Job`,
  `JobUpdateKind`): **C-union style.** A **1-byte** tag (`= variant
  declaration index`, same single-byte discriminant as a fieldless enum -
  see `wire.Decoder.Tag1`/`wire.PutTag1`) at offset 0, followed by the
  active variant's fields laid out exactly like a plain struct (same
  field-order/alignment rules as above), **starting at whatever offset the
  first field's own alignment requires after a 1-byte tag** - not
  unconditionally at offset 4. A field needing only 1-byte alignment (a
  fieldless-enum field, e.g. `RequestJob{kind: JobKind}`) packs immediately
  at offset 1; a field needing 4-byte alignment (a `String`, the common
  case) lands at offset 4 after 3 padding bytes; a field needing 8-byte
  alignment would land at offset 8. **This was found the hard way**: an
  earlier version of this package assumed a fixed 4-byte tag, which is
  correct by coincidence for every variant whose first field happens to
  need exactly 4-byte alignment (the majority - a `String` is almost always
  first), silently wrong for anything else, and undetectable by round-
  tripping through this package's own reader (which shared the same wrong
  assumption on both ends). Only cross-checking against the real `rkyv`
  decoder (see "Verifying" below) surfaced it, via `ClientMessage::
  RequestJob{kind: JobKind}` - `kind` (1-byte alignment) exposed the
  3-byte drift a `String`-first variant couldn't. `wire.TagField()`
  encodes this correctly by treating the tag as a real Layout field
  (`Field(1, 1)`) that subsequent `Field` calls pad around normally - use
  it for every enum-with-data variant's offset Layout, never hand-roll "tag
  is N bytes."
  After the active variant's fields, the whole thing is zero-padded out to
  a **fixed total size AND alignment shared by every variant of that enum
  type** - i.e. `size_of::<Archived<Enum>>()`/`align_of::<Archived<Enum>>()`
  are constant regardless of which variant is active, both sized to
  whichever variant has the largest/widest fixed (non-out-of-line)
  footprint across the *entire* enum. This has two consequences:
  - Out-of-line content referenced by the active variant's fields (long
    strings, vec elements) is written elsewhere in the buffer as usual;
    unused variants contribute nothing to the total.
  - **The enum's own alignment can be wider than any of the fields you
    actually implement**, if a variant you haven't implemented (or a field
    deep inside one you have, e.g. `JobUpdateKind::BuildOutput` containing
    `Option<BuildMetrics>`, and `BuildMetrics` containing a `u64`) needs
    8-byte alignment. Get this from `align_of::<Archived<Enum>>()`
    (`print_aligns()` in `probe.rs`), not by inspecting only the fields you
    plan to use - and use it both when reserving the enum's own root slot
    (`EncodeRoot`'s `align` parameter) and whenever the enum is embedded as
    a field of another type (its own field-list `Field` call must pass this
    alignment, not a guess).
  **Measured total sizes / alignments (bytes, current gradient-types
  definitions)**: `ClientMessage=160/8`, `ServerMessage=112/8`, `Job=96/8`,
  `JobUpdateKind=144/8`. These must be re-measured (`cargo run --bin
  probe`) whenever a variant gains/loses fields upstream, since both are
  derived from whichever variant happens to be largest/widest - a field
  added to a variant this package doesn't even implement can still change
  these numbers.
- **Root placement**: `rkyv::to_bytes` / `rkyv::from_bytes` treat the
  outermost value as occupying the **last** `size_of::<Archived<T>>()` bytes
  of the buffer; everything before that is out-of-line data referenced by
  relative offsets.
- **Sibling out-of-line fields MUST be written in declaration order -
  this is not optional, and getting it wrong doesn't fail loudly.** A
  struct's fields are validated by `bytecheck` in declaration order, and
  each one's `check_subtree_ptr` call happens against a validation window
  that has already been narrowed by every field validated before it
  (`ArchiveValidator`'s `subtree_range`, see
  `rkyv-0.8.17/src/validation/archive/validator.rs`). In practice this
  means: if field 1 is prepared/written to the arena before field 0 (e.g.
  because a Go helper hoists field 1's `Prepare*` call into a local variable
  above field 0's, which is easy to do by accident once a struct has a mix
  of unconditional and `if x != nil` fields), the resulting bytes still
  decode correctly through *this package's own* decoder - offsets are
  self-describing, order doesn't affect what our own reader computes - but
  **real `bytecheck` validation rejects them** with something like `subtree
  pointer overran range`, or worse, silently validates but a sibling field
  ends up reading as `None`/zero/default despite being populated, because
  the offsets still resolve to *some* in-bounds position, just the wrong
  one. Only cross-checking against the real decoder (see Verifying below)
  caught this - our own round-trip tests could not, by construction. The
  fix applied throughout this package: every `prepareX` function with more
  than one field computes each field's `wire.FieldWriter` via a sequential
  Go statement in the *same order* as the Rust struct's field declarations,
  never hoisted out of order relative to a field that also writes
  out-of-line content (primitive-only fields - bools, plain integers,
  `Option<primitive>` - are exempt, since they never touch the arena at
  prepare time regardless of when they're computed).
- **Enum-with-data field offsets must account for the tag's own width and
  alignment, not assume "immediately after 4 bytes."** The 4-byte
  discriminant is effectively field 0 of the variant's own layout ` -
  `wire.TagField()` reserves it as such before any variant field is laid
  out. Skipping this and hardcoding "fields start at `tag_pos + 4`"
  produces correct results only when every field in the variant needs ≤
  4-byte alignment; a field needing 8-byte alignment (anything containing a
  `u64`/`i64`/`f64`, directly or nested, e.g. `BuildMetrics` inside
  `JobUpdateKind::BuildOutput`) actually starts 4 bytes later than that,
  and every field after it inherits the same 4-byte drift. Like the
  ordering bug above, this decodes "successfully" through a decoder that
  shares the same wrong assumption - it only surfaces as a real bytecheck
  failure or, more insidiously, as a field silently decoding to the wrong
  (but plausible-looking, e.g. `None`/`false`) value.
- **Given both of the above, a Go encoder does *not* have unconstrained
  freedom in how it arranges output** - the earlier draft of this note was
  wrong. Field values, an enum's declaration-order-derived field offsets,
  and the *order* sibling out-of-line fields are written in are all
  load-bearing for real bytecheck validation, not just for byte-exact
  reproduction of rkyv's own writer. What genuinely is free: exactly how
  far apart or interleaved unrelated top-level values' out-of-line content
  is (e.g. two different Vec elements' own children don't need to be
  contiguous with each other) - just never reorder a single value's own
  sibling fields relative to their declaration order.

## Verifying against the real decoder, not just this package's own

Round-tripping through this package's own `Decoder` cannot catch bugs where
the encoder and decoder share the same wrong assumption (both of the bugs
above did exactly that - decoded "fine" through our own reader, rejected or
silently wrong through the real one). `src/bin/verify.rs` closes that gap:
it links the actual `rkyv = "0.8"` crate with `bytecheck` validation enabled
and decodes an arbitrary `.bin` file as a named type, printing the full
`Debug` output on success.

```
# from a Go test, write the encoder's output to a file, then:
nix shell nixpkgs#cargo nixpkgs#rustc --command \
  cargo run --bin verify -- JobUpdateKind /tmp/some_dump.bin
```

Add new types to `verify.rs`'s `match` as needed (anything `pub` in
`src/types.rs` or `src/messages.rs`). When implementing a new message
variant, dump a populated value with distinguishable field values (not all
zeros/empty - those can accidentally "validate" against wrong offsets) and
check the printed Debug output field-by-field against what was encoded,
not just that it printed `OK`.
