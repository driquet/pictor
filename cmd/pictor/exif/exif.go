// Package exif wires the `pictor exif` command family.
package exif

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/driquet/pictor/exif"
	"github.com/driquet/pictor/internal/fsutil"
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

// readFile decodes one file and prints its tags. Parse/extract faults are
// best-effort: they're reported as warnings, not a hard failure.
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

	tags, errs := exif.ReadTags(f, info.Size())
	printTags(out, tags)
	for _, e := range errs {
		fmt.Fprintf(errOut, "warning: %s: %v\n", path, e)
	}
	return nil
}

// printTags renders the tag list grouped by IFD (IFD0, Exif, GPS), the order
// ReadTags/Extract already sort them in.
func printTags(w io.Writer, tags []exif.Tag) {
	if len(tags) == 0 {
		fmt.Fprintln(w, "no EXIF metadata")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	group, started := exif.Group(0), false
	for _, t := range tags {
		if !started || t.Group != group {
			if started {
				fmt.Fprintln(tw)
			}
			fmt.Fprintf(tw, "[%s]\n", t.Group)
			group, started = t.Group, true
		}
		fmt.Fprintf(tw, "  %s:\t%s\n", t.Name, t.Format())
	}
	tw.Flush()
}
