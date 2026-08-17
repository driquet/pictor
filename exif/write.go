package exif

import "github.com/driquet/pictor/tiff"

// writeOpts are the decode options the RMW path needs beyond subIFDOpt:
// pixel/tile/thumbnail data pulled into the tree (so Encode can rebuild the
// file from the tree alone) and MakerNote treated as an opaque, pinned blob.
func writeOpts() []tiff.Option {
	return []tiff.Option{
		subIFDOpt(),
		tiff.WithImageData(),
		tiff.WithOpaqueTags(tagMakerNote),
		tiff.WithImmovableTags(tagMakerNote),
	}
}

// SetTIFFBytes applies NAME=VALUE assignments to a standalone TIFF/EXIF blob
// and re-encodes it: decode → mutate → tiff.Encode. Fails fast on a bad
// assignment before touching the tree; tiff.Encode itself refuses a source
// with a structural decode fault (data it can't faithfully round-trip).
func SetTIFFBytes(b []byte, kvs []string) ([]byte, []tiff.Warning, error) {
	mutate, err := Set(kvs)
	if err != nil {
		return nil, nil, err
	}
	return applyTIFFBytes(b, mutate)
}

// StripTIFFBytes removes the named tags (or all EXIF when names is empty)
// from a standalone TIFF/EXIF blob and re-encodes it.
func StripTIFFBytes(b []byte, names []string) ([]byte, []tiff.Warning, error) {
	mutate, err := Strip(names)
	if err != nil {
		return nil, nil, err
	}
	return applyTIFFBytes(b, mutate)
}

func applyTIFFBytes(b []byte, mutate func(*tiff.File)) ([]byte, []tiff.Warning, error) {
	f, err := tiff.DecodeBytes(b, writeOpts()...)
	if err != nil {
		return nil, nil, err
	}
	mutate(f)
	return f.Encode()
}
