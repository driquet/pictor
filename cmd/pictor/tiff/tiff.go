// Package tiff wires the `pictor tiff` command family.
package tiff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/driquet/pictor/internal/fsutil"
	"github.com/driquet/pictor/tiff"
	"github.com/spf13/cobra"
)

// NewCommand builds the `tiff` command and its subcommands. `tiff` itself runs
// `read` so `pictor tiff <path>` and `pictor tiff read <path>` behave alike.
func NewCommand() *cobra.Command {
	read := &cobra.Command{
		Use:   "read path...",
		Short: "Display the structure of a TIFF file (every IFD, every entry)",
		Long: "Display the structure of a TIFF file: every IFD in the chain, every entry, and any\n" +
			"TIFF-native Sub-IFDs (tag 330). This walks the container format only - it does not\n" +
			"decode or interpret pointer tags (EXIF/GPS/Interop) into human values such as GPS\n" +
			"coordinates; those print as raw entries here. Use `pictor exif read` for that.",
		RunE: runRead,
	}

	cmd := &cobra.Command{
		Use:   "tiff path...",
		Short: "Inspect TIFF file structure",
		Long: "Inspect TIFF file structure. Running `pictor tiff <path>` is equivalent to\n" +
			"`pictor tiff read <path>`; see `pictor tiff read --help` for details.",
		RunE: runRead,
	}
	cmd.AddCommand(read)
	return cmd
}

func runRead(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	files, err := fsutil.LocateFiles(args, tiff.SupportedExtensions)
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

// readFile decodes one file and prints its structure. Parse faults are
// best-effort (tiff.Decode still returns a tree): they're reported as
// warnings, not a hard failure.
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

	file, err := tiff.Decode(f, info.Size())
	if err != nil {
		return err
	}

	printFile(out, file)
	for _, e := range file.Errs {
		fmt.Fprintf(errOut, "warning: %s: %v\n", path, e)
	}
	return nil
}

// printFile renders the IFD chain (IFD0, IFD1, ...) and, structurally, any
// TIFF-native SubIFDs (tag 330) the decoder always lifts out regardless of
// caller options. EXIF/GPS/Interop pointer tags are NOT recursed here - they
// print as plain entries (tiff.Describe labels them); pictor exif read is
// where their contents show up.
func printFile(w io.Writer, f *tiff.File) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for i, ifd := 0, f.Root; ifd != nil; i, ifd = i+1, ifd.Next {
		printIFD(tw, f.Order, ifd, fmt.Sprintf("IFD%d", i))
	}
	tw.Flush()
}

func printIFD(tw *tabwriter.Writer, order binary.ByteOrder, ifd *tiff.IFD, label string) {
	fmt.Fprintf(tw, "%s (%d entries):\n", label, len(ifd.Entries))
	for _, e := range ifd.Entries {
		name, value := tiff.Describe(order, e)
		fmt.Fprintf(tw, "  %s:\t%s\n", name, value)
	}
	for _, tag := range sortedSubKeys(ifd.Subs) {
		printIFD(tw, order, ifd.Subs[tag], fmt.Sprintf("%s / Sub-IFD 0x%04X", label, tag))
	}
}

func sortedSubKeys(m map[uint16]*tiff.IFD) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
