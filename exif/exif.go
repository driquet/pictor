// Package exif reads EXIF metadata from TIFF-structured image data on top of the
// standalone tiff codec.
//
// It is the entry point for the EXIF domain: it declares the EXIF sub-IFD
// pointer tags the format-agnostic tiff engine needs, drives tiff.Decode, then
// runs the two read stages: Extract turns the parsed IFD tree into a flat list
// of recognized [Tag]s (tag table + per-tag value conversion), and FromTags
// folds those into the typed [Metadata] struct. Reads are best-effort: an
// unrecognized or malformed tag is skipped and reported in the returned []error,
// never aborting the whole read.
//
// This slice covers standalone TIFF. Host-format dispatch (JPEG/PNG/WebP via the
// container package) is layered on at ReadFile once those slices land.
package exif

import (
	"io"

	"github.com/driquet/pictor/tiff"
)

// EXIF sub-IFD pointer tags. These are EXIF (CIPA DC-008) semantics, not TIFF;
// exif owns them and hands them to the tiff engine via WithSubIFDTags so the
// codec recurses them without knowing what they mean.
const (
	tagExifIFD    uint16 = 0x8769
	tagGPSIFD     uint16 = 0x8825
	tagInteropIFD uint16 = 0xA005
)

// subIFDTags is the set exif declares to the tiff decoder.
func subIFDOpt() tiff.Option { return tiff.WithSubIFDTags(tagExifIFD, tagGPSIFD, tagInteropIFD) }

// ReadTIFF parses a standalone TIFF/EXIF blob end to end: tiff.Decode → Extract
// → FromTags. Best-effort: a valid TIFF yields a non-nil Metadata even if some
// tags fault; faults (parse + extract) are returned together.
func ReadTIFF(r io.ReaderAt, size int64) (*Metadata, []error) {
	f, err := tiff.Decode(r, size, subIFDOpt())
	if err != nil {
		return &Metadata{}, []error{err}
	}
	return fromFile(f)
}

// ReadTIFFBytes is the in-memory variant of ReadTIFF.
func ReadTIFFBytes(b []byte) (*Metadata, []error) {
	f, err := tiff.DecodeBytes(b, subIFDOpt())
	if err != nil {
		return &Metadata{}, []error{err}
	}
	return fromFile(f)
}

// ReadTags parses a standalone TIFF/EXIF blob and returns every recognized
// tag, flat and grouped (Extract's output), without folding into Metadata.
// Same decode path as ReadTIFF - useful for a generic "every tag" debug view
// that Metadata's curated fields don't cover.
func ReadTags(r io.ReaderAt, size int64) ([]Tag, []error) {
	f, err := tiff.Decode(r, size, subIFDOpt())
	if err != nil {
		return nil, []error{err}
	}
	return extractTags(f)
}

// ReadTagsBytes is the in-memory variant of ReadTags.
func ReadTagsBytes(b []byte) ([]Tag, []error) {
	f, err := tiff.DecodeBytes(b, subIFDOpt())
	if err != nil {
		return nil, []error{err}
	}
	return extractTags(f)
}

// extractTags runs Extract over an already-parsed tiff.File, carrying the
// parse faults through.
func extractTags(f *tiff.File) ([]Tag, []error) {
	var errs []error
	for _, e := range f.Errs {
		errs = append(errs, e)
	}
	tags, exErrs := Extract(f)
	errs = append(errs, exErrs...)
	return tags, errs
}

// fromFile runs the two read stages over an already-parsed tiff.File, carrying
// the parse faults through.
func fromFile(f *tiff.File) (*Metadata, []error) {
	tags, errs := extractTags(f)
	m, mErrs := FromTags(tags)
	errs = append(errs, mErrs...)
	return m, errs
}
