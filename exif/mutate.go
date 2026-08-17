package exif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/driquet/pictor/tiff"
)

// tagMakerNote is the vendor blob write can't safely relocate: Opaque bytes,
// Pinned to its original offset (dropped with a warning if that's not honourable).
const tagMakerNote uint16 = 0x927C

// tagGroup routes each modeled, settable tag to the IFD it lives in.
var tagGroup = map[uint16]Group{
	tagMake: GroupIFD0, tagModel: GroupIFD0, tagSoftware: GroupIFD0,
	tagOrientation: GroupIFD0, tagDateTime: GroupIFD0,

	tagDateTimeOriginal: GroupExif, tagOffsetTimeOriginal: GroupExif,
	tagExposureTime: GroupExif, tagFNumber: GroupExif, tagISO: GroupExif,
	tagFocalLength: GroupExif, tagPixelXDimension: GroupExif,
	tagPixelYDimension: GroupExif, tagLensModel: GroupExif,

	tagGPSLatitudeRef: GroupGPS, tagGPSLatitude: GroupGPS,
	tagGPSLongitudeRef: GroupGPS, tagGPSLongitude: GroupGPS,
	tagGPSAltitudeRef: GroupGPS, tagGPSAltitude: GroupGPS,
}

// subPointerTag is the tag id under which a group's sub-IFD hangs off root.Subs.
func subPointerTag(g Group) uint16 {
	switch g {
	case GroupExif:
		return tagExifIFD
	case GroupGPS:
		return tagGPSIFD
	default:
		return 0
	}
}

// setEntry adds or replaces a modeled entry, routing it to the correct
// sub-IFD and creating that sub-IFD on demand. Encode synthesizes its
// pointer entry from Subs, so no placeholder pointer is needed here. Reports
// whether id is a known modeled tag.
func setEntry(root *tiff.IFD, id uint16, e tiff.Entry) bool {
	g, ok := tagGroup[id]
	if !ok {
		return false
	}
	e.Tag = id
	upsert(ifdFor(root, g, true), e)
	return true
}

// removeEntry removes entry id from wherever it lives, pruning any sub-IFD it
// left empty. Reports whether a tag went.
func removeEntry(root *tiff.IFD, id uint16) bool {
	g, ok := tagGroup[id]
	if !ok {
		return false
	}
	target := ifdFor(root, g, false)
	if target == nil || !deleteEntry(target, id) {
		return false
	}
	pruneEmpty(root)
	return true
}

// stripAll empties the tree so Encode yields a bare, EXIF-free TIFF: the
// total-removal signal, recognized and unrecognized entries alike.
func stripAll(root *tiff.IFD) {
	root.Entries = nil
	root.Subs = nil
	root.Next = nil
}

// ifdFor resolves the sub-IFD for a group, creating it on demand when create
// is true. Returns nil for a non-existent sub-IFD when create is false.
func ifdFor(root *tiff.IFD, g Group, create bool) *tiff.IFD {
	if g == GroupIFD0 {
		return root
	}
	tag := subPointerTag(g)
	if child := root.Subs[tag]; child != nil {
		return child
	}
	if !create {
		return nil
	}
	child := &tiff.IFD{}
	if root.Subs == nil {
		root.Subs = map[uint16]*tiff.IFD{}
	}
	root.Subs[tag] = child
	return child
}

// pruneEmpty drops the GPS or Exif sub-IFD once it holds no entries and no
// subs of its own (Exif never nests further sub-IFDs in the settable set).
func pruneEmpty(root *tiff.IFD) {
	for _, tag := range []uint16{tagExifIFD, tagGPSIFD} {
		if child := root.Subs[tag]; child != nil && len(child.Entries) == 0 && len(child.Subs) == 0 {
			delete(root.Subs, tag)
		}
	}
}

// upsert replaces the entry with the same tag, or appends. Encode sorts
// entries by tag before writing, so insertion order doesn't matter here.
func upsert(ifd *tiff.IFD, e tiff.Entry) {
	for i := range ifd.Entries {
		if ifd.Entries[i].Tag == e.Tag {
			ifd.Entries[i] = e
			return
		}
	}
	ifd.Entries = append(ifd.Entries, e)
}

// deleteEntry removes the first entry with tag id, reporting whether one was found.
func deleteEntry(ifd *tiff.IFD, id uint16) bool {
	for i := range ifd.Entries {
		if ifd.Entries[i].Tag == id {
			ifd.Entries = append(ifd.Entries[:i], ifd.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// --- write facade: Set / Strip ------------------------------------------------

// ErrUnknownTag is returned by Set/Strip for a name outside the modeled set.
var ErrUnknownTag = errors.New("exif: unknown tag name")

// A pendingEdit is a validated assignment whose on-disk bytes are built at
// apply time, once the target tree's byte order is known.
type pendingEdit struct {
	id    uint16
	build func(binary.ByteOrder) tiff.Entry
}

// settableTag ties a settable name to the tag ids it owns (for Strip) and its
// value parser (for Set).
type settableTag struct {
	ids   []uint16
	parse func(value string) ([]pendingEdit, error)
}

// Set parses NAME=VALUE assignments into a tree mutation. Values follow
// strict-canonical syntax with two smarts: GPS decimal degrees and rational
// N/D. All parsing/validation happens now (fail-fast, before any file write);
// the byte order is applied when the mutation runs against a tree.
func Set(kvs []string) (func(*tiff.File), error) {
	var edits []pendingEdit
	for _, kv := range kvs {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("expected NAME=VALUE, got %q", kv)
		}
		st, ok := settable[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownTag, name)
		}
		es, err := st.parse(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		edits = append(edits, es...)
	}
	return func(f *tiff.File) {
		order := f.Order
		for _, e := range edits {
			setEntry(f.Root, e.id, e.build(order))
		}
	}, nil
}

// IsTagName reports whether name is a modeled, settable/strippable tag.
func IsTagName(name string) bool {
	_, ok := settable[name]
	return ok
}

// Strip builds a mutation removing the named tags, or all EXIF when names is
// empty. An unknown name errors before any file is touched.
func Strip(names []string) (func(*tiff.File), error) {
	if len(names) == 0 {
		return func(f *tiff.File) {
			stripAll(f.Root)
		}, nil
	}
	var ids []uint16
	for _, n := range names {
		st, ok := settable[n]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownTag, n)
		}
		ids = append(ids, st.ids...)
	}
	return func(f *tiff.File) {
		for _, id := range ids {
			removeEntry(f.Root, id)
		}
	}, nil
}

// settable is the modeled write vocabulary: name → (owned ids, value parser).
var settable = map[string]settableTag{
	"Make":               asciiSettable(tagMake),
	"Model":              asciiSettable(tagModel),
	"Software":           asciiSettable(tagSoftware),
	"Orientation":        shortSettable(tagOrientation, 1, 8),
	"DateTime":           datetimeSettable(tagDateTime),
	"DateTimeOriginal":   datetimeSettable(tagDateTimeOriginal),
	"OffsetTimeOriginal": asciiSettable(tagOffsetTimeOriginal),
	"ExposureTime":       rationalSettable(tagExposureTime),
	"FNumber":            rationalSettable(tagFNumber),
	"ISO":                shortSettable(tagISO, 0, 65535),
	"FocalLength":        rationalSettable(tagFocalLength),
	"PixelXDimension":    longSettable(tagPixelXDimension),
	"PixelYDimension":    longSettable(tagPixelYDimension),
	"LensModel":          asciiSettable(tagLensModel),
	"GPSLatitude":        gpsCoordSettable(tagGPSLatitude, tagGPSLatitudeRef, "N", "S"),
	"GPSLongitude":       gpsCoordSettable(tagGPSLongitude, tagGPSLongitudeRef, "E", "W"),
	"GPSAltitude":        gpsAltSettable(),
}

func asciiSettable(id uint16) settableTag {
	return settableTag{ids: []uint16{id}, parse: func(v string) ([]pendingEdit, error) {
		raw := append([]byte(v), 0)
		build := func(binary.ByteOrder) tiff.Entry {
			return tiff.Entry{Tag: id, Type: tiff.TypeASCII, Count: uint32(len(raw)), Raw: raw}
		}
		return []pendingEdit{{id, build}}, nil
	}}
}

func datetimeSettable(id uint16) settableTag {
	return settableTag{ids: []uint16{id}, parse: func(v string) ([]pendingEdit, error) {
		if _, err := time.Parse(dateTimeLayout, v); err != nil {
			return nil, fmt.Errorf("expected %q, got %q", dateTimeLayout, v)
		}
		raw := append([]byte(v), 0)
		build := func(binary.ByteOrder) tiff.Entry {
			return tiff.Entry{Tag: id, Type: tiff.TypeASCII, Count: uint32(len(raw)), Raw: raw}
		}
		return []pendingEdit{{id, build}}, nil
	}}
}

func shortSettable(id uint16, min, max uint32) settableTag {
	return settableTag{ids: []uint16{id}, parse: func(v string) ([]pendingEdit, error) {
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", v)
		}
		if uint32(n) < min || uint32(n) > max {
			return nil, fmt.Errorf("out of range %d..%d: %d", min, max, n)
		}
		build := func(o binary.ByteOrder) tiff.Entry {
			raw := make([]byte, 2)
			o.PutUint16(raw, uint16(n))
			return tiff.Entry{Tag: id, Type: tiff.TypeShort, Count: 1, Raw: raw}
		}
		return []pendingEdit{{id, build}}, nil
	}}
}

func longSettable(id uint16) settableTag {
	return settableTag{ids: []uint16{id}, parse: func(v string) ([]pendingEdit, error) {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", v)
		}
		build := func(o binary.ByteOrder) tiff.Entry {
			raw := make([]byte, 4)
			o.PutUint32(raw, uint32(n))
			return tiff.Entry{Tag: id, Type: tiff.TypeLong, Count: 1, Raw: raw}
		}
		return []pendingEdit{{id, build}}, nil
	}}
}

func rationalSettable(id uint16) settableTag {
	return settableTag{ids: []uint16{id}, parse: func(v string) ([]pendingEdit, error) {
		r, err := parseRational(v)
		if err != nil {
			return nil, err
		}
		build := func(o binary.ByteOrder) tiff.Entry {
			raw := make([]byte, 8)
			putRat(o, raw, r.Num, r.Denom)
			return tiff.Entry{Tag: id, Type: tiff.TypeRational, Count: 1, Raw: raw}
		}
		return []pendingEdit{{id, build}}, nil
	}}
}

// gpsCoordSettable parses signed decimal degrees into a deg/min/sec RATIONAL
// triplet plus its N/S or E/W reference tag.
func gpsCoordSettable(coordID, refID uint16, pos, neg string) settableTag {
	return settableTag{ids: []uint16{coordID, refID}, parse: func(v string) ([]pendingEdit, error) {
		deg, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("expected decimal degrees, got %q", v)
		}
		ref := pos
		if deg < 0 {
			ref, deg = neg, -deg
		}
		d, m, s := dms(deg)
		refRaw := append([]byte(ref), 0)
		return []pendingEdit{
			{coordID, func(o binary.ByteOrder) tiff.Entry {
				raw := make([]byte, 24)
				putRat(o, raw[0:], d, 1)
				putRat(o, raw[8:], m, 1)
				putRat(o, raw[16:], s, 100) // seconds ×100 → 2-decimal precision
				return tiff.Entry{Tag: coordID, Type: tiff.TypeRational, Count: 3, Raw: raw}
			}},
			{refID, func(binary.ByteOrder) tiff.Entry {
				return tiff.Entry{Tag: refID, Type: tiff.TypeASCII, Count: 2, Raw: refRaw}
			}},
		}, nil
	}}
}

func gpsAltSettable() settableTag {
	return settableTag{ids: []uint16{tagGPSAltitude, tagGPSAltitudeRef}, parse: func(v string) ([]pendingEdit, error) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("expected meters, got %q", v)
		}
		var ref byte
		if f < 0 {
			ref, f = 1, -f // ref byte 1 = below sea level
		}
		cm := uint32(math.Round(f * 100))
		return []pendingEdit{
			{tagGPSAltitude, func(o binary.ByteOrder) tiff.Entry {
				raw := make([]byte, 8)
				putRat(o, raw, cm, 100)
				return tiff.Entry{Tag: tagGPSAltitude, Type: tiff.TypeRational, Count: 1, Raw: raw}
			}},
			{tagGPSAltitudeRef, func(binary.ByteOrder) tiff.Entry {
				return tiff.Entry{Tag: tagGPSAltitudeRef, Type: tiff.TypeByte, Count: 1, Raw: []byte{ref}}
			}},
		}, nil
	}}
}

// parseRational accepts "N/D" or a bare integer (N → N/1) and rejects a bare
// decimal, pointing the caller at the N/D form.
func parseRational(v string) (tiff.Rational, error) {
	if n, d, ok := strings.Cut(v, "/"); ok {
		num, e1 := strconv.ParseUint(n, 10, 32)
		den, e2 := strconv.ParseUint(d, 10, 32)
		if e1 != nil || e2 != nil {
			return tiff.Rational{}, fmt.Errorf("expected N/D integers, got %q", v)
		}
		return tiff.Rational{Num: uint32(num), Denom: uint32(den)}, nil
	}
	if strings.Contains(v, ".") {
		return tiff.Rational{}, fmt.Errorf("decimal not allowed, use N/D (got %q)", v)
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return tiff.Rational{}, fmt.Errorf("expected integer or N/D, got %q", v)
	}
	return tiff.Rational{Num: uint32(n), Denom: 1}, nil
}

// dms splits decimal degrees into whole degrees, whole minutes, and seconds×100.
func dms(deg float64) (d, m, sHundredths uint32) {
	d = uint32(deg)
	rem := (deg - float64(d)) * 60
	m = uint32(rem)
	s := (rem - float64(m)) * 60
	return d, m, uint32(math.Round(s * 100))
}

func putRat(o binary.ByteOrder, b []byte, num, den uint32) {
	o.PutUint32(b[0:4], num)
	o.PutUint32(b[4:8], den)
}
