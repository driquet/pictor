package tiff

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- allocator unit tests ----------------------------------------------------

func TestAllocatorEOFAppend(t *testing.T) {
	r := require.New(t)
	a := newAllocator() // header [0,8) reserved
	r.EqualValues(8, a.alloc(10), "first alloc")
	r.EqualValues(18, a.alloc(4), "second alloc")
	r.EqualValues(22, a.size())
}

func TestAllocatorAlignment(t *testing.T) {
	r := require.New(t)
	a := newAllocator()
	a.alloc(5) // [8,13), odd end
	r.EqualValues(14, a.alloc(4), "alloc after odd must round to word-aligned")
}

func TestAllocatorGapFit(t *testing.T) {
	r := require.New(t)
	a := newAllocator()
	// Reserve a pinned blob at 40, leaving a gap [8,40).
	r.True(a.reserve(40, 20), "reserve failed")
	r.EqualValues(8, a.alloc(16), "gap-fit alloc")   // fits in the gap before the pin
	r.EqualValues(60, a.alloc(20), "post-pin alloc") // remaining gap [24,40) too small -> after pin
}

func TestAllocatorReserveOverlap(t *testing.T) {
	r := require.New(t)
	a := newAllocator()
	r.False(a.reserve(4, 8), "reserve overlapping header should fail")
	r.True(a.reserve(100, 10), "reserve should succeed")
	r.False(a.reserve(105, 10), "reserve overlapping a pin should fail")
}

// --- tree comparison ---------------------------------------------------------

func sameTree(t *testing.T, a, b *IFD, path string) {
	t.Helper()
	r := require.New(t)
	r.Equal(a == nil, b == nil, "%s: nil mismatch", path)
	if a == nil {
		return
	}
	r.Equal(len(a.Entries), len(b.Entries), "%s: entry count mismatch", path)
	for _, ea := range a.Entries {
		eb, ok := findEntry(b, ea.Tag)
		r.True(ok, "%s: tag 0x%04X missing after round-trip", path, ea.Tag)
		r.Equal(ea.Type, eb.Type, "%s: tag 0x%04X type changed", path, ea.Tag)
		r.EqualValues(ea.Count, eb.Count, "%s: tag 0x%04X count changed", path, ea.Tag)
		r.True(bytes.Equal(ea.Raw, eb.Raw), "%s: tag 0x%04X raw changed: %x vs %x", path, ea.Tag, ea.Raw, eb.Raw)
	}
	r.Equal(len(a.Subs), len(b.Subs), "%s: sub count mismatch", path)
	for id, sa := range a.Subs {
		sameTree(t, sa, b.Subs[id], path+"/sub")
	}
	sameTree(t, a.Next, b.Next, path+"/next")
}

// --- round-trip / idempotence ------------------------------------------------

func TestEncodeSemanticRoundTrip(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(orderName(order), func(t *testing.T) {
			r := require.New(t)
			ifd0 := []entry{
				{0x010F, TypeASCII, 6, ascii("Canon")},
				{0x0112, TypeShort, 1, short(order, 6)},
			}
			exifSub := []entry{
				{0x829A, TypeRational, 1, rat(order, 1, 200)},
				{0x8827, TypeShort, 1, short(order, 400)},
			}
			gps := []entry{{0x0001, TypeASCII, 2, ascii("N")}}
			blob := buildTIFF(order, ifd0, exifSub, gps)

			f1, err := DecodeBytes(blob, exifOpts()...)
			r.NoError(err)
			out, warns, err := f1.Encode()
			r.NoError(err)
			r.Empty(warns, "unexpected warnings")
			f2, err := DecodeBytes(out, exifOpts()...)
			r.NoError(err)
			r.Empty(f2.Errs, "re-decode faults")
			r.Equal(order, f2.Order, "order not preserved")
			sameTree(t, f1.Root, f2.Root, "root")

			// Idempotence: encode -> decode -> encode is byte-stable.
			out2, _, err := f2.Encode()
			r.NoError(err)
			r.True(bytes.Equal(out, out2), "Encode not idempotent (output bytes differ on re-encode)")
		})
	}
}

func TestEncodeEditTag(t *testing.T) {
	r := require.New(t)
	order := binary.LittleEndian
	blob := buildTIFF(order, []entry{{0x0112, TypeShort, 1, short(order, 1)}}, nil, nil)
	f, err := DecodeBytes(blob, exifOpts()...)
	r.NoError(err)
	// Flip Orientation 1 -> 8 in place.
	for i := range f.Root.Entries {
		if f.Root.Entries[i].Tag == 0x0112 {
			f.Root.Entries[i].Raw = short(order, 8)
		}
	}
	out, _, err := f.Encode()
	r.NoError(err)
	f2, _ := DecodeBytes(out, exifOpts()...)
	e, ok := findEntry(f2.Root, 0x0112)
	r.True(ok, "edited Orientation not persisted")
	r.EqualValues(8, order.Uint16(e.Raw))
}

// --- pins --------------------------------------------------------------------

func TestEncodePinHonoured(t *testing.T) {
	r := require.New(t)
	order := binary.LittleEndian
	maker := bytes.Repeat([]byte{0xAB}, 12)
	f := &File{
		Order: order,
		Root: &IFD{
			Entries: []Entry{
				{Tag: 0x0112, Type: TypeShort, Count: 1, Raw: short(order, 1)},
				{Tag: 0x927C, Type: TypeUndefined, Count: 12, Raw: maker, Pinned: true, Opaque: true, valOff: 200},
			},
		},
	}
	out, warns, err := f.Encode()
	r.NoError(err)
	r.Empty(warns, "unexpected warnings")
	r.True(bytes.Equal(out[200:212], maker), "MakerNote not pinned at offset 200: %x", out[200:212])
	f2, _ := DecodeBytes(out)
	e, ok := findEntry(f2.Root, 0x927C)
	r.True(ok && bytes.Equal(e.Raw, maker), "MakerNote not round-tripped: %x", e.Raw)
}

func TestEncodePinDroppedOnConflict(t *testing.T) {
	r := require.New(t)
	order := binary.LittleEndian
	maker := bytes.Repeat([]byte{0xAB}, 12)
	f := &File{
		Order: order,
		Root: &IFD{
			Entries: []Entry{
				{Tag: 0x0112, Type: TypeShort, Count: 1, Raw: short(order, 1)},
				// valOff 4 overlaps the 8-byte header -> can't be honoured.
				{Tag: 0x927C, Type: TypeUndefined, Count: 12, Raw: maker, Pinned: true, valOff: 4},
			},
		},
	}
	out, warns, err := f.Encode()
	r.NoError(err)
	r.Len(warns, 1, "want one WarnDroppedOnRelocate for 0x927C, got %v", warns)
	r.Equal(WarnDroppedOnRelocate, warns[0].Kind)
	r.EqualValues(0x927C, warns[0].Tag)
	f2, _ := DecodeBytes(out)
	_, ok := findEntry(f2.Root, 0x927C)
	r.False(ok, "dropped MakerNote should be absent from output")
}

// --- image data --------------------------------------------------------------

// buildStripTIFF hand-lays a minimal single-strip TIFF: header | IFD0(273,279) |
// pixels. Returns the blob and the pixel bytes.
func buildStripTIFF(order binary.ByteOrder) ([]byte, []byte) {
	pixels := []byte("PIXELS!!")
	const ifd0 = 8
	dir := int(dirBytes(2))
	pixOff := ifd0 + dir
	buf := make([]byte, pixOff+len(pixels))
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:4], 0x002A)
	order.PutUint32(buf[4:8], ifd0)
	order.PutUint16(buf[ifd0:], 2)
	put := func(p, id int, typ Type, count, val uint32) {
		order.PutUint16(buf[p:], uint16(id))
		order.PutUint16(buf[p+2:], uint16(typ))
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
	}
	put(ifd0+2, 273, TypeLong, 1, uint32(pixOff))         // StripOffsets
	put(ifd0+2+12, 279, TypeLong, 1, uint32(len(pixels))) // StripByteCounts
	order.PutUint32(buf[ifd0+2+24:], 0)                   // next IFD
	copy(buf[pixOff:], pixels)
	return buf, pixels
}

func TestEncodeImageDataRelocation(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(orderName(order), func(t *testing.T) {
			r := require.New(t)
			blob, pixels := buildStripTIFF(order)
			f, err := DecodeBytes(blob, WithImageData())
			r.NoError(err)
			r.Empty(f.Errs, "decode faults")
			so, ok := findEntry(f.Root, 273)
			r.True(ok, "StripOffsets missing")
			r.Len(so.imgData, 1, "strip data not dereferenced: %v", so.imgData)
			r.True(bytes.Equal(so.imgData[0], pixels), "strip data not dereferenced: %v", so.imgData)

			out, _, err := f.Encode()
			r.NoError(err)
			f2, err := DecodeBytes(out, WithImageData())
			r.NoError(err)
			so2, ok := findEntry(f2.Root, 273)
			r.True(ok, "StripOffsets missing after re-encode")
			r.Len(so2.imgData, 1, "pixels lost on re-encode: %v", so2.imgData)
			r.True(bytes.Equal(so2.imgData[0], pixels), "pixels lost on re-encode: %v", so2.imgData)
		})
	}
}

// --- SHORT offset overflow -----------------------------------------------------

func TestPlaceImageDataUpgradesShortOffsetOnOverflow(t *testing.T) {
	r := require.New(t)
	e := &encoder{order: binary.LittleEndian, alloc: newAllocator(), plans: map[*IFD]*ifdPlan{}}
	e.alloc.reserve(8, 0x10000-8) // fill everything up to 0x10000

	en := &Entry{Tag: 273, Type: TypeShort, Count: 1, imgData: [][]byte{[]byte("PIXELS!!")}}
	raw, typ := e.placeImageData(en)
	r.Equal(TypeLong, typ)
	r.Len(raw, 4, "raw len, want 4 (one LONG offset)")
	r.GreaterOrEqual(binary.LittleEndian.Uint32(raw), uint32(0x10000))
}

func TestEncodeStripOffsetUpgradesToLongOnOverflow(t *testing.T) {
	r := require.New(t)
	order := binary.LittleEndian
	pixels := []byte("PIXELS!!")
	byteCount := make([]byte, 4)
	order.PutUint32(byteCount, uint32(len(pixels)))
	f := &File{
		Order: order,
		Root: &IFD{
			Entries: []Entry{
				{Tag: 273, Type: TypeShort, Count: 1, imgData: [][]byte{pixels}},
				{Tag: 279, Type: TypeLong, Count: 1, Raw: byteCount},
				// Pin a dummy blob filling the entire gap up to 0x10000, so the
				// allocator has nowhere left below it and is forced to place the
				// pixel data beyond 0xFFFF.
				{Tag: 0x927C, Type: TypeUndefined, Count: 0x10000 - 8, Raw: bytes.Repeat([]byte{0xAB}, 0x10000-8), Pinned: true, valOff: 8},
			},
		},
	}
	out, _, err := f.Encode()
	r.NoError(err)
	f2, err := DecodeBytes(out, WithImageData())
	r.NoError(err)
	so, ok := findEntry(f2.Root, 273)
	r.True(ok, "StripOffsets missing after round-trip")
	r.Equal(TypeLong, so.Type)
	r.Len(so.imgData, 1)
	r.True(bytes.Equal(so.imgData[0], pixels), "pixel data lost/corrupted: %v", so.imgData)
}

// --- structural gate ---------------------------------------------------------

// FuzzEncode round-trips arbitrary input through decode->Encode->decode: Encode
// must never panic and, when the first decode is clean, must produce a blob that
// decodes without structural faults.
func FuzzEncode(f *testing.F) {
	f.Add(buildTIFF(binary.LittleEndian, []entry{{0x0112, TypeShort, 1, short(binary.LittleEndian, 1)}}, nil, nil))
	f.Add(buildTIFF(binary.BigEndian, []entry{{0x010F, TypeASCII, 6, ascii("Canon")}}, nil, nil))
	strip, _ := buildStripTIFF(binary.LittleEndian)
	f.Add(strip)
	f.Fuzz(func(t *testing.T, b []byte) {
		f1, err := DecodeBytes(b, append(exifOpts(), WithImageData())...)
		if err != nil {
			return // bad header; nothing to encode
		}
		out, _, err := f1.Encode()
		if err != nil {
			return // refused (structural fault) - allowed
		}
		f2, err := DecodeBytes(out, append(exifOpts(), WithImageData())...)
		require.NoError(t, err, "re-decode of Encode output failed")
		for _, e := range f2.Errs {
			require.False(t, e.Structural(), "Encode produced a structurally faulty blob: %v", e)
		}
	})
}

func TestEncodeRefusesStructuralFault(t *testing.T) {
	f := &File{
		Order: binary.LittleEndian,
		Root:  &IFD{Entries: []Entry{{Tag: 0x0112, Type: TypeShort, Count: 1, Raw: short(binary.LittleEndian, 1)}}},
		Errs:  []Error{{Code: ErrBadOffset, Tag: 0x0111}},
	}
	_, _, err := f.Encode()
	require.Error(t, err, "expected refusal on structural fault")
}
