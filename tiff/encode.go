package tiff

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
)

// Warning is a non-fatal "X dropped/changed because Y" notice from Encode - the
// channel a higher layer surfaces to a user editing one tag who would otherwise
// silently lose MakerNote/ICC/thumbnail. Separate from decode's File.Errs.
type Warning struct {
	Tag  uint16
	Kind WarningKind
	Msg  string
}

func (w Warning) String() string { return fmt.Sprintf("%s@0x%04X: %s", w.Kind, w.Tag, w.Msg) }

type WarningKind uint8

const (
	WarnDroppedOnRelocate   WarningKind = iota + 1 // pinned blob's offset couldn't be honoured
	WarnOffsetBaseAmbiguous                        // reserved; not emitted yet
)

func (k WarningKind) String() string {
	switch k {
	case WarnDroppedOnRelocate:
		return "WarnDroppedOnRelocate"
	case WarnOffsetBaseAmbiguous:
		return "WarnOffsetBaseAmbiguous"
	}
	return fmt.Sprintf("WarningKind(%d)", uint8(k))
}

const headerSize = 8

// Encode serialises the (possibly mutated) IFD tree to a standalone classic-TIFF
// blob in File.Order - the write half of the read-modify-write path.
//
// Build-from-scratch: the whole file is laid out fresh from the tree, so nothing
// but the tree is needed (a hand-built File encodes the same way). Pinned blobs
// (Pinned entries: MakerNote/ICC) are kept at their original offset; everything
// else - IFDs, ordinary values, pixel/tile/thumbnail data pulled in with
// WithImageData - is packed first-fit into the free space around them. A pinned
// blob whose offset can't be honoured is dropped with a WarnDroppedOnRelocate.
//
// Encode refuses (returns an error, no partial output) if the File carries any
// structural decode fault: those mean raw bytes weren't captured, so a rebuild
// would silently lose data.
func (f *File) Encode() ([]byte, []Warning, error) {
	for _, e := range f.Errs {
		if e.Structural() {
			return nil, nil, fmt.Errorf("tiff: refusing to encode File with structural fault: %w", e)
		}
	}
	order := f.Order
	if order == nil {
		order = binary.LittleEndian
	}
	root := f.Root
	if root == nil {
		root = &IFD{}
	}

	e := &encoder{order: order, alloc: newAllocator(), plans: map[*IFD]*ifdPlan{}}

	// Pass 1: pin the immovable blobs at their original offsets (or drop them).
	// Resolving drops first fixes each directory's final entry count.
	forEachIFD(root, e.pinIFD)

	// Pass 2: place every IFD directory (its size now known).
	forEachIFD(root, e.placeDir)

	// Pass 3: place out-of-line values and relocated pixel data.
	forEachIFD(root, e.placeValues)

	// Pass 4: emit.
	buf := make([]byte, e.alloc.size())
	if order == binary.BigEndian {
		buf[0], buf[1] = 'M', 'M'
	} else {
		buf[0], buf[1] = 'I', 'I'
	}
	order.PutUint16(buf[2:4], 0x002A)
	order.PutUint32(buf[4:8], e.plans[root].off)
	forEachIFD(root, func(ifd *IFD) { e.writeIFD(buf, ifd) })
	for _, b := range e.blobs {
		copy(buf[b.off:], b.data)
	}

	return buf, e.warns, nil
}

// ifdPlan holds the resolved layout for one directory: its table offset, and per
// original-entry bookkeeping (pin offset, drop flag, rebuilt image-data value).
type ifdPlan struct {
	off      uint32
	dropped  []bool   // parallel to IFD.Entries: pinned blob that couldn't be placed
	pinOff   []uint32 // parallel: honoured pin offset (0 = not pinned)
	imgValue [][]byte // parallel: rebuilt offset-array value for image-data entries
	imgType  []Type   // parallel: on-disk type for imgValue, when upgraded from Entry.Type (0 = not overridden)
}

type encoder struct {
	order binary.ByteOrder
	alloc *allocator
	plans map[*IFD]*ifdPlan
	warns []Warning
	blobs []blob // pixel/tile/thumbnail data to copy in at emit time
}

// blob is a placed image-data byte range awaiting its copy into the output.
type blob struct {
	off  uint32
	data []byte
}

func (e *encoder) plan(ifd *IFD) *ifdPlan {
	p := e.plans[ifd]
	if p == nil {
		p = &ifdPlan{
			dropped:  make([]bool, len(ifd.Entries)),
			pinOff:   make([]uint32, len(ifd.Entries)),
			imgValue: make([][]byte, len(ifd.Entries)),
			imgType:  make([]Type, len(ifd.Entries)),
		}
		e.plans[ifd] = p
	}
	return p
}

func (e *encoder) pinIFD(ifd *IFD) {
	p := e.plan(ifd)
	for i := range ifd.Entries {
		en := &ifd.Entries[i]
		if !en.Pinned || en.valOff == 0 || len(en.Raw) <= 4 {
			continue // not a pinned out-of-line blob
		}
		if e.alloc.reserve(en.valOff, uint32(len(en.Raw))) {
			p.pinOff[i] = en.valOff
		} else {
			p.dropped[i] = true
			e.warns = append(e.warns, Warning{
				Tag:  en.Tag,
				Kind: WarnDroppedOnRelocate,
				Msg:  "immovable value could not keep its original offset",
			})
		}
	}
}

func (e *encoder) placeDir(ifd *IFD) {
	p := e.plan(ifd)
	n := len(ifd.Subs) // synthesized pointer entries
	for i := range ifd.Entries {
		if !p.dropped[i] {
			n++
		}
	}
	p.off = e.alloc.alloc(dirBytes(n))
}

func (e *encoder) placeValues(ifd *IFD) {
	p := e.plan(ifd)
	for i := range ifd.Entries {
		if p.dropped[i] || p.pinOff[i] != 0 {
			continue // dropped, or already placed by the pin pass
		}
		en := &ifd.Entries[i]
		if en.imgData != nil {
			p.imgValue[i], p.imgType[i] = e.placeImageData(en)
		}
		if raw := p.rawOf(i, ifd); len(raw) > 4 {
			p.setValOff(i, e.alloc.alloc(uint32(len(raw))))
		}
	}
}

// placeImageData places each pixel/tile/thumbnail blob and returns the rebuilt
// offset array that becomes its new value, plus the type it was written in.
// The array is normally the entry's own SHORT/LONG type, but relocation can
// push an offset past what SHORT can hold (0xFFFF); when that happens the
// array is promoted to LONG so the offsets stay correct, and the returned
// type tells writeIFD to emit the entry as LONG instead of the original type.
func (e *encoder) placeImageData(en *Entry) ([]byte, Type) {
	offs := make([]uint32, len(en.imgData))
	for i, b := range en.imgData {
		offs[i] = e.alloc.alloc(uint32(len(b)))
		e.blobs = append(e.blobs, blob{off: offs[i], data: b})
	}
	sz := en.Type.size()
	if sz != 2 && sz != 4 { // shouldn't happen; offsets are SHORT/LONG
		sz = 4
	}
	typ := en.Type
	if sz == 2 {
		for _, o := range offs {
			if o > 0xFFFF {
				sz, typ = 4, TypeLong
				break
			}
		}
	}
	out := make([]byte, int(sz)*len(offs))
	for i, o := range offs {
		if sz == 2 {
			e.order.PutUint16(out[i*2:], uint16(o))
		} else {
			e.order.PutUint32(out[i*4:], o)
		}
	}
	return out, typ
}

// rawOf returns the value bytes to write for entry i (rebuilt image-data value
// if any, else the original Raw).
func (p *ifdPlan) rawOf(i int, ifd *IFD) []byte {
	if p.imgValue[i] != nil {
		return p.imgValue[i]
	}
	return ifd.Entries[i].Raw
}

// setValOff records a heap offset for a non-pinned out-of-line value, reusing the
// pinOff slot (pinned entries never reach here, so the slot is free).
func (p *ifdPlan) setValOff(i int, off uint32) { p.pinOff[i] = off }

func (e *encoder) writeIFD(buf []byte, ifd *IFD) {
	p := e.plans[ifd]
	order := e.order

	// Assemble the final entry list: kept data entries + synthesized sub-IFD
	// pointers, sorted ascending by tag (TIFF requires ordered directories).
	type wire struct {
		tag   uint16
		typ   Type
		count uint32
		raw   []byte // full value bytes
		off   uint32 // heap offset if heap==true
		heap  bool
	}
	var ws []wire
	for i := range ifd.Entries {
		if p.dropped[i] {
			continue
		}
		en := &ifd.Entries[i]
		raw := p.rawOf(i, ifd)
		typ := en.Type
		if p.imgType[i] != 0 {
			typ = p.imgType[i]
		}
		w := wire{tag: en.Tag, typ: typ, count: en.Count, raw: raw}
		if off := p.pinOff[i]; off != 0 { // pinned or heap-placed value
			w.off, w.heap = off, true
		}
		ws = append(ws, w)
	}
	for _, tag := range sortedKeys(ifd.Subs) {
		child := ifd.Subs[tag]
		v := make([]byte, 4)
		order.PutUint32(v, e.plans[child].off)
		ws = append(ws, wire{tag: tag, typ: TypeLong, count: 1, raw: v})
	}
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].tag < ws[j].tag })

	off := p.off
	order.PutUint16(buf[off:], uint16(len(ws)))
	q := off + 2
	for _, w := range ws {
		order.PutUint16(buf[q:], w.tag)
		order.PutUint16(buf[q+2:], uint16(w.typ))
		order.PutUint32(buf[q+4:], w.count)
		field := buf[q+8 : q+12]
		if w.heap {
			order.PutUint32(field, w.off)
			copy(buf[w.off:], w.raw)
		} else {
			copy(field, w.raw) // inline, left-justified, zero-padded
		}
		q += 12
	}
	if ifd.Next != nil {
		order.PutUint32(buf[q:], e.plans[ifd.Next].off)
	} // else next-IFD pointer stays zero
}

// dirBytes is the on-disk size of an n-entry directory: 2-byte count + n 12-byte
// entries + 4-byte next-IFD pointer.
func dirBytes(n int) uint32 { return uint32(2 + 12*n + 4) }

// forEachIFD visits every IFD reachable from ifd in a fixed order - each node,
// then its sub-IFDs (ascending tag), then the next-IFD chain - so the three
// layout passes and the write pass agree on ordering.
func forEachIFD(ifd *IFD, fn func(*IFD)) {
	for ; ifd != nil; ifd = ifd.Next {
		fn(ifd)
		for _, tag := range sortedKeys(ifd.Subs) {
			forEachIFD(ifd.Subs[tag], fn)
		}
	}
}

func sortedKeys(m map[uint16]*IFD) []uint16 {
	if len(m) == 0 {
		return nil
	}
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// --- allocator ---------------------------------------------------------------

// allocator packs write blocks into a byte range, honouring reserved spans (the
// 8-byte header, plus each pinned blob). alloc first-fits into gaps between used
// spans and otherwise appends at EOF; every allocation is word-aligned (even
// offset), the TIFF requirement for IFDs and out-of-line values.
type allocator struct {
	used []span // sorted by off, non-overlapping
}

type span struct{ off, end uint32 } // [off, end)

func newAllocator() *allocator {
	return &allocator{used: []span{{0, headerSize}}} // header reserved
}

func align2(n uint32) uint32 { return (n + 1) &^ 1 }

// reserve claims [off, off+size); returns false if it overlaps an existing span
// (a pinned offset that collides with the header or another pin).
func (a *allocator) reserve(off, size uint32) bool {
	if size == 0 {
		return true
	}
	end := off + size
	for _, s := range a.used {
		if off < s.end && s.off < end { // overlap
			return false
		}
	}
	a.insert(span{off, end})
	return true
}

// alloc reserves size word-aligned bytes, first-fit into a gap between used
// spans, else at EOF, and returns the chosen offset.
func (a *allocator) alloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	var cursor uint32
	for _, s := range a.used {
		start := align2(cursor)
		if start+size <= s.off { // fits before this span
			a.insert(span{start, start + size})
			return start
		}
		if s.end > cursor {
			cursor = s.end
		}
	}
	start := align2(cursor)
	a.insert(span{start, start + size})
	return start
}

// size returns the smallest buffer that holds every used span.
func (a *allocator) size() uint32 {
	var end uint32
	for _, s := range a.used {
		if s.end > end {
			end = s.end
		}
	}
	return end
}

func (a *allocator) insert(s span) {
	i := sort.Search(len(a.used), func(i int) bool { return a.used[i].off >= s.off })
	a.used = append(a.used, span{})
	copy(a.used[i+1:], a.used[i:])
	a.used[i] = s
}
