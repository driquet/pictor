package tiff

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EXIF pointer tags, defined in the TEST, not the package: tiff stays ignorant
// of what they mean. The caller declares them via WithSubIFDTags.
const (
	tagExifSubIFD uint16 = 0x8769
	tagGPSIFD     uint16 = 0x8825
	tagInteropIFD uint16 = 0xA005
)

func exifOpts() []Option { return []Option{WithSubIFDTags(tagExifSubIFD, tagGPSIFD, tagInteropIFD)} }

// --- synthetic TIFF/EXIF builder ---------
//
// Lays out: header(8) | IFD0 | [ExifIFD] | [GPSIFD] | value-pool. Values >4
// bytes land in the pool; <=4 bytes inline. Sub-IFD pointer entries are
// auto-added. Golden values are known by construction.

type entry struct {
	id    uint16
	typ   Type
	count uint32
	data  []byte // full value bytes, already encoded in `order`
}

func dirSize(n int) int { return 2 + 12*n + 4 }

func buildTIFF(order binary.ByteOrder, ifd0, exifSub, gps []entry) []byte {
	ifd0Count := len(ifd0)
	if exifSub != nil {
		ifd0Count++
	}
	if gps != nil {
		ifd0Count++
	}

	offIFD0 := 8
	offExif := offIFD0 + dirSize(ifd0Count)
	offGPS := offExif
	if exifSub != nil {
		offGPS += dirSize(len(exifSub))
	}
	offPool := offGPS
	if gps != nil {
		offPool += dirSize(len(gps))
	}

	total := offPool
	for _, e := range concat(ifd0, exifSub, gps) {
		if len(e.data) > 4 {
			total += len(e.data)
		}
	}
	buf := make([]byte, total)

	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:4], 0x002A)
	order.PutUint32(buf[4:8], uint32(offIFD0))

	pool := offPool
	writeDir := func(off int, entries, extra []entry) {
		all := append(append([]entry{}, entries...), extra...)
		order.PutUint16(buf[off:], uint16(len(all)))
		p := off + 2
		for _, e := range all {
			order.PutUint16(buf[p:], e.id)
			order.PutUint16(buf[p+2:], uint16(e.typ))
			order.PutUint32(buf[p+4:], e.count)
			if len(e.data) <= 4 {
				copy(buf[p+8:p+12], e.data)
			} else {
				order.PutUint32(buf[p+8:], uint32(pool))
				copy(buf[pool:], e.data)
				pool += len(e.data)
			}
			p += 12
		}
		order.PutUint32(buf[p:], 0) // next-IFD = none
	}

	var ptrs []entry
	if exifSub != nil {
		ptrs = append(ptrs, ptrEntry(order, tagExifSubIFD, offExif))
	}
	if gps != nil {
		ptrs = append(ptrs, ptrEntry(order, tagGPSIFD, offGPS))
	}
	writeDir(offIFD0, ifd0, ptrs)
	if exifSub != nil {
		writeDir(offExif, exifSub, nil)
	}
	if gps != nil {
		writeDir(offGPS, gps, nil)
	}
	return buf
}

func ptrEntry(order binary.ByteOrder, id uint16, off int) entry {
	b := make([]byte, 4)
	order.PutUint32(b, uint32(off))
	return entry{id: id, typ: TypeLong, count: 1, data: b}
}

func concat(groups ...[]entry) []entry {
	var out []entry
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func ascii(s string) []byte { return append([]byte(s), 0) }
func short(o binary.ByteOrder, v uint16) []byte {
	b := make([]byte, 2)
	o.PutUint16(b, v)
	return b
}

func rat(o binary.ByteOrder, num, den uint32) []byte {
	b := make([]byte, 8)
	o.PutUint32(b[0:], num)
	o.PutUint32(b[4:], den)
	return b
}

// --- helpers -----------------------------------------------------------------

func findEntry(ifd *IFD, id uint16) (Entry, bool) {
	for _, e := range ifd.Entries {
		if e.Tag == id {
			return e, true
		}
	}
	return Entry{}, false
}

func orderName(o binary.ByteOrder) string {
	if o == binary.BigEndian {
		return "BE"
	}
	return "LE"
}

// --- tests -------------------------------------------------------------------

func TestDecodeRoundTrip(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(orderName(order), func(t *testing.T) {
			ifd0 := []entry{
				{0x010F, TypeASCII, 6, ascii("Canon")},  // Make, out-of-line (6 > 4)
				{0x0112, TypeShort, 1, short(order, 6)}, // Orientation, inline
			}
			exifSub := []entry{
				{0x829A, TypeRational, 1, rat(order, 1, 200)}, // ExposureTime, out-of-line
				{0x8827, TypeShort, 1, short(order, 400)},     // ISO, inline
			}
			gps := []entry{
				{0x0001, TypeASCII, 2, ascii("N")}, // GPSLatitudeRef, inline
			}
			blob := buildTIFF(order, ifd0, exifSub, gps)

			f, err := DecodeBytes(blob, exifOpts()...)
			require.NoError(t, err)
			require.Empty(t, f.Errs, "unexpected faults")
			assert.Equal(t, order, f.Order)

			// Sub-IFD pointer entries lifted OUT of Entries.
			_, ok := findEntry(f.Root, tagExifSubIFD)
			assert.False(t, ok, "Exif pointer entry should be lifted out of Entries")
			assert.Len(t, f.Root.Entries, 2, "IFD0 Entries")

			// Out-of-line ASCII value captured verbatim.
			e, ok := findEntry(f.Root, 0x010F)
			require.True(t, ok, "Make missing")
			assert.Equal(t, "Canon\x00", string(e.Raw))

			// Inline SHORT value.
			e, ok = findEntry(f.Root, 0x0112)
			require.True(t, ok, "Orientation missing")
			assert.EqualValues(t, 6, order.Uint16(e.Raw))

			// Exif sub-IFD present with its tags.
			exif := f.Root.Subs[tagExifSubIFD]
			require.NotNil(t, exif, "Exif sub-IFD missing")
			e, ok = findEntry(exif, 0x829A)
			require.True(t, ok, "ExposureTime missing")
			num, den := order.Uint32(e.Raw[0:]), order.Uint32(e.Raw[4:])
			assert.EqualValues(t, 1, num, "ExposureTime numerator")
			assert.EqualValues(t, 200, den, "ExposureTime denominator")

			// GPS sub-IFD present.
			assert.NotNil(t, f.Root.Subs[tagGPSIFD], "GPS sub-IFD missing")
		})
	}
}

func TestDecodeUndeclaredPointerStaysData(t *testing.T) {
	// Without WithSubIFDTags, the Exif pointer is just an ordinary LONG entry;
	// tiff has no EXIF knowledge of its own.
	order := binary.LittleEndian
	blob := buildTIFF(order, []entry{{0x0112, TypeShort, 1, short(order, 1)}},
		[]entry{{0x8827, TypeShort, 1, short(order, 100)}}, nil)

	f, err := DecodeBytes(blob) // no options
	require.NoError(t, err)
	assert.Nil(t, f.Root.Subs[tagExifSubIFD], "Exif recursed without being declared")
	_, ok := findEntry(f.Root, tagExifSubIFD)
	assert.True(t, ok, "undeclared pointer should remain a data entry")
}

func TestDecodeBigTIFFRejected(t *testing.T) {
	blob := []byte{'I', 'I', 0x2B, 0x00, 8, 0, 0, 0} // magic 43
	_, err := DecodeBytes(blob)
	require.Error(t, err, "expected BigTIFF rejection")
}

func TestDecodeBadHeader(t *testing.T) {
	for _, tc := range [][]byte{
		{},                                 // empty
		{'X', 'Y', 0x2A, 0x00, 8, 0, 0, 0}, // bad byte-order mark
		{'I', 'I', 0x00, 0x00, 8, 0, 0, 0}, // bad magic
		{'I', 'I', 0x2A},                   // too short
	} {
		_, err := DecodeBytes(tc)
		require.Error(t, err, "expected header error for %v", tc)
	}
}

func TestDecodeTruncatedIsBestEffort(t *testing.T) {
	order := binary.LittleEndian
	blob := buildTIFF(order, []entry{
		{0x010F, TypeASCII, 6, ascii("Canon")}, // out-of-line value in the pool
		{0x0112, TypeShort, 1, short(order, 1)},
	}, nil, nil)
	cut := blob[:len(blob)-3] // chop the tail of the value pool

	f, err := DecodeBytes(cut)
	require.NoError(t, err, "truncation must not be fatal")
	require.NotNil(t, f.Root, "valid header must still yield a tree")
	var structural bool
	for _, e := range f.Errs {
		if e.Structural() {
			structural = true
		}
	}
	assert.True(t, structural, "expected a structural fault for the out-of-range value")
}

func TestDecodeCycleGuarded(t *testing.T) {
	// IFD0 whose next-IFD points back at itself (offset 8).
	order := binary.LittleEndian
	buf := make([]byte, 8+dirSize(1))
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:4], 0x002A)
	order.PutUint32(buf[4:8], 8)
	order.PutUint16(buf[8:], 1) // one entry
	order.PutUint16(buf[10:], 0x0112)
	order.PutUint16(buf[12:], uint16(TypeShort))
	order.PutUint32(buf[14:], 1)
	order.PutUint16(buf[18:], 1)
	order.PutUint32(buf[8+2+12:], 8) // next-IFD -> 8 (self)

	f, err := DecodeBytes(buf)
	require.NoError(t, err)
	var cycle bool
	for _, e := range f.Errs {
		if e.Code == ErrCycle {
			cycle = true
		}
	}
	assert.True(t, cycle, "expected ErrCycle")
}

func TestDecodeByteOffsetBase(t *testing.T) {
	order := binary.LittleEndian
	blob := buildTIFF(order, []entry{{0x0112, TypeShort, 1, short(order, 3)}}, nil, nil)
	prefix := []byte("Exif\x00\x00")
	shifted := append(append([]byte{}, prefix...), blob...)

	f, err := DecodeBytes(shifted, WithByteOffsetBase(int64(len(prefix))))
	require.NoError(t, err)
	e, ok := findEntry(f.Root, 0x0112)
	require.True(t, ok, "Orientation not parsed through offset base")
	assert.EqualValues(t, 3, order.Uint16(e.Raw))
}

func FuzzDecodeBytes(f *testing.F) {
	f.Add(buildTIFF(binary.LittleEndian, []entry{{0x0112, TypeShort, 1, short(binary.LittleEndian, 1)}}, nil, nil))
	f.Add(buildTIFF(binary.BigEndian, []entry{{0x010F, TypeASCII, 6, ascii("Canon")}}, nil, nil))
	f.Add([]byte{'I', 'I', 0x2A, 0x00, 8, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		// Must never panic; a valid header must yield a non-nil tree.
		f2, err := DecodeBytes(b, exifOpts()...)
		if err == nil {
			require.NotNil(t, f2.Root, "valid header but nil Root")
		}
	})
}
