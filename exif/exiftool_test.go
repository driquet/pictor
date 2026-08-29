package exif

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/driquet/pictor/tiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantMeta is the tag set both directions of the exiftool comparison write
// and check for, chosen to hit every value type Metadata models: string,
// enum, datetime, rational, integer, and the GPS triplet.
var wantMeta = struct {
	Make, Model, Software, LensModel string
	Orientation                      Orientation
	DateTime, DateTimeOriginal       time.Time
	FNumber, FocalLength             float64
	ISO                              uint16
	PixelXDimension, PixelYDimension uint32
	GPSLatitude, GPSLongitude        float64
	GPSAltitude                      float64
}{
	Make: "Acme", Model: "Widget9000", Software: "pictor-test", LensModel: "TestLens 50mm",
	Orientation:      OrientationRightTop, // 6
	DateTime:         time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	DateTimeOriginal: time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC),
	FNumber:          2.8, FocalLength: 50,
	ISO:             400,
	PixelXDimension: 123,
	PixelYDimension: 456,
	GPSLatitude:     48.8566,
	GPSLongitude:    2.3522,
	GPSAltitude:     42.5,
}

func requireExiftool(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not found on PATH")
	}
	return path
}

var baseImages = map[string]string{
	"jpeg": "testdata/base.jpg",
	"png":  "testdata/base.png",
	"tiff": "testdata/base.tif",
	"webp": "testdata/base.webp",
}

// TestExiftoolWrite: pictor writes the known tag set into a copy of each
// format's base image via SetTag, then exiftool reads the result back and its
// reported values must match.
func TestExiftoolWrite(t *testing.T) {
	exiftool := requireExiftool(t)

	for format, base := range baseImages {
		t.Run(format, func(t *testing.T) {
			b, err := os.ReadFile(base)
			require.NoError(t, err)

			doc, err := ReadBytes(b)
			require.NoError(t, err)

			require.NoError(t, doc.SetTag(TagMake, wantMeta.Make))
			require.NoError(t, doc.SetTag(TagModel, wantMeta.Model))
			require.NoError(t, doc.SetTag(TagSoftware, wantMeta.Software))
			require.NoError(t, doc.SetTag(TagLensModel, wantMeta.LensModel))
			require.NoError(t, doc.SetTag(TagOrientation, wantMeta.Orientation))
			require.NoError(t, doc.SetTag(TagDateTime, wantMeta.DateTime))
			require.NoError(t, doc.SetTag(TagDateTimeOriginal, wantMeta.DateTimeOriginal))
			require.NoError(t, doc.SetTag(TagExposureTime, tiff.Rational{Num: 1, Denom: 125}))
			require.NoError(t, doc.SetTag(TagFNumber, tiff.Rational{Num: 28, Denom: 10}))
			require.NoError(t, doc.SetTag(TagISO, wantMeta.ISO))
			require.NoError(t, doc.SetTag(TagFocalLength, tiff.Rational{Num: 50, Denom: 1}))
			require.NoError(t, doc.SetTag(TagPixelXDimension, wantMeta.PixelXDimension))
			require.NoError(t, doc.SetTag(TagPixelYDimension, wantMeta.PixelYDimension))
			require.NoError(t, doc.SetTag(TagGPSLatitude, wantMeta.GPSLatitude))
			require.NoError(t, doc.SetTag(TagGPSLongitude, wantMeta.GPSLongitude))
			require.NoError(t, doc.SetTag(TagGPSAltitude, wantMeta.GPSAltitude))

			var buf bytes.Buffer
			_, err = doc.Write(&buf)
			require.NoError(t, err)

			path := filepath.Join(t.TempDir(), "written"+filepath.Ext(base))
			require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

			out, err := exec.Command(exiftool, "-j", "-n", path).Output()
			require.NoError(t, err)

			var results []map[string]any
			require.NoError(t, json.Unmarshal(out, &results))
			require.Len(t, results, 1)
			got := results[0]

			assert.Equal(t, wantMeta.Make, got["Make"])
			assert.Equal(t, wantMeta.Model, got["Model"])
			assert.Equal(t, wantMeta.Software, got["Software"])
			assert.Equal(t, wantMeta.LensModel, got["LensModel"])
			assert.EqualValues(t, wantMeta.Orientation, got["Orientation"])
			assert.Equal(t, "2024:01:15 10:30:00", got["ModifyDate"])
			assert.Equal(t, "2024:01:15 09:00:00", got["DateTimeOriginal"])
			assert.InDelta(t, 1.0/125, toFloat(t, got["ExposureTime"]), 1e-6)
			assert.InDelta(t, wantMeta.FNumber, toFloat(t, got["FNumber"]), 1e-6)
			assert.EqualValues(t, wantMeta.ISO, got["ISO"])
			assert.InDelta(t, wantMeta.FocalLength, toFloat(t, got["FocalLength"]), 1e-6)
			assert.EqualValues(t, wantMeta.PixelXDimension, got["ExifImageWidth"])
			assert.EqualValues(t, wantMeta.PixelYDimension, got["ExifImageHeight"])
			assert.InDelta(t, wantMeta.GPSLatitude, toFloat(t, got["GPSLatitude"]), 1e-4)
			assert.InDelta(t, wantMeta.GPSLongitude, toFloat(t, got["GPSLongitude"]), 1e-4)
			assert.InDelta(t, wantMeta.GPSAltitude, toFloat(t, got["GPSAltitude"]), 1e-2)
		})
	}
}

func toFloat(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	require.Truef(t, ok, "want numeric, got %T (%v)", v, v)
	return f
}
