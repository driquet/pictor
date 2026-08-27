package container

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

// ErrExifTooLarge is returned when a JPEG EXIF segment would exceed the 64 KB
// APP1 limit (JPEG stores the segment length in two bytes).
var ErrExifTooLarge = errors.New("container: EXIF segment exceeds JPEG 64 KB limit")

// webpExifFlag is the VP8X flags bit marking an EXIF chunk (libwebp EXIF_FLAG).
const webpExifFlag = 0x08

// Inject writes src to dst with blob spliced into the EXIF region described by
// loc, applying the per-format fixups (JPEG segment length, PNG CRC, WebP RIFF
// size + VP8X EXIF flag). blob == nil deletes the region entirely. It is bytes
// only, no tree knowledge; the caller already turned the tiff.File back into
// blob via (*tiff.File).Encode.
//
// The trio (JPEG/PNG/WebP) is supported; loc for other formats is rejected.
func Inject(dst io.Writer, src io.ReaderAt, size int64, loc Location, blob []byte) error {
	if loc.format != FormatJPEG && loc.format != FormatPNG && loc.format != FormatWebP {
		return errors.New("container: inject unsupported for this format")
	}
	region, err := buildRegion(loc.format, blob)
	if err != nil {
		return err
	}

	var start, end int64
	switch {
	case loc.present:
		start, end = loc.regionStart, loc.regionEnd
	case blob != nil:
		start, end = loc.insertAt, loc.insertAt // insert: zero-width region
	default:
		// Delete requested but nothing present: copy through unchanged.
		start, end = 0, 0
	}
	// Defensive clamp: never let a splice range escape the buffer.
	if start > size {
		start = size
	}
	if end > size {
		end = size
	}
	if start > end {
		start = end
	}

	in := make([]byte, size)
	if _, err := src.ReadAt(in, 0); err != nil && err != io.EOF {
		return err
	}

	out := make([]byte, 0, size-(end-start)+int64(len(region)))
	out = append(out, in[:start]...)
	out = append(out, region...)
	out = append(out, in[end:]...)

	if loc.format == FormatWebP {
		fixWebP(out, loc, blob != nil)
	}

	_, err = dst.Write(out)
	return err
}

// buildRegion assembles the new on-disk EXIF segment/chunk from a bare TIFF
// blob, or nil when blob is nil (delete).
func buildRegion(f Format, blob []byte) ([]byte, error) {
	if blob == nil {
		return nil, nil
	}
	switch f {
	case FormatJPEG:
		payload := len(exifPrefix) + len(blob)
		if payload+2 > 0xFFFF {
			return nil, ErrExifTooLarge
		}
		seg := make([]byte, 4+payload)
		seg[0], seg[1] = 0xFF, 0xE1
		binary.BigEndian.PutUint16(seg[2:4], uint16(payload+2))
		n := copy(seg[4:], exifPrefix)
		copy(seg[4+n:], blob)
		return seg, nil
	case FormatPNG:
		chunk := make([]byte, 8+len(blob)+4)
		binary.BigEndian.PutUint32(chunk[:4], uint32(len(blob)))
		copy(chunk[4:8], "eXIf")
		copy(chunk[8:], blob)
		crc := crc32.ChecksumIEEE(chunk[4 : 8+len(blob)]) // over type + data
		binary.BigEndian.PutUint32(chunk[8+len(blob):], crc)
		return chunk, nil
	case FormatWebP:
		pad := len(blob) % 2
		chunk := make([]byte, 8+len(blob)+pad)
		copy(chunk[:4], "EXIF")
		binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(blob)))
		copy(chunk[8:], blob)
		return chunk, nil
	}
	return nil, nil
}

// fixWebP rewrites the RIFF file size and toggles the VP8X EXIF flag. VP8X
// precedes the EXIF region, so its offset is unshifted by the splice.
func fixWebP(out []byte, loc Location, has bool) {
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	if loc.vp8xFlags > 0 && int(loc.vp8xFlags) < len(out) {
		if has {
			out[loc.vp8xFlags] |= webpExifFlag
		} else {
			out[loc.vp8xFlags] &^= webpExifFlag
		}
	}
}
