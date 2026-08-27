package exif

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/driquet/pictor/tiff"
)

// tagMakerNote is the vendor blob write can't safely relocate: Opaque bytes,
// Pinned to its original offset (dropped with a warning if that's not honourable).
const tagMakerNote uint16 = 0x927C

// tagGroup routes each modeled, settable tag to the IFD it lives in.
var tagGroup = map[uint16]Group{
	// IFD0
	tagMake:        GroupIFD0,
	tagModel:       GroupIFD0,
	tagSoftware:    GroupIFD0,
	tagOrientation: GroupIFD0,
	tagDateTime:    GroupIFD0,

	// Exif SubIFD
	tagDateTimeOriginal:   GroupExif,
	tagOffsetTimeOriginal: GroupExif,
	tagExposureTime:       GroupExif,
	tagFNumber:            GroupExif,
	tagISO:                GroupExif,
	tagFocalLength:        GroupExif,
	tagPixelXDimension:    GroupExif,
	tagPixelYDimension:    GroupExif,
	tagLensModel:          GroupExif,

	// GPS
	tagGPSLatitudeRef:  GroupGPS,
	tagGPSLatitude:     GroupGPS,
	tagGPSLongitudeRef: GroupGPS,
	tagGPSLongitude:    GroupGPS,
	tagGPSAltitudeRef:  GroupGPS,
	tagGPSAltitude:     GroupGPS,
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

// --- typed write vocabulary ---------------------------------------------------

// ErrUnknownTag is returned by SetTag/RemoveTag for a name outside the
// modeled set.
var ErrUnknownTag = errors.New("exif: unknown tag name")

// A pendingEdit is a validated assignment whose on-disk bytes are built at
// apply time, once the target tree's byte order is known.
type pendingEdit struct {
	id    uint16
	build func(binary.ByteOrder) tiff.Entry
}

// settable is the modeled write vocabulary: name → the tag ids it owns (a
// GPS coordinate/altitude owns both its value tag and its reference tag).
var settable = map[TagName][]uint16{
	TagMake:               {tagMake},
	TagModel:              {tagModel},
	TagSoftware:           {tagSoftware},
	TagOrientation:        {tagOrientation},
	TagDateTime:           {tagDateTime},
	TagDateTimeOriginal:   {tagDateTimeOriginal},
	TagOffsetTimeOriginal: {tagOffsetTimeOriginal},
	TagExposureTime:       {tagExposureTime},
	TagFNumber:            {tagFNumber},
	TagISO:                {tagISO},
	TagFocalLength:        {tagFocalLength},
	TagPixelXDimension:    {tagPixelXDimension},
	TagPixelYDimension:    {tagPixelYDimension},
	TagLensModel:          {tagLensModel},
	TagGPSLatitude:        {tagGPSLatitude, tagGPSLatitudeRef},
	TagGPSLongitude:       {tagGPSLongitude, tagGPSLongitudeRef},
	TagGPSAltitude:        {tagGPSAltitude, tagGPSAltitudeRef},
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
