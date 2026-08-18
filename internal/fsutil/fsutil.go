// Package fsutil holds small filesystem helpers shared across pictor commands.
package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocateFiles expands roots into a sorted, deduped list of file paths.
//
// A root that is a regular file is included verbatim, regardless of extension
// (the user named it explicitly). A root that is a directory is walked
// recursively, keeping only files whose extension matches exts. Extension
// matching is case-insensitive; exts entries are dot-prefixed (e.g. ".jpg").
//
// A root that does not exist is a hard error. An empty result is not an error;
// callers decide whether zero matches is acceptable.
func LocateFiles(roots, exts []string) ([]string, error) {
	want := make(map[string]bool, len(exts))
	for _, e := range exts {
		want[strings.ToLower(e)] = true
	}

	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(root)
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if want[strings.ToLower(filepath.Ext(path))] {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", root, err)
		}
	}

	sort.Strings(out)
	return out, nil
}
