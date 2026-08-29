// Command gen writes the minimal, EXIF-free base images the exiftool
// comparison tests (exif/exiftool_test.go) start from: base.jpg, base.png,
// base.tif, base.webp in exif/testdata/. Run it once from the exif/
// directory whenever a fixture needs regenerating:
//
//	go run ./testdata/gen
//
// WebP generation shells out to cwebp (libwebp), since Go's stdlib has no
// WebP encoder; the source PNG carries an alpha channel so cwebp emits an
// extended-format (VP8X) container, which pictor requires to add EXIF to a
// WebP (see container.Location.CanAddEXIF).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/driquet/pictor/tiff"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	must(writeJPEG(filepath.Join(dir, "base.jpg")))
	must(writePNG(filepath.Join(dir, "base.png")))
	must(writeTIFF(filepath.Join(dir, "base.tif")))
	must(writeWebP(filepath.Join(dir, "base.webp")))
	fmt.Println("wrote base.jpg, base.png, base.tif, base.webp to", dir)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func checkerboard(alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	a := uint8(255)
	if alpha {
		a = 200
	}
	img.Set(0, 0, color.NRGBA{255, 0, 0, a})
	img.Set(1, 0, color.NRGBA{0, 255, 0, a})
	img.Set(0, 1, color.NRGBA{0, 0, 255, a})
	img.Set(1, 1, color.NRGBA{255, 255, 0, a})
	return img
}

func writeJPEG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, checkerboard(false), &jpeg.Options{Quality: 90})
}

func writePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, checkerboard(false))
}

func writeTIFF(path string) error {
	f := &tiff.File{Root: &tiff.IFD{}, Order: binary.LittleEndian}
	b, _, err := f.Encode()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// writeWebP encodes an alpha PNG to a temp file and runs cwebp on it, since
// there's no pure-Go WebP encoder. The alpha channel forces cwebp to emit an
// extended (VP8X) container.
func writeWebP(path string) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, checkerboard(true)); err != nil {
		return err
	}
	tmpPNG, err := os.CreateTemp("", "gen-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(tmpPNG.Name())
	if _, err := tmpPNG.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := tmpPNG.Close(); err != nil {
		return err
	}

	cmd := exec.Command("cwebp", "-quiet", tmpPNG.Name(), "-o", path)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
