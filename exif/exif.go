// Package exif reads and writes EXIF metadata on top of the format-agnostic
// tiff codec, for standalone TIFF as well as JPEG/PNG/WebP hosts.
//
// Document is the package's one entry point: Read/ReadBytes decode an image
// into a Document, Tags/Metadata project its current state into a flat tag
// list or a curated typed struct, and SetTag/RemoveTag/StripAll/Write mutate
// it and re-encode. See Document's doc comment for the full picture.
package exif

import "github.com/driquet/pictor/tiff"

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
