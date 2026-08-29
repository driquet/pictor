// Package exif wires the `pictor exif` command family.
package exif

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/driquet/pictor/exif"
	"github.com/driquet/pictor/internal/fsutil"
	"github.com/driquet/pictor/tiff"
	"github.com/spf13/cobra"
)

// NewCommand builds the `exif` command and its subcommands. `exif` itself runs
// `read` so `pictor exif <path>` and `pictor exif read <path>` behave alike.
func NewCommand() *cobra.Command {
	read := &cobra.Command{
		Use:   "read path...",
		Short: "Display EXIF metadata for images",
		Long: "Display EXIF metadata for images, grouped by IFD (IFD0, Exif, GPS, ...). Unlike\n" +
			"`pictor tiff read`, this decodes the EXIF/GPS/Interop pointer tags and formats\n" +
			"their contents into human-readable values, e.g. GPS coordinates.",
		RunE: runRead,
	}

	cmd := &cobra.Command{
		Use:   "exif path...",
		Short: "Inspect EXIF metadata",
		Long: "Inspect EXIF metadata. Running `pictor exif <path>` is equivalent to\n" +
			"`pictor exif read <path>`; see `pictor exif read --help` for details.",
		RunE: runRead,
	}
	cmd.AddCommand(read)
	return cmd
}

func runRead(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	files, err := fsutil.LocateFiles(args, exif.SupportedExtensions)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("no matching files found")
	}

	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	multi := len(files) > 1
	failed := false

	for _, path := range files {
		if multi {
			fmt.Fprintf(out, "== %s ==\n", path)
		}
		if err := readFile(out, errOut, path); err != nil {
			fmt.Fprintf(errOut, "%s: %v\n", path, err)
			failed = true
		}
		if multi {
			fmt.Fprintln(out)
		}
	}

	if failed {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return errReadFailed
	}
	return nil
}

var errReadFailed = errors.New("one or more files failed to read")

// readFile decodes one file and prints its tags. Best-effort faults land in
// doc.Errs(); they're reported as warnings, not a hard failure.
func readFile(out, errOut io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	doc, err := exif.Read(f, info.Size())
	if err != nil {
		return err
	}

	printSections(out, buildSections(path, info, doc.Tags(), doc.Metadata().GPS))
	for _, e := range doc.Errs() {
		fmt.Fprintf(errOut, "warning: %s: %v\n", path, e)
	}
	return nil
}

// row is one key/value line within a section.
type row struct{ key, val string }

// section is a named group of rows, e.g. "File" or "Camera".
type section struct {
	name string
	rows []row
}

// tagSection buckets EXIF tags into presentation sections that don't mirror
// the underlying TIFF/IFD structure (exif.Group). GPS tags aren't listed
// here; they're matched by Group instead (see buildSections) since some of
// them (the *Ref tags) have no exported exif.TagName constant to key on.
var tagSection = map[exif.TagName]string{
	exif.TagMake:      "Camera",
	exif.TagModel:     "Camera",
	exif.TagLensModel: "Camera",

	exif.TagOrientation:     "Image",
	exif.TagPixelXDimension: "Image",
	exif.TagPixelYDimension: "Image",

	exif.TagDateTime:           "Capture",
	exif.TagDateTimeOriginal:   "Capture",
	exif.TagOffsetTimeOriginal: "Capture",
	exif.TagExposureTime:       "Capture",
	exif.TagFNumber:            "Capture",
	exif.TagISO:                "Capture",
	exif.TagFocalLength:        "Capture",
	exif.TagSoftware:           "Capture",
}

// sectionOrder is the fixed display order for EXIF-derived sections; a
// section is only printed if it ended up with rows.
var sectionOrder = []string{"Camera", "Image", "Capture", "GPS"}

// buildSections assembles the File section (from OS-level file info) plus
// the EXIF-derived sections tags bucket into via tagSection/sectionOrder.
// Tags that don't match any known section (e.g. a future tag added to
// exif/metadata.go without updating tagSection) land in a catch-all "EXIF"
// section rather than being silently dropped.
func buildSections(path string, info os.FileInfo, tags []exif.Tag, gps *exif.GPSInfo) []section {
	sections := []section{{
		name: "File",
		rows: []row{
			{"Path", path},
			{"Size", strconv.FormatInt(info.Size(), 10) + " bytes"},
			{"Permissions", info.Mode().String()},
			{"ModTime", info.ModTime().Format(time.RFC3339)},
		},
	}}

	byName := make(map[string][]row)
	var fallback []row
	for _, t := range tags {
		name, ok := tagSection[t.Name]
		if !ok {
			if t.Group == exif.GroupGPS {
				name = "GPS"
			} else {
				fallback = append(fallback, row{string(t.Name), formatTag(t)})
				continue
			}
		}
		byName[name] = append(byName[name], row{string(t.Name), formatTag(t)})
	}
	if gps != nil && (gps.Latitude != 0 || gps.Longitude != 0) {
		byName["GPS"] = append(byName["GPS"],
			row{"Google Maps", fmt.Sprintf("https://www.google.com/maps?q=%.6f,%.6f", gps.Latitude, gps.Longitude)},
			row{"OpenStreetMap", fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", gps.Latitude, gps.Longitude, gps.Latitude, gps.Longitude)},
		)
	}
	for _, name := range sectionOrder {
		if rows := byName[name]; len(rows) > 0 {
			sections = append(sections, section{name, rows})
		}
	}
	if len(fallback) > 0 {
		sections = append(sections, section{"EXIF", fallback})
	}
	return sections
}

// printSections renders sections in order, tabwriter-aligned within a
// single pass so key columns line up across the whole file's output.
func printSections(w io.Writer, sections []section) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for i, s := range sections {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		fmt.Fprintf(tw, "[%s]\n", s.name)
		for _, r := range s.rows {
			fmt.Fprintf(tw, "  %s:\t%s\n", r.key, r.val)
		}
	}
	tw.Flush()
}

// formatTag renders a Tag's already-typed Value as display text. Value's
// dynamic type is whatever exif.Tag's doc comment documents: string,
// uint8/16/32, exif.Orientation, tiff.Rational, or []tiff.Rational (an
// undivided GPS deg/min/sec triplet).
func formatTag(t exif.Tag) string {
	switch v := t.Value.(type) {
	case string:
		return v
	case exif.Orientation:
		return v.String()
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case tiff.Rational:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	case []tiff.Rational:
		parts := make([]string, len(v))
		for i, r := range v {
			parts[i] = strconv.FormatFloat(r.Float64(), 'g', -1, 64)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}
