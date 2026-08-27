package exif

import (
	"encoding/binary"
	"sort"
	"strings"

	"github.com/driquet/pictor/tiff"
)

// Group is the IFD a tag lives in. Tag ids are only unambiguous within a group
// (GPS reuses the low id space), so extraction looks each entry up in the table
// for the IFD it is walking.
type Group uint8

const (
	GroupIFD0 Group = iota
	GroupExif
	GroupGPS
)

func (g Group) String() string {
	switch g {
	case GroupIFD0:
		return "IFD0"
	case GroupExif:
		return "Exif"
	case GroupGPS:
		return "GPS"
	default:
		return "?"
	}
}

// Tag is a recognized EXIF tag with its value converted from raw bytes to a
// semantic Go value. Value's dynamic type is fixed per tag by the tag table:
// string, uint16, uint32, tiff.Rational, []tiff.Rational, or uint8. Cross-tag
// assembly (GPS triplet+ref, datetime+offset) happens later, in fromTags.
type Tag struct {
	ID    uint16
	Name  TagName
	Group Group
	Value any
}

// TagName identifies a modeled, settable EXIF tag by name.
type TagName string

// The full settable/strippable tag vocabulary.
const (
	TagMake               TagName = "Make"
	TagModel              TagName = "Model"
	TagSoftware           TagName = "Software"
	TagOrientation        TagName = "Orientation"
	TagDateTime           TagName = "DateTime"
	TagDateTimeOriginal   TagName = "DateTimeOriginal"
	TagOffsetTimeOriginal TagName = "OffsetTimeOriginal"
	TagExposureTime       TagName = "ExposureTime"
	TagFNumber            TagName = "FNumber"
	TagISO                TagName = "ISO"
	TagFocalLength        TagName = "FocalLength"
	TagPixelXDimension    TagName = "PixelXDimension"
	TagPixelYDimension    TagName = "PixelYDimension"
	TagLensModel          TagName = "LensModel"
	TagGPSLatitude        TagName = "GPSLatitude"
	TagGPSLongitude       TagName = "GPSLongitude"
	TagGPSAltitude        TagName = "GPSAltitude"
)

// valueConv turns one entry's raw bytes into its semantic value, or errors if
// the entry has the wrong type/shape. It never sees cross-tag context.
type valueConv func(o binary.ByteOrder, e tiff.Entry) (any, error)

type tagDef struct {
	name TagName
	conv valueConv
}

// Tag ids, grouped by the IFD they live in.
const (
	// IFD0
	tagMake        uint16 = 0x010F
	tagModel       uint16 = 0x0110
	tagSoftware    uint16 = 0x0131
	tagOrientation uint16 = 0x0112
	tagDateTime    uint16 = 0x0132
	// Exif SubIFD
	tagDateTimeOriginal   uint16 = 0x9003
	tagOffsetTimeOriginal uint16 = 0x9011
	tagExposureTime       uint16 = 0x829A
	tagFNumber            uint16 = 0x829D
	tagISO                uint16 = 0x8827
	tagFocalLength        uint16 = 0x920A
	tagPixelXDimension    uint16 = 0xA002
	tagPixelYDimension    uint16 = 0xA003
	tagLensModel          uint16 = 0xA434
	// GPS
	tagGPSLatitudeRef  uint16 = 0x0001
	tagGPSLatitude     uint16 = 0x0002
	tagGPSLongitudeRef uint16 = 0x0003
	tagGPSLongitude    uint16 = 0x0004
	tagGPSAltitudeRef  uint16 = 0x0005
	tagGPSAltitude     uint16 = 0x0006
)

// Per-group tag tables. Keyed by id within the group the extractor is walking.
// GPS ref tags (LatitudeRef/LongitudeRef) aren't independently settable (they
// travel with their coordinate via SetTag), so they have no TagName constant.
const (
	tagNameGPSLatitudeRef  TagName = "GPSLatitudeRef"
	tagNameGPSLongitudeRef TagName = "GPSLongitudeRef"
	tagNameGPSAltitudeRef  TagName = "GPSAltitudeRef"
)

var (
	ifd0Table = map[uint16]tagDef{
		tagMake:        {TagMake, convASCII},
		tagModel:       {TagModel, convASCII},
		tagSoftware:    {TagSoftware, convASCII},
		tagOrientation: {TagOrientation, convOrientation},
		tagDateTime:    {TagDateTime, convASCII},
	}
	exifTable = map[uint16]tagDef{
		tagDateTimeOriginal:   {TagDateTimeOriginal, convASCII},
		tagOffsetTimeOriginal: {TagOffsetTimeOriginal, convASCII},
		tagExposureTime:       {TagExposureTime, convRational},
		tagFNumber:            {TagFNumber, convRational},
		tagISO:                {TagISO, convShort},
		tagFocalLength:        {TagFocalLength, convRational},
		tagPixelXDimension:    {TagPixelXDimension, convDimension},
		tagPixelYDimension:    {TagPixelYDimension, convDimension},
		tagLensModel:          {TagLensModel, convASCII},
	}
	gpsTable = map[uint16]tagDef{
		tagGPSLatitudeRef:  {tagNameGPSLatitudeRef, convASCII},
		tagGPSLatitude:     {TagGPSLatitude, convRationalTriplet},
		tagGPSLongitudeRef: {tagNameGPSLongitudeRef, convASCII},
		tagGPSLongitude:    {TagGPSLongitude, convRationalTriplet},
		tagGPSAltitudeRef:  {tagNameGPSAltitudeRef, convByte},
		tagGPSAltitude:     {TagGPSAltitude, convRational},
	}
)

// extract walks the parsed IFD tree and returns the recognized tags with their
// values converted. Unrecognized tags are ignored; malformed recognized tags are
// skipped with a fault appended. Deterministic order (by group, then id).
func extract(f *tiff.File) ([]Tag, []error) {
	if f == nil || f.Root == nil {
		return nil, nil
	}
	e := &extractor{order: f.Order}
	e.walk(f.Root, GroupIFD0, ifd0Table)
	if exif := f.Root.Subs[tagExifIFD]; exif != nil {
		e.walk(exif, GroupExif, exifTable)
	}
	if gps := f.Root.Subs[tagGPSIFD]; gps != nil {
		e.walk(gps, GroupGPS, gpsTable)
	}
	sortTags(e.tags)
	sortErrors(e.errs)
	return e.tags, e.errs
}

type extractor struct {
	order binary.ByteOrder
	tags  []Tag
	errs  []error
}

func (e *extractor) walk(ifd *tiff.IFD, group Group, table map[uint16]tagDef) {
	for _, entry := range ifd.Entries {
		def, ok := table[entry.Tag]
		if !ok {
			continue // unrecognized: not this package's concern
		}
		v, err := def.conv(e.order, entry)
		if err != nil {
			e.errs = append(e.errs, wrapConv(err, entry.Tag))
			continue
		}
		e.tags = append(e.tags, Tag{ID: entry.Tag, Name: def.name, Group: group, Value: v})
	}
}

func sortTags(tags []Tag) {
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Group != tags[j].Group {
			return tags[i].Group < tags[j].Group
		}
		return tags[i].ID < tags[j].ID
	})
}

// --- value converters (single-tag) --------------------------------------------

func convASCII(_ binary.ByteOrder, e tiff.Entry) (any, error) {
	s := string(e.Raw)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), nil
}

func convShort(o binary.ByteOrder, e tiff.Entry) (any, error) {
	if e.Type != tiff.TypeShort || len(e.Raw) < 2 {
		return nil, errWrongType("not a SHORT")
	}
	return o.Uint16(e.Raw[:2]), nil
}

func convOrientation(o binary.ByteOrder, e tiff.Entry) (any, error) {
	if e.Type != tiff.TypeShort || len(e.Raw) < 2 {
		return nil, errWrongType("orientation not a SHORT")
	}
	v := o.Uint16(e.Raw[:2])
	if v < 1 || v > 8 {
		return nil, errBadValue("orientation out of range 1..8")
	}
	return Orientation(v), nil
}

func convRational(o binary.ByteOrder, e tiff.Entry) (any, error) {
	if e.Type != tiff.TypeRational || len(e.Raw) < 8 {
		return nil, errWrongType("not a RATIONAL")
	}
	return rationalAt(o, e.Raw, 0), nil
}

// convRationalTriplet reads the GPS deg/min/sec triplet (3 RATIONALs).
func convRationalTriplet(o binary.ByteOrder, e tiff.Entry) (any, error) {
	if e.Type != tiff.TypeRational {
		return nil, errWrongType("GPS coordinate not RATIONAL")
	}
	if e.Count < 3 || len(e.Raw) < 24 {
		return nil, errBadCount("GPS coordinate needs 3 RATIONALs")
	}
	return []tiff.Rational{
		rationalAt(o, e.Raw, 0), rationalAt(o, e.Raw, 8), rationalAt(o, e.Raw, 16),
	}, nil
}

// convDimension reads a SHORT or LONG as uint32.
func convDimension(o binary.ByteOrder, e tiff.Entry) (any, error) {
	switch e.Type {
	case tiff.TypeShort:
		if len(e.Raw) < 2 {
			return nil, errBadValue("dimension SHORT truncated")
		}
		return uint32(o.Uint16(e.Raw[:2])), nil
	case tiff.TypeLong:
		if len(e.Raw) < 4 {
			return nil, errBadValue("dimension LONG truncated")
		}
		return o.Uint32(e.Raw[:4]), nil
	default:
		return nil, errWrongType("dimension not SHORT/LONG")
	}
}

func convByte(_ binary.ByteOrder, e tiff.Entry) (any, error) {
	if len(e.Raw) < 1 {
		return nil, errBadValue("empty BYTE")
	}
	return e.Raw[0], nil
}

func rationalAt(o binary.ByteOrder, b []byte, off int) tiff.Rational {
	return tiff.Rational{Num: o.Uint32(b[off : off+4]), Denom: o.Uint32(b[off+4 : off+8])}
}
