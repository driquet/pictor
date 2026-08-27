package container

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBlob is a distinct sentinel TIFF blob to inject (starts with a valid TIFF
// header so nothing downstream chokes on the magic).
var newBlob = []byte("II\x2a\x00NEW-EXIF-BLOB-DATA")

func jpegNoExif() []byte {
	return []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xE0, 0, 4, 0, 0, // APP0 (JFIF), 2-byte payload
		0xFF, 0xD9, // EOI
	}
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

func webpNoExif() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	b.Write([]byte{0, 0, 0, 0})
	b.WriteString("WEBP")
	b.WriteString("VP8X")
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, 10)
	b.Write(sz)
	b.Write(make([]byte, 10)) // flags byte 0 => no EXIF
	return b.Bytes()
}

// inject runs Inject over file and returns the output bytes.
func inject(t *testing.T, file []byte, blob []byte) []byte {
	t.Helper()
	_, _, loc, err := Locate(bytes.NewReader(file), int64(len(file)))
	// ErrNoExif is fine; loc still carries the insert point.
	require.True(t, err == nil || err == ErrNoExif, "Locate: %v", err)
	var out bytes.Buffer
	require.NoError(t, Inject(&out, bytes.NewReader(file), int64(len(file)), loc, blob))
	return out.Bytes()
}

// locatedBlob re-locates file and returns the EXIF blob, or nil on ErrNoExif.
func locatedBlob(t *testing.T, file []byte) []byte {
	t.Helper()
	blob, size, _, err := Locate(bytes.NewReader(file), int64(len(file)))
	if err == ErrNoExif {
		return nil
	}
	require.NoError(t, err, "re-Locate")
	b := make([]byte, size)
	_, err = blob.ReadAt(b, 0)
	require.NoError(t, err, "read located blob")
	return b
}

func TestInject(t *testing.T) {
	withExif := map[string]func([]byte) []byte{
		"jpeg": jpegWith, "png": pngWith, "webp": webpWith,
	}
	noExif := map[string]func() []byte{
		"jpeg": jpegNoExif, "png": pngNoExif, "webp": webpNoExif,
	}

	for name := range withExif {
		t.Run(name+"/replace", func(t *testing.T) {
			out := inject(t, withExif[name](sentinel), newBlob)
			assert.Equal(t, newBlob, locatedBlob(t, out))
		})
		t.Run(name+"/delete", func(t *testing.T) {
			out := inject(t, withExif[name](sentinel), nil)
			assert.Nil(t, locatedBlob(t, out), "EXIF still present after delete")
			_, err := detectFormat(bytes.NewReader(out))
			assert.NoError(t, err, "output no longer a valid %s", name)
		})
		t.Run(name+"/insert", func(t *testing.T) {
			out := inject(t, noExif[name](), newBlob)
			assert.Equal(t, newBlob, locatedBlob(t, out), "inserted blob mismatch")
		})
	}
}

func TestInjectWebPFlagToggles(t *testing.T) {
	// Insert sets the VP8X EXIF flag; delete clears it.
	inserted := inject(t, webpNoExif(), newBlob)
	_, _, loc, _ := Locate(bytes.NewReader(inserted), int64(len(inserted)))
	assert.NotZero(t, inserted[loc.vp8xFlags]&webpExifFlag, "insert did not set the VP8X EXIF flag")

	deleted := inject(t, inserted, nil)
	// vp8xFlags offset is stable (VP8X precedes EXIF).
	assert.Zero(t, deleted[loc.vp8xFlags]&webpExifFlag, "delete did not clear the VP8X EXIF flag")
}

// FuzzInject feeds arbitrary container files + blobs through the splice path.
// Baseline: no panic, and a successful inject still yields sniffable bytes.
func FuzzInject(f *testing.F) {
	f.Add(jpegWith(sentinel), []byte("II\x2a\x00blob"))
	f.Add(pngWith(sentinel), []byte("MM\x00\x2ablob"))
	f.Add(webpWith(sentinel), []byte(nil)) // delete path

	f.Fuzz(func(t *testing.T, file, blob []byte) {
		r := bytes.NewReader(file)
		_, _, loc, err := Locate(r, int64(len(file)))
		if err != nil && err != ErrNoExif {
			return
		}
		var out bytes.Buffer
		if err := Inject(&out, bytes.NewReader(file), int64(len(file)), loc, blob); err != nil {
			return
		}
		// Must not panic; sniffing is best-effort (a delete can leave a stub).
		_, _ = detectFormat(bytes.NewReader(out.Bytes()))
	})
}
