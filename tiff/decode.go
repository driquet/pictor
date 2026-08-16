package tiff

import (
	"encoding/binary"
	"io"
)

// tagSubIFDs is the TIFF-native SubIFDs pointer tag (Adobe TN). Always recursed,
// in addition to whatever the caller declares via WithSubIFDTags.
const tagSubIFDs uint16 = 330

const maxIFDDepth = 8

// Option configures a decode.
type Option func(*config)

type config struct {
	base         int64           // byte position of the TIFF header within the buffer
	subIFDTags   map[uint16]bool // caller-declared pointer tags (on top of tagSubIFDs)
	opaqueTags   map[uint16]bool // caller-declared Opaque values (MakerNote/ICC/XMP)
	immovableTag map[uint16]bool // caller-declared pinned values (Pinned=true)
	imageData    bool            // dereference native pixel/tile/thumbnail offset arrays
}

// tagKind returns the set-membership helper used by the option maps.
func tagSet(dst *map[uint16]bool, ids []uint16) {
	if *dst == nil {
		*dst = map[uint16]bool{}
	}
	for _, id := range ids {
		(*dst)[id] = true
	}
}

// WithByteOffsetBase sets the offset-base convention: the byte position of the
// TIFF header within the buffer. 0 (default) = standalone TIFF, offsets count
// from byte 0. Use nonzero when the buffer carries bytes before the header.
func WithByteOffsetBase(base int64) Option {
	return func(c *config) { c.base = base }
}

// WithSubIFDTags declares which tag ids hold pointers to child IFDs, so the
// engine recurses them into IFD.Subs and (on write) fixes up their offsets,
// without this package knowing any EXIF semantics. Presence drives recursion at
// every IFD level; ids are matched wherever they appear. The TIFF-native SubIFDs
// tag (330) is always handled.
func WithSubIFDTags(tagIDs ...uint16) Option {
	return func(c *config) { tagSet(&c.subIFDTags, tagIDs) }
}

// WithOpaqueTags marks the given tag ids Opaque: their value bytes must not be
// interpreted (MakerNote 0x927C, ICC, XMP). Encode still copies them verbatim;
// the flag is for higher layers that would otherwise decode the bytes.
func WithOpaqueTags(tagIDs ...uint16) Option {
	return func(c *config) { tagSet(&c.opaqueTags, tagIDs) }
}

// WithImmovableTags marks the given tag ids Pinned: Encode keeps their
// out-of-line value at its original offset - MakerNote/ICC internal pointers use
// an unknown vendor base, so the blob can't be relocated. If the offset can't be
// honoured the value is dropped with a WarnDroppedOnRelocate warning.
func WithImmovableTags(tagIDs ...uint16) Option {
	return func(c *config) { tagSet(&c.immovableTag, tagIDs) }
}

// WithImageData dereferences the TIFF-native pixel/tile/thumbnail offset arrays
// (StripOffsets 273↔279, TileOffsets 324↔325, JPEG thumbnail 0x0201↔0x0202) and
// pulls the addressed byte ranges into the tree, so Encode can rebuild a
// self-contained file from the tree alone. Off by default: read-only callers
// never touch pixels. Only standalone TIFF carries strips; a no-op otherwise.
func WithImageData() Option {
	return func(c *config) { c.imageData = true }
}

// Decode reads size bytes from r (byte 0 of the addressed range = the buffer the
// offset base is measured against) and decodes the TIFF within. Best-effort: a
// valid header yields a non-nil File even if individual entries fault.
func Decode(r io.ReaderAt, size int64, opts ...Option) (*File, error) {
	if size < 0 {
		return nil, newErr(ErrShortBuffer, 0, 0, "negative size")
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, size), buf); err != nil {
		return nil, newErr(ErrShortBuffer, 0, 0, err.Error())
	}
	return DecodeBytes(buf, opts...)
}

// DecodeBytes decodes an in-memory TIFF blob. Entry.Raw windows alias b, so
// mutating b after the call rots the tree. Returns a non-nil File whenever the
// header is valid; a fatal header fault returns a nil File and the error.
func DecodeBytes(b []byte, opts ...Option) (*File, error) {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}

	order, ifd0Off, err := readHeader(b, cfg.base)
	if err != nil {
		return nil, err
	}

	// Entry budget: a well-formed file's directories don't overlap, so total
	// entries <= usable/12. Caps the quadratic blowup from overlapping malformed
	// sub-IFDs. usable is the buffer beyond the header base.
	usable := int64(len(b)) - cfg.base
	p := &decoder{
		buf:       b,
		order:     order,
		base:      cfg.base,
		subTags:   cfg.subIFDTags,
		opaque:    cfg.opaqueTags,
		immovable: cfg.immovableTag,
		visited:   map[uint32]bool{},
		budget:    int(usable / 12),
	}
	root := p.decodeIFD(ifd0Off, 0)
	if root == nil {
		// A valid header always yields a tree, even if IFD0's offset was
		// unreachable (the fault is already collected).
		root = &IFD{}
	}
	if cfg.imageData {
		p.derefImageData(root)
	}
	sortErrors(p.errs)
	return &File{Root: root, Order: order, Errs: p.errs}, nil
}

// readHeader validates the 8-byte TIFF header at buffer offset base and returns
// the byte order and the IFD0 offset (relative to the header).
func readHeader(b []byte, base int64) (binary.ByteOrder, uint32, error) {
	if base < 0 || base+8 > int64(len(b)) {
		return nil, 0, newErr(ErrBadHeader, 0, 0, "buffer shorter than TIFF header")
	}
	h := b[base:]
	var order binary.ByteOrder
	switch {
	case h[0] == 'I' && h[1] == 'I':
		order = binary.LittleEndian
	case h[0] == 'M' && h[1] == 'M':
		order = binary.BigEndian
	default:
		return nil, 0, newErr(ErrBadHeader, 0, 0, "bad byte-order marker")
	}
	switch magic := order.Uint16(h[2:4]); magic {
	case 0x002A: // classic TIFF
	case 0x002B:
		return nil, 0, newErr(ErrBadHeader, 0, 0, "BigTIFF (magic 43) is not supported")
	default:
		return nil, 0, newErr(ErrBadHeader, 0, 0, "bad magic")
	}
	return order, order.Uint32(h[4:8]), nil
}

// decoder is the mutable state of one decode pass over a single buffer.
type decoder struct {
	buf   []byte           // whole input; Entry.Raw windows alias it
	order binary.ByteOrder // file-wide byte order, from the TIFF header
	base  int64            // byte position of the TIFF header within buf (offset origin)

	// subTags are the pointer tag ids the caller declared via WithSubIFDTags: tags
	// whose value is an offset to a child IFD instead of real data. When an entry
	// carries one, its child is decoded and lifted into IFD.Subs. The TIFF-native
	// SubIFDs tag (330) counts too; see isPointer.
	subTags map[uint16]bool

	// opaque / immovable are the caller-declared Opaque and Pinned tag sets
	// (WithOpaqueTags / WithImmovableTags); readEntry stamps the flags.
	opaque    map[uint16]bool
	immovable map[uint16]bool

	// visited holds IFD offsets already decoded, so a pointer or next-IFD chain that
	// loops back on itself is caught instead of recursing forever.
	visited map[uint32]bool

	// budget caps total directory entries decoded across the whole tree. Overlapping
	// malformed sub-IFDs could otherwise re-scan the same bytes quadratically; the
	// ceiling (usable bytes / 12, one entry = 12 bytes) bounds the work a hostile
	// file can force.
	budget int

	errs []Error // best-effort faults collected during the pass
}

func (p *decoder) fail(code ErrorCode, tag uint16, off uint32, msg string) {
	p.errs = append(p.errs, newErr(code, tag, off, msg))
}

// abs resolves a header-relative offset to a buffer index.
func (p *decoder) abs(off uint32) int64 {
	return p.base + int64(off)
}

// isPointer reports whether a tag id should be recursed as a sub-IFD pointer.
func (p *decoder) isPointer(id uint16) bool {
	return id == tagSubIFDs || p.subTags[id]
}

// decodeIFD decodes the directory at off (header-relative), lifts its sub-IFD
// pointer entries into Subs, and follows the IFD chain. Guarded by cycle
// detection, depth, and the entry budget.
func (p *decoder) decodeIFD(off uint32, depth int) *IFD {
	if depth > maxIFDDepth {
		p.fail(ErrCycle, 0, off, "max IFD depth exceeded")
		return nil
	}
	if p.visited[off] {
		p.fail(ErrCycle, 0, off, "IFD offset revisited")
		return nil
	}
	p.visited[off] = true

	base := p.abs(off)
	if base < 0 || base+2 > int64(len(p.buf)) {
		p.fail(ErrBadOffset, 0, off, "IFD offset out of range")
		return nil
	}
	count := int(p.order.Uint16(p.buf[base : base+2]))

	ifd := &IFD{}
	pos := base + 2
	for range count {
		if pos+12 > int64(len(p.buf)) {
			p.fail(ErrShortBuffer, 0, uint32(pos), "entry table truncated")
			break
		}
		if p.budget <= 0 {
			p.fail(ErrBadCount, 0, uint32(pos), "entry budget exhausted")
			break
		}
		p.budget--
		e, ok := p.readEntry(pos)
		if ok {
			ifd.Entries = append(ifd.Entries, e)
		}
		pos += 12
	}

	// Lift sub-IFD pointers out of Entries into Subs (recurse into each child).
	kept := ifd.Entries[:0]
	for _, e := range ifd.Entries {
		if !p.isPointer(e.Tag) {
			kept = append(kept, e)
			continue
		}
		poff, ok := pointerValue(p.order, e)
		if !ok {
			p.fail(ErrBadValue, e.Tag, 0, "sub-IFD pointer value too short")
			continue // dropped: a structural gap the write path will gate on
		}
		child := p.decodeIFD(poff, depth+1)
		if child == nil {
			continue // fault already collected; entry dropped
		}
		if ifd.Subs == nil {
			ifd.Subs = map[uint16]*IFD{}
		}
		ifd.Subs[e.Tag] = child
	}
	ifd.Entries = kept

	// Follow the IFD chain (next-IFD offset sits right after the entry table).
	if pos+4 <= int64(len(p.buf)) {
		if next := p.order.Uint32(p.buf[pos : pos+4]); next != 0 {
			ifd.Next = p.decodeIFD(next, depth+1)
		}
	}
	return ifd
}

// readEntry decodes one 12-byte directory entry at buffer index pos and resolves
// its value window (zero-copy for out-of-line values). Returns false only if the
// entry is unsalvageable; unknown types and bad offsets still survive as an
// Entry so a preserve-all write can round-trip them.
func (p *decoder) readEntry(pos int64) (Entry, bool) {
	id := p.order.Uint16(p.buf[pos : pos+2])
	typ := Type(p.order.Uint16(p.buf[pos+2 : pos+4]))
	count := p.order.Uint32(p.buf[pos+4 : pos+8])
	valField := pos + 8 // 4-byte value/offset field

	e := Entry{Tag: id, Type: typ, Count: count, Opaque: p.opaque[id], Pinned: p.immovable[id]}

	esize := typ.size()
	if esize == 0 {
		// Unknown type: keep the raw 4-byte inline field, flag it.
		e.Raw = p.buf[valField : valField+4]
		p.fail(ErrUnknownType, id, uint32(pos), "unknown TIFF type")
		return e, true
	}

	// total may overflow uint32 for a lying count; use uint64.
	total := uint64(esize) * uint64(count)
	if total <= 4 {
		// Inline: value left-justified in the 4-byte field.
		e.Raw = p.buf[valField : valField+int64(total)]
		return e, true
	}
	voff := p.order.Uint32(p.buf[valField : valField+4])
	start := p.abs(voff)
	end := start + int64(total)
	if start < 0 || end > int64(len(p.buf)) {
		p.fail(ErrBadOffset, id, voff, "value offset out of range")
		return e, true // survives with nil Raw
	}
	e.Raw = p.buf[start:end]
	e.valOff = voff
	return e, true
}

// imagePair is a TIFF-native (offset-array tag, length-array tag) couple whose
// addressed byte ranges WithImageData pulls into the tree.
type imagePair struct{ off, cnt uint16 }

// imagePairs are the three second-level indirections a read-only decode skips:
// strips, tiles, and the embedded JPEG thumbnail (single-element arrays for the
// thumbnail). Dead old-JPEG table tags (0x0203–0x0205) are ignored.
var imagePairs = []imagePair{
	{273, 279},       // StripOffsets / StripByteCounts
	{324, 325},       // TileOffsets / TileByteCounts
	{0x0201, 0x0202}, // JPEGInterchangeFormat / ...Length (thumbnail)
}

// derefImageData walks the IFD chain and, for each imagePair present, slices the
// pixel/tile/thumbnail byte ranges out of the buffer into the offset entry's
// imgData. An out-of-range offset records a structural fault (which then gates
// Encode). Sub-IFDs never hold strips, so only the top-level chain is walked.
func (p *decoder) derefImageData(ifd *IFD) {
	for ; ifd != nil; ifd = ifd.Next {
		for _, pr := range imagePairs {
			oi := entryIndex(ifd, pr.off)
			ci := entryIndex(ifd, pr.cnt)
			if oi < 0 || ci < 0 {
				continue
			}
			offs := readUintArray(p.order, ifd.Entries[oi].Type, ifd.Entries[oi].Raw, ifd.Entries[oi].Count)
			lens := readUintArray(p.order, ifd.Entries[ci].Type, ifd.Entries[ci].Raw, ifd.Entries[ci].Count)
			if len(offs) == 0 || len(offs) != len(lens) {
				p.fail(ErrBadValue, pr.off, 0, "image offset/count arrays mismatched")
				continue
			}
			blobs := make([][]byte, len(offs))
			ok := true
			for i := range offs {
				start := p.abs(offs[i])
				end := start + int64(lens[i])
				if start < 0 || end > int64(len(p.buf)) || end < start {
					p.fail(ErrBadOffset, pr.off, offs[i], "image data offset out of range")
					ok = false
					break
				}
				blobs[i] = p.buf[start:end]
			}
			if ok {
				ifd.Entries[oi].imgData = blobs
			}
		}
	}
}

// entryIndex returns the index of the first entry with tag id, or -1.
func entryIndex(ifd *IFD, id uint16) int {
	for i := range ifd.Entries {
		if ifd.Entries[i].Tag == id {
			return i
		}
	}
	return -1
}

// readUintArray reads count SHORT/LONG elements from raw as uint32s, clamped to
// what raw actually holds. Other types yield nil (not an offset array).
func readUintArray(order binary.ByteOrder, t Type, raw []byte, count uint32) []uint32 {
	sz := t.size()
	if sz != 2 && sz != 4 {
		return nil
	}
	n := int(count)
	if max := len(raw) / int(sz); n > max {
		n = max
	}
	out := make([]uint32, n)
	for i := range out {
		if sz == 2 {
			out[i] = uint32(order.Uint16(raw[i*2:]))
		} else {
			out[i] = order.Uint32(raw[i*4:])
		}
	}
	return out
}

// pointerValue reads a child-IFD offset from a pointer entry's value.
func pointerValue(order binary.ByteOrder, e Entry) (uint32, bool) {
	if len(e.Raw) < 4 {
		return 0, false
	}
	return order.Uint32(e.Raw[:4]), true
}
