package exif

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/driquet/pictor/tiff"
)

// readBack re-encodes via SetTIFFBytes/StripTIFFBytes and decodes the result
// with ReadTIFFBytes, failing the test on any error/fault.
func readBack(t *testing.T, out []byte, warns []tiff.Warning, err error) *Metadata {
	t.Helper()
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")
	m, errs := ReadTIFFBytes(out)
	require.Empty(t, errs, "read back")
	return m
}

func TestSetAsciiFields(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"Make=Canon", "Model=EOSR5", "Software=pictor", "LensModel=RF50"})
	m := readBack(t, out, warns, err)
	assert.Equal(t, "Canon", m.Make)
	assert.Equal(t, "EOSR5", m.Model)
	assert.Equal(t, "pictor", m.Software)
	assert.Equal(t, "RF50", m.LensModel)
}

func TestSetOrientationRoundTrip(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"Orientation=6"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.Orientation)
	assert.Equal(t, OrientationRightTop, *m.Orientation)
}

func TestSetISORoundTrip(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"ISO=400"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 400, *m.ISO)
}

func TestSetDateTimeRoundTrip(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"DateTime=2021:07:15 12:30:45"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.DateTime)
	assert.Equal(t, 12, m.DateTime.Hour())
}

func TestSetDateTimeRejectsBadFormat(t *testing.T) {
	_, err := Set([]string{"DateTime=not-a-date"})
	require.Error(t, err, "want error for malformed datetime")
}

func TestSetRationalFields(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"ExposureTime=1/200", "FNumber=28/10", "FocalLength=50"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.ExposureTime)
	assert.EqualValues(t, 1, m.ExposureTime.Num)
	assert.EqualValues(t, 200, m.ExposureTime.Denom)
	require.NotNil(t, m.FNumber)
	assert.Equal(t, 2.8, *m.FNumber)
	require.NotNil(t, m.FocalLength)
	assert.Equal(t, float64(50), *m.FocalLength)
}

func TestSetRationalRejectsBareDecimal(t *testing.T) {
	_, err := Set([]string{"FNumber=2.8"})
	require.Error(t, err, "want error for bare decimal, expected N/D form")
}

func TestSetLongFields(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"PixelXDimension=6000", "PixelYDimension=4000"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.PixelXDimension)
	assert.EqualValues(t, 6000, *m.PixelXDimension)
	require.NotNil(t, m.PixelYDimension)
	assert.EqualValues(t, 4000, *m.PixelYDimension)
}

func TestSetGPSCoordRoundTrip(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"GPSLatitude=48.858222", "GPSLongitude=-2.294444"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.GPS)
	assert.InDelta(t, 48.858222, m.GPS.Latitude, 1e-4)
	assert.Negative(t, m.GPS.Longitude, "want negative (W)")
}

func TestSetGPSAltitudeRoundTrip(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"GPSAltitude=-12.5"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.GPS)
	require.NotNil(t, m.GPS.Altitude)
	assert.Equal(t, -12.5, *m.GPS.Altitude)
}

func TestSetCreatesSubIFDsOnDemand(t *testing.T) {
	// No Exif/GPS sub-IFD present at all in the source blob.
	blob := buildTIFF(binary.LittleEndian, []entry{{tagMake, tiff.TypeASCII, 3, ascii("X")}}, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"ISO=200", "GPSAltitude=10"})
	m := readBack(t, out, warns, err)
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 200, *m.ISO)
	require.NotNil(t, m.GPS)
	require.NotNil(t, m.GPS.Altitude)
	assert.Equal(t, float64(10), *m.GPS.Altitude)
}

func TestStripNamedTagRemovesOnlyThat(t *testing.T) {
	o := binary.LittleEndian
	ifd0 := []entry{{tagMake, tiff.TypeASCII, 6, ascii("Canon")}, {tagModel, tiff.TypeASCII, 6, ascii("EOSR5")}}
	blob := buildTIFF(o, ifd0, nil, nil)
	out, warns, err := StripTIFFBytes(blob, []string{"Make"})
	m := readBack(t, out, warns, err)
	assert.Empty(t, m.Make, "want stripped")
	assert.Equal(t, "EOSR5", m.Model, "want untouched")
}

func TestStripGPSNamePrunesSubIFD(t *testing.T) {
	o := binary.LittleEndian
	gps := []entry{
		{tagGPSLatitudeRef, tiff.TypeASCII, 2, ascii("N")},
		{tagGPSLatitude, tiff.TypeRational, 3, rats(o, 48, 1, 51, 1, 2999, 100)},
	}
	blob := buildTIFF(o, nil, nil, gps)
	out, warns, err := StripTIFFBytes(blob, []string{"GPSLatitude"})
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")
	f, perr := tiff.DecodeBytes(out, subIFDOpt())
	require.NoError(t, perr, "parse")
	assert.Nil(t, f.Root.Subs[tagGPSIFD], "GPS sub-IFD should have been pruned once empty")
}

func TestStripAllShrinksTreeToZero(t *testing.T) {
	o := binary.LittleEndian
	ifd0 := []entry{
		{tagMake, tiff.TypeASCII, 6, ascii("Canon")},
		{0x9999, tiff.TypeShort, 1, short(o, 1)}, // unrecognized tag: still wiped
	}
	exifSub := []entry{{tagISO, tiff.TypeShort, 1, short(o, 400)}}
	blob := buildTIFF(o, ifd0, exifSub, nil)

	out, warns, err := StripTIFFBytes(blob, nil)
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")
	f, perr := tiff.DecodeBytes(out, subIFDOpt())
	require.NoError(t, perr, "parse")
	assert.Empty(t, f.Root.Entries)
	assert.Empty(t, f.Root.Subs)
	assert.Nil(t, f.Root.Next)
}

func TestSetOnStructuralFaultRefuses(t *testing.T) {
	// Truncated buffer: valid header, IFD0 offset points past EOF -> structural fault.
	blob := []byte{'I', 'I', 0x2A, 0x00, 0xFF, 0xFF, 0x00, 0x00}
	_, _, err := SetTIFFBytes(blob, []string{"Make=Canon"})
	require.Error(t, err, "want error refusing to write a structurally faulted source")
}

func TestSetMultipleAssignmentsAllApply(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	out, warns, err := SetTIFFBytes(blob, []string{"Make=Canon", "Model=EOSR5", "ISO=100"})
	m := readBack(t, out, warns, err)
	assert.Equal(t, "Canon", m.Make)
	assert.Equal(t, "EOSR5", m.Model)
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 100, *m.ISO)
}

// TestSetPropagatesDroppedRelocateWarning hand-builds a TIFF where the
// MakerNote's out-of-line value offset (4) overlaps the reserved 8-byte
// header span, forcing Encode to drop it. The facade must surface that
// warning unwrapped rather than swallowing it.
func TestSetPropagatesDroppedRelocateWarning(t *testing.T) {
	o := binary.LittleEndian
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:4], 0x002A)
	o.PutUint32(buf[4:8], 8) // IFD0 offset
	o.PutUint16(buf[8:10], 1)
	o.PutUint16(buf[10:12], tagMakerNote)
	o.PutUint16(buf[12:14], uint16(tiff.TypeUndefined))
	o.PutUint32(buf[14:18], 8) // count: 8 bytes, out-of-line
	o.PutUint32(buf[18:22], 4) // value offset overlaps the [0,8) header span
	o.PutUint32(buf[22:26], 0) // next-IFD = end of chain

	out, warns, err := SetTIFFBytes(buf, []string{"Make=Canon"})
	require.NoError(t, err, "write")
	require.Len(t, warns, 1, "want one WarnDroppedOnRelocate@MakerNote")
	assert.Equal(t, tiff.WarnDroppedOnRelocate, warns[0].Kind)
	assert.Equal(t, tagMakerNote, warns[0].Tag)

	m, errs := ReadTIFFBytes(out)
	require.Empty(t, errs, "read back")
	assert.Equal(t, "Canon", m.Make, "want Canon despite the dropped MakerNote")
}

func TestIsTagName(t *testing.T) {
	assert.True(t, IsTagName("Make"), "Make should be a known tag name")
	assert.False(t, IsTagName("not-a-tag"), "unknown name should not be a tag name")
	assert.False(t, IsTagName(strings.ToLower("Make")), "tag names are case-sensitive")
}
