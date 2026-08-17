package exif

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/driquet/pictor/tiff"
)

// --- synthetic TIFF/EXIF builder -----------------------------------------

type entry struct {
	id   uint16
	typ  tiff.Type
	cnt  uint32
	data []byte
}

func dirSize(n int) int { return 2 + 12*n + 4 }

func buildTIFF(o binary.ByteOrder, ifd0, exifSub, gps []entry) []byte {
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
	if o == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	o.PutUint16(buf[2:4], 0x002A)
	o.PutUint32(buf[4:8], uint32(offIFD0))

	pool := offPool
	writeDir := func(off int, entries, extra []entry) {
		all := append(append([]entry{}, entries...), extra...)
		o.PutUint16(buf[off:], uint16(len(all)))
		p := off + 2
		for _, e := range all {
			o.PutUint16(buf[p:], e.id)
			o.PutUint16(buf[p+2:], uint16(e.typ))
			o.PutUint32(buf[p+4:], e.cnt)
			if len(e.data) <= 4 {
				copy(buf[p+8:p+12], e.data)
			} else {
				o.PutUint32(buf[p+8:], uint32(pool))
				copy(buf[pool:], e.data)
				pool += len(e.data)
			}
			p += 12
		}
		o.PutUint32(buf[p:], 0)
	}
	var ptrs []entry
	if exifSub != nil {
		ptrs = append(ptrs, ptrEntry(o, tagExifIFD, offExif))
	}
	if gps != nil {
		ptrs = append(ptrs, ptrEntry(o, tagGPSIFD, offGPS))
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

func ptrEntry(o binary.ByteOrder, id uint16, off int) entry {
	b := make([]byte, 4)
	o.PutUint32(b, uint32(off))
	return entry{id, tiff.TypeLong, 1, b}
}
func concat(gs ...[]entry) []entry {
	var out []entry
	for _, g := range gs {
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
func long(o binary.ByteOrder, v uint32) []byte {
	b := make([]byte, 4)
	o.PutUint32(b, v)
	return b
}
func rat(o binary.ByteOrder, n, d uint32) []byte {
	b := make([]byte, 8)
	o.PutUint32(b[0:], n)
	o.PutUint32(b[4:], d)
	return b
}
func rats(o binary.ByteOrder, nd ...uint32) []byte {
	var out []byte
	for i := 0; i+1 < len(nd); i += 2 {
		out = append(out, rat(o, nd[i], nd[i+1])...)
	}
	return out
}
func orderName(o binary.ByteOrder) string {
	if o == binary.BigEndian {
		return "BE"
	}
	return "LE"
}

// --- tests -----------------------------------------------------------------

func TestReadTIFFGolden(t *testing.T) {
	for _, o := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(orderName(o), func(t *testing.T) {
			ifd0 := []entry{
				{tagMake, tiff.TypeASCII, 6, ascii("Canon")},
				{tagModel, tiff.TypeASCII, 6, ascii("EOSR5")},
				{tagOrientation, tiff.TypeShort, 1, short(o, 6)},
				{tagDateTime, tiff.TypeASCII, 20, ascii("2021:07:15 12:30:45")},
			}
			exifSub := []entry{
				{tagExposureTime, tiff.TypeRational, 1, rat(o, 1, 200)},
				{tagFNumber, tiff.TypeRational, 1, rat(o, 28, 10)},
				{tagISO, tiff.TypeShort, 1, short(o, 400)},
				{tagFocalLength, tiff.TypeRational, 1, rat(o, 50, 1)},
				{tagPixelXDimension, tiff.TypeLong, 1, long(o, 6000)},
				{tagPixelYDimension, tiff.TypeShort, 1, short(o, 4000)},
				{tagLensModel, tiff.TypeASCII, 8, ascii("RF50 F1")},
				{tagDateTimeOriginal, tiff.TypeASCII, 20, ascii("2021:07:15 12:30:45")},
				{tagOffsetTimeOriginal, tiff.TypeASCII, 7, ascii("+02:00")},
			}
			gps := []entry{
				{tagGPSLatitudeRef, tiff.TypeASCII, 2, ascii("N")},
				{tagGPSLatitude, tiff.TypeRational, 3, rats(o, 48, 1, 51, 1, 2999, 100)},
				{tagGPSLongitudeRef, tiff.TypeASCII, 2, ascii("W")},
				{tagGPSLongitude, tiff.TypeRational, 3, rats(o, 2, 1, 20, 1, 1000, 100)},
				{tagGPSAltitudeRef, tiff.TypeByte, 1, []byte{0}},
				{tagGPSAltitude, tiff.TypeRational, 1, rat(o, 12345, 100)},
			}
			blob := buildTIFF(o, ifd0, exifSub, gps)

			m, errs := ReadTIFFBytes(blob)
			require.Empty(t, errs, "unexpected faults")

			assert.Equal(t, "Canon", m.Make)
			assert.Equal(t, "EOSR5", m.Model)
			if assert.NotNil(t, m.Orientation) {
				assert.Equal(t, OrientationRightTop, *m.Orientation)
			}
			if assert.NotNil(t, m.DateTime) {
				assert.Equal(t, 12, m.DateTime.Hour())
			}
			require.NotNil(t, m.DateTimeOriginal)
			_, off := m.DateTimeOriginal.Zone()
			assert.Equal(t, 2*3600, off)
			if assert.NotNil(t, m.ExposureTime) {
				assert.EqualValues(t, 1, m.ExposureTime.Num)
				assert.EqualValues(t, 200, m.ExposureTime.Denom)
			}
			if assert.NotNil(t, m.FNumber) {
				assert.Equal(t, 2.8, *m.FNumber)
			}
			if assert.NotNil(t, m.FocalLength) {
				assert.Equal(t, float64(50), *m.FocalLength)
			}
			if assert.NotNil(t, m.ISO) {
				assert.EqualValues(t, 400, *m.ISO)
			}
			if assert.NotNil(t, m.PixelXDimension) {
				assert.EqualValues(t, 6000, *m.PixelXDimension)
			}
			if assert.NotNil(t, m.PixelYDimension) {
				assert.EqualValues(t, 4000, *m.PixelYDimension)
			}
			assert.Equal(t, "RF50 F1", m.LensModel)

			require.NotNil(t, m.GPS)
			wantLat := 48 + 51.0/60 + 29.99/3600
			assert.InDelta(t, wantLat, m.GPS.Latitude, 1e-6)
			assert.Negative(t, m.GPS.Longitude, "W => negative")
			if assert.NotNil(t, m.GPS.Altitude) {
				assert.InDelta(t, 123.45, *m.GPS.Altitude, 1e-6)
			}
		})
	}
}

func TestReadTIFFEmptyIsClean(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, []entry{{tagOrientation, tiff.TypeShort, 1, short(binary.LittleEndian, 1)}}, nil, nil)
	m, errs := ReadTIFFBytes(blob)
	require.Empty(t, errs, "faults")
	assert.Empty(t, m.Make, "absent tags should stay zero/nil without error")
	assert.Nil(t, m.GPS)
	assert.Nil(t, m.ISO)
}

func TestExtractSkipsUnknownAndMalformed(t *testing.T) {
	o := binary.LittleEndian
	blob := buildTIFF(o, []entry{
		{tagOrientation, tiff.TypeShort, 1, short(o, 99)}, // out of range 1..8 -> fault
		{0x9999, tiff.TypeShort, 1, short(o, 1)},          // unknown -> silently skipped
	}, nil, nil)

	f, _ := tiff.DecodeBytes(blob, subIFDOpt())
	tags, errs := Extract(f)
	for _, tg := range tags {
		assert.NotEqual(t, uint16(0x9999), tg.ID, "unknown tag should not be extracted")
		assert.NotEqual(t, "Orientation", tg.Name, "malformed orientation should not be extracted")
	}
	require.Len(t, errs, 1, "want 1 fault (bad orientation)")
	e, ok := errs[0].(Error)
	require.True(t, ok, "fault should be an exif.Error")
	assert.Equal(t, tiff.ErrBadValue, e.Code)
}

func TestGPSLonePairFaults(t *testing.T) {
	o := binary.LittleEndian
	// Latitude only, longitude missing.
	gps := []entry{
		{tagGPSLatitudeRef, tiff.TypeASCII, 2, ascii("N")},
		{tagGPSLatitude, tiff.TypeRational, 3, rats(o, 48, 1, 0, 1, 0, 1)},
	}
	blob := buildTIFF(o, []entry{{tagMake, tiff.TypeASCII, 3, ascii("X")}}, nil, gps)

	m, errs := ReadTIFFBytes(blob)
	assert.Nil(t, m.GPS, "lone latitude must not yield GPS")

	var found bool
	for _, e := range errs {
		if ex, ok := e.(Error); ok && ex.Tag == tagGPSLatitude {
			found = true
		}
	}
	assert.True(t, found, "expected a lone-coordinate fault, got %v", errs)
}
