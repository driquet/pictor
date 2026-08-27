package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/driquet/pictor/exif/container"
	"github.com/driquet/pictor/tiff"
)

// SupportedExtensions lists the file extensions Read/ReadBytes can be pointed
// at directly.
var SupportedExtensions = []string{".tif", ".tiff", ".jpg", ".jpeg", ".png", ".webp"}

// Document is a decoded image's EXIF handle - TIFF, JPEG, PNG, or WebP. It
// retains the full tree (MakerNote, thumbnail, pixel data, unknown tags) plus
// enough container info to write back losslessly, so Tags/SetTag/RemoveTag are
// a view onto the one real tree, never a detached copy that would lose
// whatever they don't model.
//
// Document is the package's only entry point: there is no standalone
// tag-list or string-keyed read/write path outside it.
type Document struct {
	file     *tiff.File
	original io.ReaderAt
	size     int64
	loc      container.Location // zero value for standalone TIFF

	// Tags/Metadata/Errs share one lazily-computed cache: the first call
	// after Read (or after a mutation) walks the tree once; SetTag/
	// RemoveTag/StripAll invalidate it rather than updating it in place, so
	// it always reflects the tree's current state.
	cacheValid bool
	tags       []Tag
	meta       *Metadata
	errs       []error
}

// ErrCannotAddEXIF is returned when a mutation would add a new EXIF section to
// a file whose format can't currently hold one (e.g. a simple, non-VP8X WebP).
var ErrCannotAddEXIF = errors.New("exif: this file's format cannot hold a new EXIF section")

// decodeOpts are the decode options Read needs beyond subIFDOpt: pixel/tile/
// thumbnail data pulled into the tree (so Write can rebuild the file from the
// tree alone) and MakerNote treated as an opaque, pinned blob.
func decodeOpts() []tiff.Option {
	return []tiff.Option{
		subIFDOpt(),
		tiff.WithImageData(),
		tiff.WithOpaqueTags(tagMakerNote),
		tiff.WithImmovableTags(tagMakerNote),
	}
}

// Read decodes an image's EXIF - TIFF, JPEG, PNG, or WebP - into a Document.
// r must stay valid until Write is called: Document holds onto it to splice
// the original container back together. Fails only on a fatal error (unknown
// format, unreadable structure); best-effort tag faults land in Errs instead.
func Read(r io.ReaderAt, size int64) (*Document, error) {
	blob, blobSize, loc, err := container.Locate(r, size)
	if errors.Is(err, container.ErrUnknownFormat) {
		return nil, err
	}

	var f *tiff.File
	switch {
	case loc.Format() == container.FormatTIFF:
		f, err = tiff.Decode(r, size, decodeOpts()...)
	case err == nil:
		f, err = tiff.Decode(blob, blobSize, decodeOpts()...)
	case errors.Is(err, container.ErrNoExif):
		f, err = &tiff.File{Root: &tiff.IFD{}, Order: binary.LittleEndian}, nil
	default:
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	return &Document{file: f, original: r, size: size, loc: loc}, nil
}

// ReadBytes is the in-memory variant of Read.
func ReadBytes(b []byte) (*Document, error) {
	return Read(bytes.NewReader(b), int64(len(b)))
}

// ensureCache walks the tree once (structural parse faults, per-tag
// extraction faults, cross-tag metadata assembly faults) and caches the
// result for Tags/Metadata/Errs, until the next mutation invalidates it.
func (d *Document) ensureCache() {
	if d.cacheValid {
		return
	}
	var errs []error
	for _, e := range d.file.Errs {
		errs = append(errs, e)
	}
	tags, exErrs := extract(d.file)
	errs = append(errs, exErrs...)
	meta, mErrs := fromTags(tags)
	errs = append(errs, mErrs...)

	d.tags, d.meta, d.errs, d.cacheValid = tags, meta, errs, true
}

// Tags returns the recognized tags as a read-only snapshot, recomputed from
// the retained tree the first time it's asked for after Read or after any
// SetTag/RemoveTag/StripAll.
func (d *Document) Tags() []Tag {
	d.ensureCache()
	return d.tags
}

// Metadata folds the current tags into a Metadata. Read-only: there is no
// SetMetadata, since Metadata is a curated projection (only the fields it
// models) - writing it back from scratch would drop MakerNote/thumbnail/
// unknown tags. Edit via SetTag/RemoveTag instead.
func (d *Document) Metadata() *Metadata {
	d.ensureCache()
	return d.meta
}

// Errs are the best-effort faults observed the last time the tree was
// walked: structural TIFF parse faults, unrecognized-value tag faults, and
// cross-tag metadata assembly faults (e.g. a lone GPS coordinate, an
// unparseable datetime). Recomputed the same way as Tags/Metadata, so a
// mutation that resolves a fault (e.g. RemoveTag on the lone GPS coordinate)
// clears it here too.
func (d *Document) Errs() []error {
	d.ensureCache()
	return d.errs
}

// SetTag validates value against name's expected Go type and applies it to
// the retained tree.
func (d *Document) SetTag(name TagName, value any) error {
	edits, err := buildTag(name, value)
	if err != nil {
		return err
	}
	order := d.file.Order
	for _, e := range edits {
		setEntry(d.file.Root, e.id, e.build(order))
	}
	d.cacheValid = false
	return nil
}

// RemoveTag removes name from the retained tree, pruning any sub-IFD left
// empty. A name that isn't currently present is a no-op, not an error.
func (d *Document) RemoveTag(name TagName) error {
	ids, ok := settable[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTag, name)
	}
	for _, id := range ids {
		removeEntry(d.file.Root, id)
	}
	d.cacheValid = false
	return nil
}

// StripAll empties the tree, removing every tag (recognized and unrecognized
// alike) - the "no EXIF at all" write.
func (d *Document) StripAll() {
	stripAll(d.file.Root)
	d.cacheValid = false
}

// Write re-encodes the retained tree to w. For a JPEG/PNG/WebP host it
// splices the result back into the original container via container.Inject;
// for a standalone TIFF - which has no host to splice into - it writes the
// encoded bytes directly.
func (d *Document) Write(w io.Writer) ([]tiff.Warning, error) {
	if d.loc.Format() == container.FormatTIFF {
		b, warns, err := d.file.Encode()
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(b); err != nil {
			return nil, err
		}
		return warns, nil
	}

	// An empty tree means "no EXIF section should remain" - the same
	// Inject(blob=nil) call deletes it if present, or no-ops if it was
	// already absent.
	if treeEmpty(d.file.Root) {
		return nil, container.Inject(w, d.original, d.size, d.loc, nil)
	}

	if !d.loc.HasEXIF() && !d.loc.CanAddEXIF() {
		return nil, ErrCannotAddEXIF
	}

	encoded, warns, err := d.file.Encode()
	if err != nil {
		return nil, err
	}
	if err := container.Inject(w, d.original, d.size, d.loc, encoded); err != nil {
		return nil, err
	}
	return warns, nil
}

// treeEmpty reports whether an IFD carries nothing worth keeping: no entries,
// no sub-IFDs, no chained IFD1 (thumbnail).
func treeEmpty(ifd *tiff.IFD) bool {
	return ifd == nil || (len(ifd.Entries) == 0 && len(ifd.Subs) == 0 && ifd.Next == nil)
}

// --- typed tag builders (the SetTag counterpart to mutate.go's tree engine;
// kept separate since it's about validating/encoding a single typed value,
// not walking the tree) --------------------------------------------------

// buildTag validates value against name's expected Go type and returns the
// pending edits to apply, mirroring exactly the type Tags() would report for
// that name (see Tag's doc comment) - what you read is what you write back.
func buildTag(name TagName, value any) ([]pendingEdit, error) {
	ids, ok := settable[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTag, name)
	}

	switch name {
	case TagMake, TagModel, TagSoftware, TagLensModel, TagOffsetTimeOriginal:
		s, ok := value.(string)
		if !ok {
			return nil, typeErr(name, "string", value)
		}
		return asciiEdit(ids[0], s), nil

	case TagDateTime, TagDateTimeOriginal:
		tm, ok := value.(time.Time)
		if !ok {
			return nil, typeErr(name, "time.Time", value)
		}
		return asciiEdit(ids[0], tm.Format(dateTimeLayout)), nil

	case TagOrientation:
		o, ok := value.(Orientation)
		if !ok {
			return nil, typeErr(name, "exif.Orientation", value)
		}
		if o < 1 || o > 8 {
			return nil, fmt.Errorf("%s: out of range 1..8: %d", name, o)
		}
		return shortEdit(ids[0], uint16(o)), nil

	case TagISO:
		v, ok := value.(uint16)
		if !ok {
			return nil, typeErr(name, "uint16", value)
		}
		return shortEdit(ids[0], v), nil

	case TagPixelXDimension, TagPixelYDimension:
		v, ok := value.(uint32)
		if !ok {
			return nil, typeErr(name, "uint32", value)
		}
		return longEdit(ids[0], v), nil

	case TagExposureTime, TagFNumber, TagFocalLength:
		r, ok := value.(tiff.Rational)
		if !ok {
			return nil, typeErr(name, "tiff.Rational", value)
		}
		return rationalEdit(ids[0], r), nil

	case TagGPSLatitude:
		f, ok := value.(float64)
		if !ok {
			return nil, typeErr(name, "float64 (signed decimal degrees)", value)
		}
		return gpsCoordEdit(ids[0], ids[1], f, "N", "S"), nil

	case TagGPSLongitude:
		f, ok := value.(float64)
		if !ok {
			return nil, typeErr(name, "float64 (signed decimal degrees)", value)
		}
		return gpsCoordEdit(ids[0], ids[1], f, "E", "W"), nil

	case TagGPSAltitude:
		f, ok := value.(float64)
		if !ok {
			return nil, typeErr(name, "float64 (signed meters)", value)
		}
		return gpsAltEdit(ids[0], ids[1], f), nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownTag, name)
	}
}

func typeErr(name TagName, want string, got any) error {
	return fmt.Errorf("%s: expected %s, got %T", name, want, got)
}

func asciiEdit(id uint16, s string) []pendingEdit {
	raw := append([]byte(s), 0)
	return []pendingEdit{{id, func(binary.ByteOrder) tiff.Entry {
		return tiff.Entry{Tag: id, Type: tiff.TypeASCII, Count: uint32(len(raw)), Raw: raw}
	}}}
}

func shortEdit(id uint16, v uint16) []pendingEdit {
	return []pendingEdit{{id, func(o binary.ByteOrder) tiff.Entry {
		raw := make([]byte, 2)
		o.PutUint16(raw, v)
		return tiff.Entry{Tag: id, Type: tiff.TypeShort, Count: 1, Raw: raw}
	}}}
}

func longEdit(id uint16, v uint32) []pendingEdit {
	return []pendingEdit{{id, func(o binary.ByteOrder) tiff.Entry {
		raw := make([]byte, 4)
		o.PutUint32(raw, v)
		return tiff.Entry{Tag: id, Type: tiff.TypeLong, Count: 1, Raw: raw}
	}}}
}

func rationalEdit(id uint16, r tiff.Rational) []pendingEdit {
	return []pendingEdit{{id, func(o binary.ByteOrder) tiff.Entry {
		raw := make([]byte, 8)
		putRat(o, raw, r.Num, r.Denom)
		return tiff.Entry{Tag: id, Type: tiff.TypeRational, Count: 1, Raw: raw}
	}}}
}

// gpsCoordEdit converts an already-typed decimal-degree value into the
// deg/min/sec RATIONAL triplet plus its N/S or E/W reference tag.
func gpsCoordEdit(coordID, refID uint16, deg float64, pos, neg string) []pendingEdit {
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
	}
}

// gpsAltEdit converts an already-typed signed-meters value into a RATIONAL
// plus its below/above-sea-level reference byte.
func gpsAltEdit(altID, refID uint16, meters float64) []pendingEdit {
	var ref byte
	if meters < 0 {
		ref, meters = 1, -meters // ref byte 1 = below sea level
	}
	cm := uint32(math.Round(meters * 100))
	return []pendingEdit{
		{altID, func(o binary.ByteOrder) tiff.Entry {
			raw := make([]byte, 8)
			putRat(o, raw, cm, 100)
			return tiff.Entry{Tag: altID, Type: tiff.TypeRational, Count: 1, Raw: raw}
		}},
		{refID, func(binary.ByteOrder) tiff.Entry {
			return tiff.Entry{Tag: refID, Type: tiff.TypeByte, Count: 1, Raw: []byte{ref}}
		}},
	}
}
