package exif

import (
	"testing"

	"github.com/driquet/pictor/tiff"
	"github.com/stretchr/testify/assert"
)

func TestTagFormat(t *testing.T) {
	assert.Equal(t, "Acme", Tag{Value: "Acme"}.Format())
	assert.Equal(t, "Normal", Tag{Value: OrientationTopLeft}.Format())
	assert.Equal(t, "100", Tag{Value: uint16(100)}.Format())
	assert.Equal(t, "0.5", Tag{Value: tiff.Rational{Num: 1, Denom: 2}}.Format())
	assert.Equal(t, "40, 26, 46.302",
		Tag{Value: []tiff.Rational{{Num: 40, Denom: 1}, {Num: 26, Denom: 1}, {Num: 46302, Denom: 1000}}}.Format())
}
