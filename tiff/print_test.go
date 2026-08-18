package tiff

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDescribeCurated(t *testing.T) {
	order := binary.LittleEndian

	name, value := Describe(order, Entry{Tag: 0x0100, Type: TypeLong, Count: 1, Raw: []byte{100, 0, 0, 0}})
	assert.Equal(t, "ImageWidth", name)
	assert.Equal(t, "100", value)

	name, value = Describe(order, Entry{Tag: 0x0103, Type: TypeShort, Count: 1, Raw: short(order, 5)})
	assert.Equal(t, "Compression", name)
	assert.Equal(t, "LZW", value)

	name, value = Describe(order, Entry{Tag: 0x8769, Type: TypeLong, Count: 1, Raw: []byte{44, 1, 0, 0}})
	assert.Equal(t, "Exif IFD Pointer", name)
	assert.Equal(t, "offset 300", value)

	name, value = Describe(order, Entry{Tag: 0x010F, Type: TypeASCII, Count: 5, Raw: ascii("Acme")})
	assert.Equal(t, "Make", name)
	assert.Equal(t, "Acme", value)
}

func TestDescribeGenericFallback(t *testing.T) {
	order := binary.LittleEndian

	// Unrecognized tag, SHORT array: decoded per-element, hex tag id as name.
	raw := make([]byte, 6)
	order.PutUint16(raw[0:], 1)
	order.PutUint16(raw[2:], 2)
	order.PutUint16(raw[4:], 3)
	name, value := Describe(order, Entry{Tag: 0x1234, Type: TypeShort, Count: 3, Raw: raw})
	assert.Equal(t, "0x1234", name)
	assert.Equal(t, "1, 2, 3", value)

	// Undefined type: binary-data marker, not a byte dump.
	name, value = Describe(order, Entry{Tag: 0x1235, Type: TypeUndefined, Count: 4, Raw: []byte{1, 2, 3, 4}})
	assert.Equal(t, "0x1235", name)
	assert.Equal(t, "(Binary data, 4 bytes)", value)

	// Opaque overrides type: still a binary-data marker even for an ASCII type.
	name, value = Describe(order, Entry{Tag: 0x1236, Type: TypeASCII, Count: 4, Raw: []byte("abcd"), Opaque: true})
	assert.Equal(t, "0x1236", name)
	assert.Equal(t, "(Binary data, 4 bytes)", value)
}

func TestDescribeGenericFallbackCapsLongArrays(t *testing.T) {
	order := binary.LittleEndian

	raw := make([]byte, 4*12) // 12 LONGs, more than maxGenericValues
	for i := range 12 {
		order.PutUint32(raw[i*4:], uint32(i))
	}
	_, value := Describe(order, Entry{Tag: 0x1237, Type: TypeLong, Count: 12, Raw: raw})
	assert.Equal(t, "0, 1, 2, 3, 4, 5, 6, 7, ... (4 more)", value)
}
