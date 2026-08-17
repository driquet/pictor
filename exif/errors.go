package exif

import (
	"fmt"
	"sort"

	"github.com/driquet/pictor/tiff"
)

// Error is a best-effort extract/parse fault. It reuses tiff's ErrorCode
// vocabulary so faults sort and read consistently across layers. Tag is the
// offending tag id (0 if none).
type Error struct {
	Code tiff.ErrorCode
	Tag  uint16
	msg  string
}

func (e Error) Error() string {
	s := e.Code.String()
	if e.Tag != 0 {
		s += fmt.Sprintf("@0x%04X", e.Tag)
	}
	if e.msg != "" {
		s += ": " + e.msg
	}
	return s
}

func newErr(code tiff.ErrorCode, tag uint16, msg string) Error {
	return Error{Code: code, Tag: tag, msg: msg}
}

// convErr is a value-conversion fault raised without tag context; wrapConv
// attaches the tag id once the extractor knows which entry failed.
type convErr struct {
	code tiff.ErrorCode
	msg  string
}

func (e convErr) Error() string { return e.msg }

func errWrongType(msg string) error { return convErr{tiff.ErrWrongType, msg} }
func errBadValue(msg string) error  { return convErr{tiff.ErrBadValue, msg} }
func errBadCount(msg string) error  { return convErr{tiff.ErrBadCount, msg} }

func wrapConv(err error, tag uint16) error {
	if c, ok := err.(convErr); ok {
		return Error{Code: c.code, Tag: tag, msg: c.msg}
	}
	return Error{Code: tiff.ErrBadValue, Tag: tag, msg: err.Error()}
}

// sortErrors orders faults deterministically by (Code, Tag) so best-effort
// output is golden-stable. Non-Error entries sort last, by message.
func sortErrors(errs []error) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, aok := errs[i].(Error)
		b, bok := errs[j].(Error)
		if aok && bok {
			if a.Code != b.Code {
				return a.Code < b.Code
			}
			return a.Tag < b.Tag
		}
		if aok != bok {
			return aok
		}
		return errs[i].Error() < errs[j].Error()
	})
}
