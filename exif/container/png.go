package container

import (
	"encoding/binary"
	"io"
)

// locatePNG walks chunks (Length|Type|Data|CRC) until eXIf or IEND, filling
// loc. The eXIf data is a bare TIFF stream. On ErrNoExif loc.insertAt points
// after IHDR, the slot a new eXIf chunk goes.
func locatePNG(r io.ReaderAt, size int64, loc *Location) error {
	off := int64(8) // skip the 8-byte signature
	loc.insertAt = 8
	hdr := make([]byte, 8)
	for off+8 <= size {
		if _, err := r.ReadAt(hdr, off); err != nil {
			return ErrNoExif
		}
		dataLen := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		data := off + 8
		if data+dataLen+4 > size { // +4 CRC
			return ErrNoExif
		}
		chunkEnd := data + dataLen + 4
		if typ == "eXIf" {
			loc.present = true
			loc.blobStart = data
			loc.blobLen = dataLen
			loc.regionStart = off
			loc.regionEnd = chunkEnd
			return nil
		}
		if typ == "IHDR" {
			loc.insertAt = chunkEnd
		}
		if typ == "IEND" {
			return ErrNoExif
		}
		off = chunkEnd
	}
	return ErrNoExif
}
