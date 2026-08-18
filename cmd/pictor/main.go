// Command pictor is a CLI for inspecting and manipulating image metadata.
package main

import (
	"os"

	"github.com/driquet/pictor/cmd/pictor/exif"
	"github.com/driquet/pictor/cmd/pictor/tiff"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "pictor",
		Short: "Inspect and manipulate image metadata",
	}
	root.AddCommand(tiff.NewCommand())
	root.AddCommand(exif.NewCommand())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
