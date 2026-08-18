package exif

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/driquet/pictor/tiff"
)

// SupportedExtensions lists the file extensions ReadTIFF can be pointed at
// directly (a standalone TIFF blob is itself the file).
var SupportedExtensions = []string{".tif", ".tiff"}

// Format renders a Tag's already-typed Value as display text, for a generic
// "every recognized tag" debug view (unlike Metadata, which curates a fixed
// set of fields). Value's dynamic type is whatever Extract's tag table
// produced for it: string, uint8/16/32, Orientation, tiff.Rational, or
// []tiff.Rational (an undivided GPS deg/min/sec triplet).
func (t Tag) Format() string {
	switch v := t.Value.(type) {
	case string:
		return v
	case Orientation:
		return v.String()
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case tiff.Rational:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	case []tiff.Rational:
		parts := make([]string, len(v))
		for i, r := range v {
			parts[i] = strconv.FormatFloat(r.Float64(), 'g', -1, 64)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}
