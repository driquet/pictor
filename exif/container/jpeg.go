package container

import (
	"encoding/binary"
	"io"
)

// exifPrefix is the 6-byte JPEG-only identifier before the TIFF header.
var exifPrefix = []byte("Exif\x00\x00")

// locateJPEG walks APP marker segments until APP1/Exif or SOS/EOI, filling loc.
// On success blobStart is the TIFF-header start (after the 6-byte prefix); on
// ErrNoExif loc.insertAt points after SOI (or a leading JFIF APP0).
func locateJPEG(r io.ReaderAt, size int64, loc *Location) error {
	off := int64(2) // skip SOI (FF D8)
	loc.insertAt = 2
	hdr := make([]byte, 4)
	for off+2 <= size {
		if _, err := r.ReadAt(hdr[:2], off); err != nil {
			return ErrNoExif
		}
		if hdr[0] != 0xFF {
			return ErrNoExif
		}
		marker := hdr[1]
		switch {
		case marker == 0xFF: // fill byte, advance one
			off++
			continue
		case marker == 0xD9 || marker == 0xDA: // EOI / SOS, no EXIF ahead
			return ErrNoExif
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01: // standalone
			off += 2
			continue
		}
		// Segment with a 2-byte length (includes the length bytes, not marker).
		if _, err := r.ReadAt(hdr[:4], off); err != nil {
			return ErrNoExif
		}
		segLen := int64(binary.BigEndian.Uint16(hdr[2:4]))
		if segLen < 2 {
			return ErrNoExif // forward-progress guard
		}
		payload := off + 4
		payloadLen := segLen - 2
		if payload+payloadLen > size {
			return ErrNoExif
		}
		if marker == 0xE1 && payloadLen >= 6 {
			pre := make([]byte, 6)
			if _, err := r.ReadAt(pre, payload); err == nil && string(pre) == string(exifPrefix) {
				loc.present = true
				loc.blobStart = payload + 6
				loc.blobLen = payloadLen - 6
				loc.regionStart = off
				loc.regionEnd = payload + payloadLen
				return nil
			}
		}
		// A leading JFIF APP0 is the conventional slot to insert EXIF after.
		if marker == 0xE0 && off == 2 {
			loc.insertAt = payload + payloadLen
		}
		off = payload + payloadLen
	}
	return ErrNoExif
}
