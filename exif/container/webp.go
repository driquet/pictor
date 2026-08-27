package container

import (
	"encoding/binary"
	"io"
)

// locateWebP walks RIFF chunks (FourCC|Size|payload, padded to even) after the
// 12-byte RIFF/WEBP header until the EXIF chunk, filling loc. It also records
// the VP8X flags-byte offset so Inject can toggle the EXIF flag. On ErrNoExif
// loc.insertAt is end-of-file (EXIF trails the image data).
func locateWebP(r io.ReaderAt, size int64, loc *Location) error {
	off := int64(12)
	loc.insertAt = size
	hdr := make([]byte, 8)
	for off+8 <= size {
		if _, err := r.ReadAt(hdr, off); err != nil {
			return ErrNoExif
		}
		fourcc := string(hdr[:4])
		chunkLen := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		payload := off + 8
		if payload+chunkLen > size {
			return ErrNoExif
		}
		if fourcc == "VP8X" && chunkLen >= 1 {
			loc.vp8xFlags = payload // flags live in the first VP8X payload byte
		}
		if fourcc == "EXIF" {
			loc.present = true
			loc.blobStart = payload
			loc.blobLen = chunkLen
			loc.regionStart = off
			end := payload + chunkLen
			if chunkLen%2 == 1 && end < size {
				end++ // include the even-byte pad, when the file actually has one
			}
			loc.regionEnd = end
			return nil
		}
		off = payload + chunkLen
		if chunkLen%2 == 1 {
			off++ // even-byte padding, not counted in Size
		}
	}
	return ErrNoExif
}
