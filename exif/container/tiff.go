package container

// locateTIFF: a native TIFF file is the EXIF blob. The TIFF header sits at
// byte 0, so the whole file is the section the parser sees. TIFF write is
// unsupported (the facade errors), so no insert/region offsets are needed.
func locateTIFF(size int64, loc *Location) error {
	loc.present = true
	loc.blobStart = 0
	loc.blobLen = size
	return nil
}
