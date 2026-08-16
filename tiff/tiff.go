// Package tiff is a standalone, format-agnostic TIFF 6.0 IFD read/write engine.
//
// It models TIFF *structure* (the header, the IFD chain, directory entries,
// and the offset graph) and knows nothing about what individual tags mean. No
// EXIF, GPS, or orientation semantics live here; higher layers interpret tags.
// A caller injects the little format-specific knowledge the engine needs (which
// tag ids point to child IFDs) through options, so this package stays reusable
// for any TIFF-structured data, EXIF or not. Imports stdlib only.
//
// This file holds the shared types. Decode/DecodeBytes populate them on read;
// (*File).Encode (see encode.go) serialises them back with the allocator.
package tiff

import (
	"encoding/binary"
	"fmt"
)

// File is a decoded TIFF: the IFD tree plus the byte order and any best-effort
// faults collected while decoding. The tree is mutable: edit it in place and
// (once Encode lands) re-encode, the read-modify-write path exif drives.
type File struct {
	Root  *IFD             // IFD0; Root.Next chains to further top-level IFDs (IFD1 thumbnail, ...)
	Order binary.ByteOrder // file-wide byte order, preserved from decode
	Errs  []Error          // best-effort parse faults; see Error.Structural
}

// IFD is one image file directory.
//
// Entries holds real data tags only. A sub-IFD pointer entry (a tag the caller
// declared via WithSubIFDTags, or the TIFF-native SubIFDs tag) is lifted out of
// Entries during decode: its child lands in Subs keyed by the pointer tag id,
// and the entry itself is dropped so no stale offset is ever kept. (Encode will
// synthesize a fresh pointer entry from Subs.) Next follows the IFD chain.
type IFD struct {
	Entries []Entry
	Subs    map[uint16]*IFD // pointer tag id -> child IFD (e.g. Exif 0x8769, GPS 0x8825)
	Next    *IFD            // next IFD in the chain (nil = end)
}

// Entry is one directory entry. Raw is a zero-copy window over the value bytes
// in File.Order; interpret it with the caller's own tag knowledge, or copy it
// out with Bytes if it must outlive the decode buffer.
type Entry struct {
	Tag   uint16
	Type  Type
	Count uint32
	Raw   []byte

	// Opaque marks a value whose bytes must not be interpreted (MakerNote, ICC,
	// unknown blob). Pinned is the allocator hint consumed on write: true =
	// MakerNote/thumbnail/ICC, kept at its original offset and dropped if that
	// position can't be preserved (their internal pointers use an unknown vendor
	// base, so the blob can't be relocated); false (the default) = relocatable,
	// e.g. pixel strips/tiles, moved freely with an offset fixup. Both default
	// false on read; the caller sets them at decode via WithOpaqueTags/
	// WithImmovableTags (tiff auto-marks its own native pixel tags as relocatable).
	Opaque bool
	Pinned bool

	valOff uint32 // original offset of an out-of-line value (0 = inline); encode reservation

	// imgData holds pixel/tile/thumbnail byte ranges dereferenced through this
	// entry's offset array when decoded WithImageData (only for the TIFF-native
	// offset tags 273/324/0x0201). nil otherwise. On Encode the blobs are placed
	// fresh and this entry's value is rewritten as the new offset array.
	imgData [][]byte
}

// Bytes returns an owned copy of Raw, for a value that must outlive the parse
// buffer that Raw aliases.
func (e Entry) Bytes() []byte {
	b := make([]byte, len(e.Raw))
	copy(b, e.Raw)
	return b
}

// Type is an on-disk TIFF field type.
type Type uint16

const (
	TypeByte      Type = 1
	TypeASCII     Type = 2
	TypeShort     Type = 3
	TypeLong      Type = 4
	TypeRational  Type = 5
	TypeSByte     Type = 6
	TypeUndefined Type = 7
	TypeSShort    Type = 8
	TypeSLong     Type = 9
	TypeSRational Type = 10
	TypeFloat     Type = 11
	TypeDouble    Type = 12
	TypeIFD       Type = 13 // LONG-sized offset to a sub-IFD (Adobe TN, not TIFF 6.0)
)

// typeSize is the byte size of one element of each type, indexed by type. Index
// 0 and out-of-range types are 0 (unknown). Type 13 (IFD) is LONG-sized.
var typeSize = [14]uint32{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8, 4}

// size returns the element byte size, or 0 if the type is unknown.
func (t Type) size() uint32 {
	if int(t) < len(typeSize) {
		return typeSize[t]
	}
	return 0
}

// Rational is an unsigned TIFF fraction as stored on disk.
type Rational struct {
	Num, Denom uint32
}

func (r Rational) Float64() float64 {
	if r.Denom == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Denom)
}

func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Denom)
}

// SRational is a signed TIFF fraction as stored on disk.
type SRational struct {
	Num, Denom int32
}

func (r SRational) Float64() float64 {
	if r.Denom == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Denom)
}

func (r SRational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Denom)
}
