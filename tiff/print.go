package tiff

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SupportedExtensions lists the file extensions Decode can be pointed at
// directly (a standalone TIFF blob is itself the file).
var SupportedExtensions = []string{".tif", ".tiff"}

// maxGenericValues caps how many elements Describe prints for a tag it has no
// curated formatter for, so a huge array (e.g. StripOffsets) doesn't flood the
// output.
const maxGenericValues = 8

// tagDef is a curated baseline TIFF 6.0 tag: a friendly name plus how to
// render its value. This is a small, hand-picked set for a useful debug view,
// not the full baseline registry - anything else falls back to a generic
// type-based decode in Describe.
type tagDef struct {
	name   string
	format func(binary.ByteOrder, Entry) string
}

// baseTags are the curated tags Describe recognizes. Baseline TIFF 6.0 plus
// the three well-known EXIF sub-IFD pointer tags - labeled here for a
// pure-structural dump, never recursed (that's the exif package's job).
var baseTags = map[uint16]tagDef{
	0x00FE: {"NewSubfileType", fmtUint},
	0x0100: {"ImageWidth", fmtUint},
	0x0101: {"ImageLength", fmtUint},
	0x0102: {"BitsPerSample", fmtUintArray},
	0x0103: {"Compression", fmtCompression},
	0x0106: {"PhotometricInterpretation", fmtPhotometric},
	0x010A: {"FillOrder", fmtUint},
	0x010D: {"DocumentName", fmtASCII},
	0x010E: {"ImageDescription", fmtASCII},
	0x010F: {"Make", fmtASCII},
	0x0110: {"Model", fmtASCII},
	0x0111: {"StripOffsets", genericValue},
	0x0112: {"Orientation", fmtOrientation},
	0x0115: {"SamplesPerPixel", fmtUint},
	0x0116: {"RowsPerStrip", fmtUint},
	0x0117: {"StripByteCounts", genericValue},
	0x011A: {"XResolution", fmtRational},
	0x011B: {"YResolution", fmtRational},
	0x011C: {"PlanarConfiguration", fmtUint},
	0x0128: {"ResolutionUnit", fmtResolutionUnit},
	0x0129: {"PageNumber", fmtUintArray},
	0x0131: {"Software", fmtASCII},
	0x0132: {"DateTime", fmtASCII},
	0x013D: {"Predictor", fmtUint},
	0x013E: {"WhitePoint", genericValue},
	0x013F: {"PrimaryChromaticities", genericValue},
	0x0140: {"ColorMap", genericValue},
	0x0142: {"TileWidth", fmtUint},
	0x0143: {"TileLength", fmtUint},
	0x0144: {"TileOffsets", genericValue},
	0x0145: {"TileByteCounts", genericValue},
	0x014C: {"InkSet", fmtUint},
	0x0152: {"ExtraSamples", fmtUintArray},
	0x0153: {"SampleFormat", fmtUintArray},
	0x8769: {"Exif IFD Pointer", fmtPointer},
	0x8825: {"GPS IFD Pointer", fmtPointer},
	0xA005: {"Interop IFD Pointer", fmtPointer},
	0x830E: {"ModelPixelScaleTag", genericValue},
	0x8482: {"ModelTiepointTag", genericValue},
	0x87AF: {"GeoKeyDirectoryTag", genericValue},
	0x87B1: {"GeoAsciiParamsTag", fmtASCII},
}

// Describe renders one entry as a (name, value) pair for display. A curated
// tag gets its friendly name and a semantic value (e.g. an enum label); an
// unrecognized tag gets its hex id and a generic type-based decode.
func Describe(order binary.ByteOrder, e Entry) (name, value string) {
	if d, ok := baseTags[e.Tag]; ok {
		return d.name, d.format(order, e)
	}
	return fmt.Sprintf("0x%04X", e.Tag), genericValue(order, e)
}

// --- curated formatters --------------------------------------------------

func fmtASCII(_ binary.ByteOrder, e Entry) string {
	s := string(e.Raw)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func fmtUint(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	return strconv.FormatUint(v, 10)
}

func fmtUintArray(order binary.ByteOrder, e Entry) string {
	sz := int(e.Type.size())
	if sz == 0 {
		return genericValue(order, e)
	}
	n := len(e.Raw) / sz
	parts := make([]string, 0, n)
	for i := range n {
		v, ok := uintAt(order, e.Type, e.Raw, i*sz)
		if !ok {
			return genericValue(order, e)
		}
		parts = append(parts, strconv.FormatUint(v, 10))
	}
	return strings.Join(parts, ", ")
}

func fmtRational(order binary.ByteOrder, e Entry) string {
	if e.Type != TypeRational || len(e.Raw) < 8 {
		return genericValue(order, e)
	}
	r := Rational{Num: order.Uint32(e.Raw[0:4]), Denom: order.Uint32(e.Raw[4:8])}
	return strconv.FormatFloat(r.Float64(), 'g', -1, 64)
}

func fmtPointer(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	return fmt.Sprintf("offset %d", v)
}

func fmtCompression(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	switch v {
	case 1:
		return "Uncompressed"
	case 5:
		return "LZW"
	case 6:
		return "JPEG (old-style)"
	case 7:
		return "JPEG"
	case 8:
		return "Deflate"
	case 32773:
		return "PackBits"
	default:
		return fmt.Sprintf("Unknown (%d)", v)
	}
}

func fmtPhotometric(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	switch v {
	case 0:
		return "WhiteIsZero"
	case 1:
		return "BlackIsZero"
	case 2:
		return "RGB"
	case 3:
		return "Palette color"
	case 4:
		return "Transparency mask"
	case 5:
		return "CMYK"
	case 6:
		return "YCbCr"
	case 8:
		return "CIELab"
	default:
		return fmt.Sprintf("Unknown (%d)", v)
	}
}

func fmtResolutionUnit(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	switch v {
	case 1:
		return "None"
	case 2:
		return "Inch"
	case 3:
		return "Centimeter"
	default:
		return fmt.Sprintf("Unknown (%d)", v)
	}
}

func fmtOrientation(order binary.ByteOrder, e Entry) string {
	v, ok := uintAt(order, e.Type, e.Raw, 0)
	if !ok {
		return genericValue(order, e)
	}
	switch v {
	case 1:
		return "Normal"
	case 2:
		return "Mirror horizontal"
	case 3:
		return "Rotate 180°"
	case 4:
		return "Mirror vertical"
	case 5:
		return "Mirror horizontal, rotate 270° CW"
	case 6:
		return "Rotate 90° CW"
	case 7:
		return "Mirror horizontal, rotate 90° CW"
	case 8:
		return "Rotate 270° CW"
	default:
		return fmt.Sprintf("Unknown (%d)", v)
	}
}

// --- generic fallback ------------------------------------------------------

// genericValue decodes a tag Describe has no curated formatter for, purely
// from its declared TIFF Type - no tag semantics needed. Opaque values and
// types with no known size (Undefined, or a type this codec doesn't
// recognize) print as a binary-data marker; everything else decodes
// element-by-element, capped at maxGenericValues.
func genericValue(order binary.ByteOrder, e Entry) string {
	if e.Opaque || e.Type == TypeUndefined || e.Type.size() == 0 {
		return fmt.Sprintf("(Binary data, %d bytes)", len(e.Raw))
	}
	if e.Type == TypeASCII {
		return fmtASCII(order, e)
	}

	sz := int(e.Type.size())
	n := len(e.Raw) / sz
	more := 0
	if n > maxGenericValues {
		more = n - maxGenericValues
		n = maxGenericValues
	}

	parts := make([]string, 0, n)
	for i := range n {
		parts = append(parts, elementAt(order, e.Type, e.Raw, i*sz))
	}
	out := strings.Join(parts, ", ")
	if more > 0 {
		out += fmt.Sprintf(", ... (%d more)", more)
	}
	return out
}

// uintAt reads the element at byte offset off as an unsigned integer, for
// types that are naturally unsigned/whole-numbered. ok is false for
// rationals, floats, and signed types, where the caller should fall back to
// genericValue instead of misrepresenting the value.
func uintAt(order binary.ByteOrder, t Type, raw []byte, off int) (uint64, bool) {
	switch t {
	case TypeByte:
		if off >= len(raw) {
			return 0, false
		}
		return uint64(raw[off]), true
	case TypeShort:
		if off+2 > len(raw) {
			return 0, false
		}
		return uint64(order.Uint16(raw[off : off+2])), true
	case TypeLong, TypeIFD:
		if off+4 > len(raw) {
			return 0, false
		}
		return uint64(order.Uint32(raw[off : off+4])), true
	default:
		return 0, false
	}
}

// elementAt renders the single element of type t at byte offset off in raw.
func elementAt(order binary.ByteOrder, t Type, raw []byte, off int) string {
	switch t {
	case TypeByte:
		return strconv.FormatUint(uint64(raw[off]), 10)
	case TypeSByte:
		return strconv.FormatInt(int64(int8(raw[off])), 10)
	case TypeShort:
		return strconv.FormatUint(uint64(order.Uint16(raw[off:off+2])), 10)
	case TypeSShort:
		return strconv.FormatInt(int64(int16(order.Uint16(raw[off:off+2]))), 10)
	case TypeLong, TypeIFD:
		return strconv.FormatUint(uint64(order.Uint32(raw[off:off+4])), 10)
	case TypeSLong:
		return strconv.FormatInt(int64(int32(order.Uint32(raw[off:off+4]))), 10)
	case TypeRational:
		return Rational{Num: order.Uint32(raw[off : off+4]), Denom: order.Uint32(raw[off+4 : off+8])}.String()
	case TypeSRational:
		return SRational{Num: int32(order.Uint32(raw[off : off+4])), Denom: int32(order.Uint32(raw[off+4 : off+8]))}.String()
	case TypeFloat:
		return strconv.FormatFloat(float64(math.Float32frombits(order.Uint32(raw[off:off+4]))), 'g', -1, 32)
	case TypeDouble:
		return strconv.FormatFloat(math.Float64frombits(order.Uint64(raw[off:off+8])), 'g', -1, 64)
	default:
		return fmt.Sprintf("%x", raw[off:])
	}
}
