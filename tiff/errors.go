package tiff

import (
	"fmt"
	"sort"
)

// ErrorCode is a stable, sortable classification of a decode fault. Stable so
// collected faults can be golden-diffed in tests; the numeric ordering plus
// (Tag, Offset) gives a deterministic sort key.
type ErrorCode uint8

const (
	ErrBadHeader   ErrorCode = iota + 1 // TIFF header missing/invalid
	ErrShortBuffer                      // read ran past the buffer
	ErrBadOffset                        // value/IFD offset out of range
	ErrUnknownType                      // TIFF type outside 1..13
	ErrBadCount                         // count zero or too large for the type
	ErrCycle                            // IFD pointer cycle / depth exceeded
	ErrWrongType                        // tag present but not the expected type
	ErrBadValue                         // value could not be interpreted
)

var errCodeName = map[ErrorCode]string{
	ErrBadHeader:   "ErrBadHeader",
	ErrShortBuffer: "ErrShortBuffer",
	ErrBadOffset:   "ErrBadOffset",
	ErrUnknownType: "ErrUnknownType",
	ErrBadCount:    "ErrBadCount",
	ErrCycle:       "ErrCycle",
	ErrWrongType:   "ErrWrongType",
	ErrBadValue:    "ErrBadValue",
}

func (c ErrorCode) String() string {
	if n, ok := errCodeName[c]; ok {
		return n
	}
	return fmt.Sprintf("ErrorCode(%d)", uint8(c))
}

// Structural reports whether a fault means an entry's raw bytes could not be
// captured, so a preserve-all write cannot faithfully round-trip the file. It
// gates the read-modify-write pipeline: a structural fault blocks the write; a
// value-level fault (raw bytes intact, only the decoded value bad) does not.
func (c ErrorCode) Structural() bool {
	switch c {
	case ErrBadHeader, ErrShortBuffer, ErrBadOffset, ErrBadCount, ErrCycle:
		return true
	default: // ErrUnknownType, ErrWrongType, ErrBadValue: raw bytes intact
		return false
	}
}

// Error is a single best-effort decode fault. Tag is the offending tag id (0 if
// none); Offset is the blob offset it occurred at (0 if none).
type Error struct {
	Code   ErrorCode
	Tag    uint16
	Offset uint32
	msg    string
}

// Structural reports whether this fault blocks a faithful round-trip.
func (e Error) Structural() bool { return e.Code.Structural() }

func (e Error) Error() string {
	s := e.Code.String()
	if e.Tag != 0 {
		s += fmt.Sprintf("@0x%04X", e.Tag)
	} else if e.Offset != 0 {
		s += fmt.Sprintf("@+0x%X", e.Offset)
	}
	if e.msg != "" {
		s += ": " + e.msg
	}
	return s
}

func newErr(code ErrorCode, tag uint16, off uint32, msg string) Error {
	return Error{Code: code, Tag: tag, Offset: off, msg: msg}
}

// sortErrors orders a collected fault slice deterministically by (Code, Tag,
// Offset), so best-effort output is golden-stable.
func sortErrors(errs []Error) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i], errs[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Tag != b.Tag {
			return a.Tag < b.Tag
		}
		return a.Offset < b.Offset
	})
}
