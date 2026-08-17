package exif

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetUnknownTagErrors(t *testing.T) {
	_, err := Set([]string{"Bogus=1"})
	require.Error(t, err, "want error for unknown tag name")
}

func TestSetMalformedAssignmentErrors(t *testing.T) {
	_, err := Set([]string{"NoEqualsSign"})
	require.Error(t, err, "want error for missing '='")
}

func TestSetMalformedValueErrors(t *testing.T) {
	_, err := Set([]string{"Orientation=nine"})
	require.Error(t, err, "want error for non-integer Orientation")

	_, err = Set([]string{"Orientation=9"})
	require.Error(t, err, "want error for Orientation out of range 1..8")
}

func TestStripUnknownTagErrors(t *testing.T) {
	_, err := Strip([]string{"Bogus"})
	require.Error(t, err, "want error for unknown tag name")
}
