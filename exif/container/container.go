// Package container locates the raw EXIF/TIFF blob inside an image file.
//
// Locate is the entry point: it sniffs the container format (JPEG, PNG, WebP,
// or bare TIFF), walks its structure, and returns a reader over the embedded
// EXIF blob for the exif package to decode. It imports nothing from exif or
// tiff (a one-way dependency); container knows nothing about tag meaning, only
// where the blob sits in each host format.
package container

import (
	"errors"
	"io"
)

// Format is a recognized container format.
type Format uint8

const (
	FormatJPEG Format = iota + 1
	FormatTIFF
	FormatPNG
	FormatWebP
)

// ErrUnknownFormat is returned when the magic bytes match none of the four
// supported containers: a clean refusal, distinct from a malformed-EXIF parse.
var ErrUnknownFormat = errors.New("container: unknown format")

// ErrNoExif is returned when the container is recognized but holds no EXIF
// section. Callers treat it as benign absence, not corruption.
var ErrNoExif = errors.New("container: no EXIF section found")

// Location describes where the EXIF blob sits within the container and carries
// the byte offsets the write path (Inject) needs, so a write never re-walks the
// container. Its fields are unexported; the read path uses only the returned
// blob.
type Location struct {
	format             Format
	blobStart, blobLen int64

	// Write-side descriptor, populated by the locate walk.
	present                bool  // an EXIF region was found
	regionStart, regionEnd int64 // on-disk region to replace/delete (incl. header)
	insertAt               int64 // where a new EXIF region goes when absent
	vp8xFlags              int64 // WebP: file offset of the VP8X flags byte (0 if n/a)
}

// Format returns the container format the Location was found in.
func (l Location) Format() Format { return l.format }

// HasEXIF reports whether the container already holds an EXIF section.
func (l Location) HasEXIF() bool { return l.present }

// CanAddEXIF reports whether a new EXIF section can be inserted. True for
// TIFF (which always already has one; there's nothing to add) and JPEG/PNG,
// false for a simple (non-VP8X) WebP, which needs a VP8X upgrade this version
// doesn't do.
func (l Location) CanAddEXIF() bool {
	if l.format == FormatWebP {
		return l.vp8xFlags > 0
	}
	return l.format == FormatJPEG || l.format == FormatPNG || l.format == FormatTIFF
}

// Locate detects the container and returns an io.ReaderAt over the raw TIFF
// blob (byte 0 = the TIFF header), its size, and a Location descriptor.
// Returns ErrUnknownFormat if the head matches no supported format. When the
// container is recognized but holds no EXIF, it returns ErrNoExif together with
// a Location whose insert point (and WebP flags offset) are filled so Inject
// can still add a section.
func Locate(r io.ReaderAt, size int64) (io.ReaderAt, int64, Location, error) {
	f, err := detectFormat(r)
	if err != nil {
		return nil, 0, Location{}, err
	}
	var loc Location
	loc.format = f
	switch f {
	case FormatJPEG:
		err = locateJPEG(r, size, &loc)
	case FormatPNG:
		err = locatePNG(r, size, &loc)
	case FormatWebP:
		err = locateWebP(r, size, &loc)
	case FormatTIFF:
		err = locateTIFF(size, &loc)
	}
	if err != nil {
		return nil, 0, loc, err
	}
	return io.NewSectionReader(r, loc.blobStart, loc.blobLen), loc.blobLen, loc, nil
}

// detectFormat sniffs the <=12-byte magic.
func detectFormat(r io.ReaderAt) (Format, error) {
	head := make([]byte, 12)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	switch {
	case n >= 2 && head[0] == 0xFF && head[1] == 0xD8:
		return FormatJPEG, nil
	case n >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return FormatPNG, nil
	case n >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return FormatWebP, nil
	case n >= 4 && head[0] == 'I' && head[1] == 'I' && head[2] == 0x2A && head[3] == 0x00:
		return FormatTIFF, nil
	case n >= 4 && head[0] == 'M' && head[1] == 'M' && head[2] == 0x00 && head[3] == 0x2A:
		return FormatTIFF, nil
	default:
		return 0, ErrUnknownFormat
	}
}
