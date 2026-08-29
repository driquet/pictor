package exif

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/driquet/pictor/tiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonregDir holds non-regression fixtures: an image plus a same-named .json
// of exiftool's reading of it. Add a fixture here whenever a real bug is
// found - drop the offending image in, run the test to generate its expected
// json, review the json, commit both. TestExiftoolRead/Write cover the
// synthetic all-tags-at-once case; this corpus accumulates real-world ones.
const nonregDir = "testdata/nonregression"

// nonregCheck compares one exiftool JSON key against the Metadata field it
// corresponds to. present is false when exiftool didn't report the tag at
// all, which must mean the Metadata field is absent too - a value pictor
// reports that exiftool doesn't is as much a bug as a mismatched value.
type nonregCheck struct {
	key    string
	assert func(t *testing.T, m *Metadata, v any, present bool)
}

var nonregChecks = []nonregCheck{
	{"Make", checkString(func(m *Metadata) string { return m.Make })},
	{"Model", checkString(func(m *Metadata) string { return m.Model })},
	{"Software", checkString(func(m *Metadata) string { return m.Software })},
	{"LensModel", checkString(func(m *Metadata) string { return m.LensModel })},
	{"Orientation", checkOrientation},
	{"ModifyDate", checkDateTime(func(m *Metadata) *time.Time { return m.DateTime })},
	{"DateTimeOriginal", checkDateTime(func(m *Metadata) *time.Time { return m.DateTimeOriginal })},
	{"ExposureTime", checkRational(func(m *Metadata) *tiff.Rational { return m.ExposureTime }, 1e-6)},
	{"FNumber", checkFloat(func(m *Metadata) *float64 { return m.FNumber }, 1e-6)},
	{"FocalLength", checkFloat(func(m *Metadata) *float64 { return m.FocalLength }, 1e-6)},
	{"ISO", checkUint16(func(m *Metadata) *uint16 { return m.ISO })},
	{"ExifImageWidth", checkUint32(func(m *Metadata) *uint32 { return m.PixelXDimension })},
	{"ExifImageHeight", checkUint32(func(m *Metadata) *uint32 { return m.PixelYDimension })},
}

func checkString(get func(*Metadata) string) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		if !present {
			assert.Empty(t, get(m))
			return
		}
		assert.Equal(t, v, get(m))
	}
}

func checkOrientation(t *testing.T, m *Metadata, v any, present bool) {
	if !present {
		assert.Nil(t, m.Orientation)
		return
	}
	if assert.NotNil(t, m.Orientation) {
		assert.EqualValues(t, v, *m.Orientation)
	}
}

func checkDateTime(get func(*Metadata) *time.Time) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		got := get(m)
		if !present {
			assert.Nil(t, got)
			return
		}
		s, _ := v.(string)
		want, err := time.Parse(dateTimeLayout, s)
		require.NoError(t, err)
		if assert.NotNil(t, got) {
			assert.True(t, want.Equal(*got))
		}
	}
}

func checkFloat(get func(*Metadata) *float64, tol float64) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		got := get(m)
		if !present {
			assert.Nil(t, got)
			return
		}
		want, ok := v.(float64)
		require.Truef(t, ok, "want numeric, got %T (%v)", v, v)
		if assert.NotNil(t, got) {
			assert.InDelta(t, want, *got, tol)
		}
	}
}

func checkRational(get func(*Metadata) *tiff.Rational, tol float64) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		got := get(m)
		if !present {
			assert.Nil(t, got)
			return
		}
		want, ok := v.(float64)
		require.Truef(t, ok, "want numeric, got %T (%v)", v, v)
		if assert.NotNil(t, got) {
			assert.InDelta(t, want, got.Float64(), tol)
		}
	}
}

func checkUint16(get func(*Metadata) *uint16) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		got := get(m)
		if !present {
			assert.Nil(t, got)
			return
		}
		if assert.NotNil(t, got) {
			assert.EqualValues(t, v, *got)
		}
	}
}

func checkUint32(get func(*Metadata) *uint32) func(*testing.T, *Metadata, any, bool) {
	return func(t *testing.T, m *Metadata, v any, present bool) {
		got := get(m)
		if !present {
			assert.Nil(t, got)
			return
		}
		if assert.NotNil(t, got) {
			assert.EqualValues(t, v, *got)
		}
	}
}

// checkGPS handles the three GPS keys together since Metadata.GPS is a
// single struct assembled from all three (see buildGPS in metadata.go).
func checkGPS(t *testing.T, m *Metadata, exif map[string]any) {
	lat, latOK := exif["GPSLatitude"]
	lon, lonOK := exif["GPSLongitude"]
	alt, altOK := exif["GPSAltitude"]

	if !latOK && !lonOK && !altOK {
		assert.Nil(t, m.GPS)
		return
	}
	if !assert.NotNil(t, m.GPS) {
		return
	}
	if latOK && lonOK {
		assert.InDelta(t, lat.(float64), m.GPS.Latitude, 1e-4)
		assert.InDelta(t, lon.(float64), m.GPS.Longitude, 1e-4)
	}
	if !altOK {
		assert.Nil(t, m.GPS.Altitude)
		return
	}
	if assert.NotNil(t, m.GPS.Altitude) {
		assert.InDelta(t, alt.(float64), *m.GPS.Altitude, 1e-2)
	}
}

// TestNonRegression walks testdata/nonregression for supported-extension
// images, reads each with pictor, and compares Metadata against the exiftool
// reading stored in the fixture's same-named .json (see nonregDir's doc
// comment for how to add a fixture).
func TestNonRegression(t *testing.T) {
	entries, err := os.ReadDir(nonregDir)
	require.NoError(t, err)

	fixtures := map[string]string{} // basename -> image path
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !slices.Contains(SupportedExtensions, ext) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ext)
		fixtures[name] = filepath.Join(nonregDir, e.Name())
	}
	require.NotEmpty(t, fixtures, "no fixtures found in %s", nonregDir)

	for name, imgPath := range fixtures {
		t.Run(name, func(t *testing.T) {
			exif := loadOrGenerateExpected(t, imgPath, filepath.Join(nonregDir, name+".json"))

			b, err := os.ReadFile(imgPath)
			require.NoError(t, err)
			doc, err := ReadBytes(b)
			require.NoError(t, err)
			m := doc.Metadata()

			for _, c := range nonregChecks {
				v, present := exif[c.key]
				c.assert(t, m, v, present)
			}
			checkGPS(t, m, exif)
		})
	}
}

// loadOrGenerateExpected returns jsonPath's stored exiftool reading of
// imgPath. If jsonPath doesn't exist yet, it runs exiftool to generate it and
// fails the test either way - once to force the new fixture's expectations
// to be reviewed and committed, and forever if exiftool isn't installed to
// generate them with.
func loadOrGenerateExpected(t *testing.T, imgPath, jsonPath string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		require.ErrorIs(t, err, os.ErrNotExist)
		generateExpected(t, imgPath, jsonPath)
		t.Fatalf("no expected json for %s: generated %s from exiftool's reading - review it and commit, then re-run", imgPath, jsonPath)
	}

	var results []map[string]any
	require.NoError(t, json.Unmarshal(data, &results))
	require.Len(t, results, 1)
	return results[0]
}

// generateExpected runs exiftool on imgPath and writes its JSON reading to
// jsonPath, excluding the File group, ExifToolVersion, and SourceFile - all
// either machine/run-dependent or a plain echo of imgPath - so the fixture
// stays stable across regenerations and machines.
func generateExpected(t *testing.T, imgPath, jsonPath string) {
	t.Helper()

	exiftool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Fatalf("exiftool not found on PATH: install it to generate %s", jsonPath)
	}

	out, err := exec.Command(exiftool, "-j", "-n", "--File:all", "--ExifToolVersion", imgPath).Output()
	require.NoError(t, err)

	var results []map[string]any
	require.NoError(t, json.Unmarshal(out, &results))
	require.Len(t, results, 1)
	delete(results[0], "SourceFile")

	var pretty bytes.Buffer
	enc := json.NewEncoder(&pretty)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(results))
	require.NoError(t, os.WriteFile(jsonPath, pretty.Bytes(), 0o644))
}
