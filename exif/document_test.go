package exif

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/driquet/pictor/exif/container"
	"github.com/driquet/pictor/tiff"
)

// Minimal host-format wrappers, mirroring exif/container's own test helpers -
// just enough to round-trip a TIFF blob through Read/Write.

func jpegWith(blob []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	payload := append([]byte("Exif\x00\x00"), blob...)
	seg := make([]byte, 2)
	binary.BigEndian.PutUint16(seg, uint16(len(payload)+2))
	b.Write([]byte{0xFF, 0xE1})
	b.Write(seg)
	b.Write(payload)
	b.Write([]byte{0xFF, 0xD9}) // EOI
	return b.Bytes()
}

func jpegNoExif() []byte {
	return []byte{0xFF, 0xD8, 0xFF, 0xD9} // SOI, EOI
}

func pngWith(blob []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	chunk := func(typ string, data []byte) {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(data)))
		b.Write(l)
		b.WriteString(typ)
		b.Write(data)
		b.Write([]byte{0, 0, 0, 0}) // CRC (ignored on read)
	}
	chunk("IHDR", make([]byte, 13))
	chunk("eXIf", blob)
	chunk("IEND", nil)
	return b.Bytes()
}

func pngNoExif() []byte {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	chunk := func(typ string, data []byte) {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(data)))
		b.Write(l)
		b.WriteString(typ)
		b.Write(data)
		b.Write([]byte{0, 0, 0, 0})
	}
	chunk("IHDR", make([]byte, 13))
	chunk("IEND", nil)
	return b.Bytes()
}

func webpWithVP8X(blob []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString("WEBP")
	b.WriteString("VP8X")
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, 10)
	b.Write(sz)
	b.Write(make([]byte, 10))
	b.WriteString("EXIF")
	binary.LittleEndian.PutUint32(sz, uint32(len(blob)))
	b.Write(sz)
	b.Write(blob)
	if len(blob)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func webpSimpleNoExif() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString("WEBP")
	b.WriteString("VP8 ") // bare lossy chunk, no VP8X
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, 4)
	b.Write(sz)
	b.Write(make([]byte, 4))
	return b.Bytes()
}

// readOK reads b, requiring a clean decode: no fatal error, no best-effort
// faults either.
func readOK(t *testing.T, b []byte) *Document {
	t.Helper()
	doc, err := ReadBytes(b)
	require.NoError(t, err, "read")
	require.Empty(t, doc.Errs(), "unexpected faults")
	return doc
}

// writeBack runs doc.Write, requiring success, and re-reads the result via a
// fresh ReadBytes so assertions exercise the real round-trip, not internal
// state.
func writeBack(t *testing.T, doc *Document) *Document {
	t.Helper()
	var buf bytes.Buffer
	warns, err := doc.Write(&buf)
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")
	return readOK(t, buf.Bytes())
}

func TestReadWriteJPEGInsert(t *testing.T) {
	doc := readOK(t, jpegNoExif())
	require.NoError(t, doc.SetTag(TagMake, "Canon"))
	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "Canon", m.Make)
}

func TestReadWriteJPEGReplace(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, []entry{{tagMake, tiff.TypeASCII, 6, ascii("Nikon")}}, nil, nil)
	doc := readOK(t, jpegWith(blob))
	require.NoError(t, doc.SetTag(TagMake, "Canon"))
	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "Canon", m.Make)
}

func TestReadWritePNGInsert(t *testing.T) {
	doc := readOK(t, pngNoExif())
	require.NoError(t, doc.SetTag(TagModel, "EOSR5"))
	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "EOSR5", m.Model)
}

func TestReadWritePNGReplace(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	doc := readOK(t, pngWith(blob))
	require.NoError(t, doc.SetTag(TagModel, "EOSR5"))
	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "EOSR5", m.Model)
}

func TestReadWriteWebPVP8XReplace(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	doc := readOK(t, webpWithVP8X(blob))
	require.NoError(t, doc.SetTag(TagISO, uint16(200)))
	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 200, *m.ISO)
}

func TestWebPSimpleCannotAddEXIF(t *testing.T) {
	doc := readOK(t, webpSimpleNoExif())
	require.NoError(t, doc.SetTag(TagMake, "Canon"))
	_, err := doc.Write(io.Discard)
	require.ErrorIs(t, err, ErrCannotAddEXIF)
}

func TestReadUnknownFormat(t *testing.T) {
	_, err := ReadBytes([]byte("not an image"))
	require.ErrorIs(t, err, container.ErrUnknownFormat)
}

func TestReadWriteTIFFDispatchesDirectly(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, nil, nil, nil)
	doc := readOK(t, blob)
	require.NoError(t, doc.SetTag(TagMake, "Canon"))
	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "Canon", m.Make)
}

func TestStripAllDeletesJPEGSection(t *testing.T) {
	blob := buildTIFF(binary.LittleEndian, []entry{{tagMake, tiff.TypeASCII, 6, ascii("Canon")}}, nil, nil)
	doc := readOK(t, jpegWith(blob))
	doc.StripAll()

	var buf bytes.Buffer
	warns, err := doc.Write(&buf)
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")

	_, _, loc, lerr := container.Locate(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.ErrorIs(t, lerr, container.ErrNoExif, "EXIF section should have been fully removed")
	assert.False(t, loc.HasEXIF())
}

func TestStripAllOnAbsentEXIFIsNoop(t *testing.T) {
	in := jpegNoExif()
	doc := readOK(t, in)
	doc.StripAll()

	var buf bytes.Buffer
	warns, err := doc.Write(&buf)
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")
	assert.Equal(t, in, buf.Bytes(), "want byte-identical no-op")
}

func TestRemoveTagPrunesGPSSubIFD(t *testing.T) {
	// Latitude is the sub-IFD's only entry, by construction: removing it
	// should empty and prune the GPS sub-IFD entirely, not just the tag. A
	// lone coordinate is an expected, reported fault pre-removal (see
	// TestGPSLonePairFaults) - readOK's zero-faults assumption doesn't apply
	// to this fixture, so Read/Write are called directly here instead.
	o := binary.LittleEndian
	gps := []entry{
		{tagGPSLatitudeRef, tiff.TypeASCII, 2, ascii("N")},
		{tagGPSLatitude, tiff.TypeRational, 3, rats(o, 48, 1, 51, 1, 2999, 100)},
	}
	blob := buildTIFF(o, nil, nil, gps)
	doc, err := ReadBytes(blob)
	require.NoError(t, err)
	require.NoError(t, doc.RemoveTag(TagGPSLatitude))

	var buf bytes.Buffer
	warns, err := doc.Write(&buf)
	require.NoError(t, err, "write")
	require.Empty(t, warns, "unexpected warnings")

	got, err := ReadBytes(buf.Bytes())
	require.NoError(t, err, "read back")
	require.Empty(t, got.Errs(), "read back faults")
	assert.Nil(t, got.Metadata().GPS)
}

func TestRemoveTagLeavesSiblingsUntouched(t *testing.T) {
	ifd0 := []entry{{tagMake, tiff.TypeASCII, 6, ascii("Canon")}, {tagModel, tiff.TypeASCII, 6, ascii("EOSR5")}}
	blob := buildTIFF(binary.LittleEndian, ifd0, nil, nil)
	doc := readOK(t, blob)
	require.NoError(t, doc.RemoveTag(TagMake))

	m := writeBack(t, doc).Metadata()
	assert.Empty(t, m.Make, "want removed")
	assert.Equal(t, "EOSR5", m.Model, "want untouched")
}

func TestSetTagRejectsWrongType(t *testing.T) {
	doc := readOK(t, jpegNoExif())
	err := doc.SetTag(TagISO, "not a uint16")
	require.Error(t, err)
}

func TestSetTagAsciiFields(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagMake, "Canon"))
	require.NoError(t, doc.SetTag(TagModel, "EOSR5"))
	require.NoError(t, doc.SetTag(TagSoftware, "pictor"))
	require.NoError(t, doc.SetTag(TagLensModel, "RF50"))

	m := writeBack(t, doc).Metadata()
	assert.Equal(t, "Canon", m.Make)
	assert.Equal(t, "EOSR5", m.Model)
	assert.Equal(t, "pictor", m.Software)
	assert.Equal(t, "RF50", m.LensModel)
}

func TestSetTagOrientationRoundTrip(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagOrientation, OrientationRightTop))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.Orientation)
	assert.Equal(t, OrientationRightTop, *m.Orientation)
}

func TestSetTagOrientationRejectsOutOfRange(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.Error(t, doc.SetTag(TagOrientation, Orientation(9)))
}

func TestSetTagISORoundTrip(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagISO, uint16(400)))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 400, *m.ISO)
}

func TestSetTagDateTimeRoundTrip(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	tm, err := time.Parse(dateTimeLayout, "2021:07:15 12:30:45")
	require.NoError(t, err)
	require.NoError(t, doc.SetTag(TagDateTime, tm))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.DateTime)
	assert.Equal(t, 12, m.DateTime.Hour())
}

func TestSetTagRationalFields(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagExposureTime, tiff.Rational{Num: 1, Denom: 200}))
	require.NoError(t, doc.SetTag(TagFNumber, tiff.Rational{Num: 28, Denom: 10}))
	require.NoError(t, doc.SetTag(TagFocalLength, tiff.Rational{Num: 50, Denom: 1}))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.ExposureTime)
	assert.EqualValues(t, 1, m.ExposureTime.Num)
	assert.EqualValues(t, 200, m.ExposureTime.Denom)
	require.NotNil(t, m.FNumber)
	assert.Equal(t, 2.8, *m.FNumber)
	require.NotNil(t, m.FocalLength)
	assert.Equal(t, float64(50), *m.FocalLength)
}

func TestSetTagLongFields(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagPixelXDimension, uint32(6000)))
	require.NoError(t, doc.SetTag(TagPixelYDimension, uint32(4000)))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.PixelXDimension)
	assert.EqualValues(t, 6000, *m.PixelXDimension)
	require.NotNil(t, m.PixelYDimension)
	assert.EqualValues(t, 4000, *m.PixelYDimension)
}

func TestSetTagGPSCoordRoundTrip(t *testing.T) {
	doc := readOK(t, jpegNoExif())
	require.NoError(t, doc.SetTag(TagGPSLatitude, 48.858222))
	require.NoError(t, doc.SetTag(TagGPSLongitude, -2.294444))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.GPS)
	assert.InDelta(t, 48.858222, m.GPS.Latitude, 1e-4)
	assert.Negative(t, m.GPS.Longitude, "want negative (W)")
}

func TestSetTagGPSAltitudeRoundTrip(t *testing.T) {
	doc := readOK(t, buildTIFF(binary.LittleEndian, nil, nil, nil))
	require.NoError(t, doc.SetTag(TagGPSAltitude, -12.5))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.GPS)
	require.NotNil(t, m.GPS.Altitude)
	assert.Equal(t, -12.5, *m.GPS.Altitude)
}

func TestSetTagCreatesSubIFDsOnDemand(t *testing.T) {
	// No Exif/GPS sub-IFD present at all in the source blob.
	blob := buildTIFF(binary.LittleEndian, []entry{{tagMake, tiff.TypeASCII, 3, ascii("X")}}, nil, nil)
	doc := readOK(t, blob)
	require.NoError(t, doc.SetTag(TagISO, uint16(200)))
	require.NoError(t, doc.SetTag(TagGPSAltitude, 10.0))

	m := writeBack(t, doc).Metadata()
	require.NotNil(t, m.ISO)
	assert.EqualValues(t, 200, *m.ISO)
	require.NotNil(t, m.GPS)
	require.NotNil(t, m.GPS.Altitude)
	assert.Equal(t, float64(10), *m.GPS.Altitude)
}

// TestWriteRefusesStructuralFault hand-builds a TIFF where IFD0's offset
// points past EOF - a structural fault tiff.Decode records in f.Errs (surfaced
// here via doc.Errs()) rather than failing outright. Encode must still refuse
// to write a source it can't faithfully round-trip.
func TestWriteRefusesStructuralFault(t *testing.T) {
	blob := []byte{'I', 'I', 0x2A, 0x00, 0xFF, 0xFF, 0x00, 0x00}
	doc, err := ReadBytes(blob)
	require.NoError(t, err, "decode itself is best-effort, not fatal")
	require.NotEmpty(t, doc.Errs(), "want a structural fault recorded")

	_, werr := doc.Write(io.Discard)
	require.Error(t, werr, "want error refusing to write a structurally faulted source")
}

// TestWritePropagatesDroppedRelocateWarning hand-builds a TIFF where the
// MakerNote's out-of-line value offset (4) overlaps the reserved 8-byte
// header span, forcing Encode to drop it. Write must surface that warning
// unwrapped rather than swallowing it.
func TestWritePropagatesDroppedRelocateWarning(t *testing.T) {
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

	doc := readOK(t, buf)
	require.NoError(t, doc.SetTag(TagMake, "Canon"))

	var out bytes.Buffer
	warns, err := doc.Write(&out)
	require.NoError(t, err, "write")
	require.Len(t, warns, 1, "want one WarnDroppedOnRelocate@MakerNote")
	assert.Equal(t, tiff.WarnDroppedOnRelocate, warns[0].Kind)
	assert.Equal(t, tagMakerNote, warns[0].Tag)

	m := readOK(t, out.Bytes()).Metadata()
	assert.Equal(t, "Canon", m.Make, "want Canon despite the dropped MakerNote")
}

func TestDocumentTagsReflectsMutation(t *testing.T) {
	doc := readOK(t, jpegNoExif())
	require.NoError(t, doc.SetTag(TagMake, "Canon"))

	var got Tag
	found := false
	for _, tg := range doc.Tags() {
		if tg.Name == TagMake {
			got, found = tg, true
		}
	}
	require.True(t, found, "Make should show up in Tags() right after SetTag, before Write")
	assert.Equal(t, "Canon", got.Value)
}
