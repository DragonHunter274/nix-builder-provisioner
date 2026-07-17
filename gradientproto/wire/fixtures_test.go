package wire

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "testdata", "fixtures", name+".bin")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func loadProbeFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "testdata", "fixtures", "probe", name+".bin")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading probe fixture %s: %v", name, err)
	}
	return b
}

// TestStringProbe validates String decode against every length the Rust
// probe generated (fixtures/probe/str_NNNNNN.bin, each a bare `String`
// serialized on its own), covering the SSO inline/out-of-line boundary at
// 8 bytes and the packed-length-word behavior across several byte-size
// classes of the encoded length. It also round-trips our own encoder
// (PrepareString) through our own decoder.
func TestStringProbe(t *testing.T) {
	lengths := []int{0, 1, 3, 6, 7, 8, 9, 10, 11, 12, 13, 15, 16, 20, 100, 126, 127, 128, 129, 130, 200, 255, 256, 257, 300, 65535, 65536, 65537}
	for _, n := range lengths {
		want := make([]byte, n)
		for i := range want {
			want[i] = 'x'
		}
		wantStr := string(want)

		fixture := loadProbeFixture(t, fmt.Sprintf("str_%06d", n))
		// The bare String is itself the "root" here, so it occupies the
		// last StringSize bytes of the buffer.
		pos := len(fixture) - StringSize
		got := NewDecoder(fixture).String(pos)
		if got != wantStr {
			t.Errorf("len=%d: decode mismatch: got len %d, want len %d", n, len(got), len(wantStr))
		}

		// Round-trip through our own encoder. Our encoder is free to place
		// out-of-line content differently than rkyv's own writer (see
		// arena.go's package doc on order-independence), so we don't assert
		// byte-for-byte equality with the fixture here - decode/re-decode
		// equivalence is the correctness bar. Cross-validation against the
		// real rkyv decoder happens separately (see EncodeRoot tests).
		a := NewArena()
		w := PrepareString(a, wantStr)
		p := a.Reserve(StringAlign, StringSize)
		w(p)
		got2 := NewDecoder(a.Bytes()).String(p)
		if got2 != wantStr {
			t.Errorf("len=%d: round-trip mismatch: got len %d, want len %d", n, len(got2), len(wantStr))
		}
	}
}

// TestVecU32Probe validates Vec<u32> decode against fixtures/probe/vecu32_*.bin
// and round-trips our own encoder.
func TestVecU32Probe(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 300} {
		want := make([]uint32, n)
		for i := range want {
			want[i] = uint32(i)
		}

		fixture := loadProbeFixture(t, fmt.Sprintf("vecu32_%06d", n))
		pos := len(fixture) - VecSize
		d := NewDecoder(fixture)
		if got := d.VecLen(pos); got != n {
			t.Fatalf("n=%d: VecLen = %d", n, got)
		}
		got := make([]uint32, 0, n)
		d.VecEach(pos, 4, func(elemPos, index int) {
			got = append(got, d.U32(elemPos))
		})
		if !equalU32(got, want) {
			t.Errorf("n=%d: decode mismatch: got %v, want %v", n, got, want)
		}

		a := NewArena()
		w := PrepareVec(a, n, 4, 4, func(index int) FieldWriter {
			return PrepareU32(a, want[index])
		})
		p := a.Reserve(VecAlign, VecSize)
		w(p)
		d2 := NewDecoder(a.Bytes())
		got2 := make([]uint32, 0, n)
		d2.VecEach(p, 4, func(elemPos, index int) {
			got2 = append(got2, d2.U32(elemPos))
		})
		if !equalU32(got2, want) {
			t.Errorf("n=%d: round-trip mismatch: got %v, want %v", n, got2, want)
		}
	}
}

func equalU32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOptionProbe validates Option<u32> and Option<String> against the
// probe fixtures and round-trips our own encoder.
func TestOptionProbe(t *testing.T) {
	t.Run("u32 none", func(t *testing.T) {
		fixture := loadProbeFixture(t, "option_u32_none")
		size := OptionSize(4, 4)
		pos := len(fixture) - size
		if NewDecoder(fixture).OptionIsSome(pos) {
			t.Fatal("expected None")
		}
	})
	t.Run("u32 some", func(t *testing.T) {
		fixture := loadProbeFixture(t, "option_u32_some")
		size := OptionSize(4, 4)
		pos := len(fixture) - size
		d := NewDecoder(fixture)
		if !d.OptionIsSome(pos) {
			t.Fatal("expected Some")
		}
		got := d.U32(OptionValPos(pos, 4))
		if want := uint32(0x11223344); got != want {
			t.Fatalf("got %#x, want %#x", got, want)
		}
	})
	t.Run("string some round-trip", func(t *testing.T) {
		fixture := loadProbeFixture(t, "option_string_some")
		size := OptionSize(StringAlign, StringSize)
		pos := len(fixture) - size
		d := NewDecoder(fixture)
		if !d.OptionIsSome(pos) {
			t.Fatal("expected Some")
		}
		got := d.String(OptionValPos(pos, StringAlign))
		if got != "hi" {
			t.Fatalf("got %q, want %q", got, "hi")
		}

		a := NewArena()
		w := PrepareOptionSome(a, StringAlign, PrepareString(a, "hi"))
		p := a.Reserve(StringAlign, size)
		w(p)
		d2 := NewDecoder(a.Bytes())
		if !d2.OptionIsSome(p) {
			t.Fatal("round-trip: expected Some")
		}
		if got := d2.String(OptionValPos(p, StringAlign)); got != "hi" {
			t.Fatalf("round-trip: got %q, want %q", got, "hi")
		}
	})
}

// TestCandidateScore validates a struct with mixed alignment (String, u32,
// u64) - the u64 field forces 4 bytes of padding after the u32 - against
// the real CandidateScore fixture: job_id="j-1", missing_count=3,
// missing_nar_size=987654321.
func TestCandidateScore(t *testing.T) {
	fixture := loadFixture(t, "CandidateScore")
	const size = 24 // String(8) + u32(4) + pad(4) + u64(8)
	if len(fixture) != size {
		t.Fatalf("fixture size = %d, want %d", len(fixture), size)
	}
	d := NewDecoder(fixture)
	if got := d.String(0); got != "j-1" {
		t.Errorf("job_id = %q, want %q", got, "j-1")
	}
	if got := d.U32(8); got != 3 {
		t.Errorf("missing_count = %d, want 3", got)
	}
	if got := d.U64(16); got != 987654321 {
		t.Errorf("missing_nar_size = %d, want 987654321", got)
	}

	a := NewArena()
	w := PrepareStruct(
		Field{0, PrepareString(a, "j-1")},
		Field{8, PrepareU32(a, 3)},
		Field{16, PrepareU64(a, 987654321)},
	)
	p := a.Reserve(8, size)
	w(p)
	if got := a.Bytes()[p : p+size]; string(got) != string(fixture) {
		t.Errorf("byte-exact encode mismatch: got % x, want % x", got, fixture)
	}
}

// TestRequiredPath validates a struct with an Option<struct> field
// (Option<CacheInfo>, align 8) against the real fixtures, including the
// out-of-line long-string path.
func TestRequiredPath(t *testing.T) {
	const path = "/nix/store/gggg...-baz"
	const structSize = 32 // String(8) + Option<CacheInfo>(1+7pad+16=24)

	t.Run("no cache info", func(t *testing.T) {
		fixture := loadFixture(t, "RequiredPath_no_cache_info")
		pos := len(fixture) - structSize
		d := NewDecoder(fixture)
		if got := d.String(pos); got != path {
			t.Errorf("path = %q, want %q", got, path)
		}
		if d.OptionIsSome(pos + 8) {
			t.Error("expected cache_info = None")
		}
	})

	t.Run("with cache info", func(t *testing.T) {
		fixture := loadFixture(t, "RequiredPath_with_cache_info")
		pos := len(fixture) - structSize
		d := NewDecoder(fixture)
		if got := d.String(pos); got != path {
			t.Errorf("path = %q, want %q", got, path)
		}
		optPos := pos + 8
		if !d.OptionIsSome(optPos) {
			t.Fatal("expected cache_info = Some")
		}
		valPos := OptionValPos(optPos, 8)
		if got := d.U64(valPos); got != 111 {
			t.Errorf("file_size = %d, want 111", got)
		}
		if got := d.U64(valPos + 8); got != 222 {
			t.Errorf("nar_size = %d, want 222", got)
		}

		a := NewArena()
		cacheInfo := PrepareStruct(
			Field{0, PrepareU64(a, 111)},
			Field{8, PrepareU64(a, 222)},
		)
		w := PrepareStruct(
			Field{0, PrepareString(a, path)},
			Field{8, PrepareOptionSome(a, 8, cacheInfo)},
		)
		p := a.Reserve(8, structSize)
		w(p)
		d2 := NewDecoder(a.Bytes())
		if got := d2.String(p); got != path {
			t.Errorf("round-trip: path = %q, want %q", got, path)
		}
		if got := d2.U64(OptionValPos(p+8, 8)); got != 111 {
			t.Errorf("round-trip: file_size = %d, want 111", got)
		}
	})
}

// TestClientMessageEnumLayout validates the enum-with-data "C-union"
// layout - fixed 4-byte tag, variant fields starting right after it (padded
// further if the variant's widest field needs more than 4-byte alignment -
// see TagField), padded to a constant total size shared by every
// ClientMessage variant (measured as 160 bytes via
// testdata/gen-fixtures/src/bin/probe.rs) - against real fixtures for two
// field-less/small variants where no out-of-line content makes byte-exact
// comparison meaningful.
func TestClientMessageEnumLayout(t *testing.T) {
	const clientMessageSize = 160
	const clientMessageAlign = 8 // widest field across all variants needs a u64/f64

	t.Run("Draining", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_Draining")
		const tagDraining = 12 // 0-indexed declaration order in ClientMessage
		a := NewArena()
		buf := EncodeRoot(a, clientMessageAlign, clientMessageSize, tagDraining, PrepareStruct())
		if string(buf) != string(fixture) {
			t.Errorf("byte-exact encode mismatch:\ngot  % x\nwant % x", buf, fixture)
		}
		if got := NewDecoder(fixture).U32(0); got != tagDraining {
			t.Errorf("decoded tag = %d, want %d", got, tagDraining)
		}
	})

	t.Run("JobCompleted", func(t *testing.T) {
		fixture := loadFixture(t, "ClientMessage_JobCompleted")
		const tagJobCompleted = 10
		l := TagField()
		jobIDOff := l.Field(StringAlign, StringSize)
		a := NewArena()
		fields := PrepareStruct(Field{jobIDOff, PrepareString(a, "j-1")})
		buf := EncodeRoot(a, clientMessageAlign, clientMessageSize, tagJobCompleted, fields)
		if string(buf) != string(fixture) {
			t.Errorf("byte-exact encode mismatch:\ngot  % x\nwant % x", buf, fixture)
		}

		d := NewDecoder(fixture)
		if got := d.U32(0); got != tagJobCompleted {
			t.Errorf("decoded tag = %d, want %d", got, tagJobCompleted)
		}
		if got := d.String(jobIDOff); got != "j-1" {
			t.Errorf("decoded job_id = %q, want %q", got, "j-1")
		}
	})
}

// TestGradientCapabilities validates a plain all-bool struct against the
// real fixtures, and round-trips our own encoder.
func TestGradientCapabilities(t *testing.T) {
	// struct GradientCapabilities { core, federate, fetch, eval, build, cache bool }
	cases := []struct {
		name                                      string
		core, federate, fetch, eval, build, cache bool
	}{
		{"GradientCapabilities_default", false, false, false, false, false, false},
		{"GradientCapabilities_build_only", false, false, false, false, true, false},
		{"GradientCapabilities_all_true", true, true, true, true, true, true},
	}
	const size = 6
	for _, c := range cases {
		fixture := loadFixture(t, c.name)
		if len(fixture) != size {
			t.Fatalf("%s: fixture size = %d, want %d", c.name, len(fixture), size)
		}
		d := NewDecoder(fixture)
		if got := d.Bool(0); got != c.core {
			t.Errorf("%s: core = %v, want %v", c.name, got, c.core)
		}
		if got := d.Bool(1); got != c.federate {
			t.Errorf("%s: federate = %v, want %v", c.name, got, c.federate)
		}
		if got := d.Bool(2); got != c.fetch {
			t.Errorf("%s: fetch = %v, want %v", c.name, got, c.fetch)
		}
		if got := d.Bool(3); got != c.eval {
			t.Errorf("%s: eval = %v, want %v", c.name, got, c.eval)
		}
		if got := d.Bool(4); got != c.build {
			t.Errorf("%s: build = %v, want %v", c.name, got, c.build)
		}
		if got := d.Bool(5); got != c.cache {
			t.Errorf("%s: cache = %v, want %v", c.name, got, c.cache)
		}

		a := NewArena()
		w := PrepareStruct(
			Field{0, PrepareBool(a, c.core)},
			Field{1, PrepareBool(a, c.federate)},
			Field{2, PrepareBool(a, c.fetch)},
			Field{3, PrepareBool(a, c.eval)},
			Field{4, PrepareBool(a, c.build)},
			Field{5, PrepareBool(a, c.cache)},
		)
		p := a.Reserve(1, size)
		w(p)
		if got := a.Bytes()[p : p+size]; string(got) != string(fixture) {
			t.Errorf("%s: byte-exact encode mismatch: got % x, want % x", c.name, got, fixture)
		}
	}
}
