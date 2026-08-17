package exif

import (
	"fmt"
	"time"

	"github.com/driquet/pictor/tiff"
)

const dateTimeLayout = "2006:01:02 15:04:05"

// Orientation is the EXIF orientation enum (1..8).
type Orientation uint16

const (
	OrientationTopLeft     Orientation = 1
	OrientationTopRight    Orientation = 2
	OrientationBottomRight Orientation = 3
	OrientationBottomLeft  Orientation = 4
	OrientationLeftTop     Orientation = 5
	OrientationRightTop    Orientation = 6
	OrientationRightBottom Orientation = 7
	OrientationLeftBottom  Orientation = 8
)

// String renders the orientation as the visual transform needed to view the
// image upright (per the EXIF spec's 8 orientations).
func (o Orientation) String() string {
	switch o {
	case OrientationTopLeft:
		return "Normal"
	case OrientationTopRight:
		return "Mirror horizontal"
	case OrientationBottomRight:
		return "Rotate 180°"
	case OrientationBottomLeft:
		return "Mirror vertical"
	case OrientationLeftTop:
		return "Mirror horizontal, rotate 270° CW"
	case OrientationRightTop:
		return "Rotate 90° CW"
	case OrientationRightBottom:
		return "Mirror horizontal, rotate 90° CW"
	case OrientationLeftBottom:
		return "Rotate 270° CW"
	default:
		return fmt.Sprintf("Unknown (%d)", uint16(o))
	}
}

// Metadata is the flat typed view. Pointer/empty-string fields mean
// absent-or-unparseable; the []error from FromTags names which faults fired.
type Metadata struct {
	Make, Model, Software string
	Orientation           *Orientation
	DateTime              *time.Time // naive wall-clock

	DateTimeOriginal *time.Time
	ExposureTime     *tiff.Rational
	FNumber          *float64
	ISO              *uint16
	FocalLength      *float64 // mm
	PixelXDimension  *uint32
	PixelYDimension  *uint32
	LensModel        string

	GPS *GPSInfo
}

// GPSInfo holds decoded GPS coordinates. Latitude/Longitude are signed (S/W
// negative); Altitude ref byte 1 => below sea level => negative.
type GPSInfo struct {
	Latitude, Longitude float64
	Altitude            *float64
}

// FromTags folds the extracted tags into a Metadata. Never fails wholesale:
// absent tags leave fields nil with no error; a malformed cross-tag combination
// (e.g. a lone GPS coordinate, an unparseable datetime) leaves the field nil and
// appends a fault.
func FromTags(tags []Tag) (*Metadata, []error) {
	m := &Metadata{}
	x := index(tags)
	var errs []error

	m.Make = x.str("Make")
	m.Model = x.str("Model")
	m.Software = x.str("Software")
	m.LensModel = x.str("LensModel")

	if o, ok := x.get("Orientation").(Orientation); ok {
		m.Orientation = &o
	}
	if r, ok := x.get("ExposureTime").(tiff.Rational); ok {
		m.ExposureTime = &r
	}
	m.FNumber = x.ratFloat("FNumber")
	m.FocalLength = x.ratFloat("FocalLength")
	if v, ok := x.get("ISO").(uint16); ok {
		m.ISO = &v
	}
	if v, ok := x.get("PixelXDimension").(uint32); ok {
		m.PixelXDimension = &v
	}
	if v, ok := x.get("PixelYDimension").(uint32); ok {
		m.PixelYDimension = &v
	}

	m.DateTime = parseDateTime(x.str("DateTime"), "", tagDateTime, &errs)
	m.DateTimeOriginal = parseDateTime(x.str("DateTimeOriginal"), x.str("OffsetTimeOriginal"), tagDateTimeOriginal, &errs)

	m.GPS = buildGPS(x, &errs)
	sortErrors(errs)
	return m, errs
}

// --- tag index ---------------------------------------------------------------

type tagIndex map[string]any

func index(tags []Tag) tagIndex {
	x := make(tagIndex, len(tags))
	for _, t := range tags {
		x[t.Name] = t.Value
	}
	return x
}

func (x tagIndex) get(name string) any { return x[name] }

func (x tagIndex) str(name string) string {
	s, _ := x[name].(string)
	return s
}

func (x tagIndex) ratFloat(name string) *float64 {
	if r, ok := x[name].(tiff.Rational); ok {
		f := r.Float64()
		return &f
	}
	return nil
}

// --- cross-tag assembly ------------------------------------------------------

func parseDateTime(s, offset string, tag uint16, errs *[]error) *time.Time {
	if s == "" {
		return nil
	}
	layout, val := dateTimeLayout, s
	if offset != "" {
		layout, val = dateTimeLayout+" -07:00", s+" "+offset
	}
	tm, err := time.Parse(layout, val)
	if err != nil {
		*errs = append(*errs, newErr(tiff.ErrBadValue, tag, "bad datetime: "+err.Error()))
		return nil
	}
	return &tm
}

func buildGPS(x tagIndex, errs *[]error) *GPSInfo {
	g := &GPSInfo{}
	got := false

	// Latitude and longitude are only meaningful as a pair: a lone coordinate
	// would leave the other at 0 (Null Island), so require both.
	lat, latOK := gpsCoord(x, "GPSLatitude", "GPSLatitudeRef", "S")
	lon, lonOK := gpsCoord(x, "GPSLongitude", "GPSLongitudeRef", "W")
	switch {
	case latOK && lonOK:
		g.Latitude, g.Longitude = lat, lon
		got = true
	case latOK != lonOK:
		*errs = append(*errs, newErr(tiff.ErrBadValue, tagGPSLatitude, "GPS has only one of latitude/longitude"))
	}

	if r, ok := x.get("GPSAltitude").(tiff.Rational); ok {
		alt := r.Float64()
		if ref, ok := x.get("GPSAltitudeRef").(byte); ok && ref == 1 {
			alt = -alt // below sea level
		}
		g.Altitude = &alt
		got = true
	}
	if got {
		return g
	}
	return nil
}

// gpsCoord combines a deg/min/sec triplet with its N/S or E/W ref into signed
// decimal degrees.
func gpsCoord(x tagIndex, coord, ref, neg string) (float64, bool) {
	t, ok := x.get(coord).([]tiff.Rational)
	if !ok || len(t) < 3 {
		return 0, false
	}
	v := t[0].Float64() + t[1].Float64()/60 + t[2].Float64()/3600
	if r, ok := x.get(ref).(string); ok && r == neg {
		v = -v
	}
	return v, true
}
