package container

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinel is an arbitrary payload standing in for a TIFF blob; the container
// walk carves bytes, it doesn't parse them.
var sentinel = []byte("II\x2a\x00SENTINEL-BLOB")

func readAll(t *testing.T, r io.ReaderAt, n int64) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := r.ReadAt(b, 0)
	require.True(t, err == nil || err == io.EOF, "read blob: %v", err)
	return b
}

func jpegWith(blob []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})       // SOI
	b.Write([]byte{0xFF, 0xE0, 0, 4}) // APP0 (JFIF), len 4, 2 bytes payload
	b.Write([]byte{0, 0})
	payload := append([]byte("Exif\x00\x00"), blob...)
	seg := make([]byte, 2)
	binary.BigEndian.PutUint16(seg, uint16(len(payload)+2))
	b.Write([]byte{0xFF, 0xE1}) // APP1
	b.Write(seg)
	b.Write(payload)
	return b.Bytes()
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

func webpWith(blob []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{0, 0, 0, 0}) // file size (ignored on read)
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

func TestLocate(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"jpeg": jpegWith,
		"png":  pngWith,
		"webp": webpWith,
	}
	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			file := wrap(sentinel)
			blob, size, _, err := Locate(bytes.NewReader(file), int64(len(file)))
			require.NoError(t, err)
			assert.Equal(t, sentinel, readAll(t, blob, size))
		})
	}
}

func TestLocateTIFFWholeFile(t *testing.T) {
	blob, size, _, err := Locate(bytes.NewReader(sentinel), int64(len(sentinel)))
	require.NoError(t, err)
	assert.Equal(t, int64(len(sentinel)), size)
	_ = readAll(t, blob, size)
}

func TestLocateUnknownFormat(t *testing.T) {
	_, _, _, err := Locate(bytes.NewReader([]byte("garbage!")), 8)
	assert.ErrorIs(t, err, ErrUnknownFormat)
}

func TestLocateNoExif(t *testing.T) {
	// JPEG with only SOI + EOI, no APP1.
	file := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	_, _, _, err := Locate(bytes.NewReader(file), int64(len(file)))
	assert.Error(t, err)
}
