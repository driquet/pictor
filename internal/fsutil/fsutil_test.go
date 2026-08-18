package fsutil

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocateFiles(t *testing.T) {
	dir := t.TempDir()
	// layout: a.jpg, b.PNG, note.txt, sub/c.jpeg
	mk := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	aJpg := mk("a.jpg")
	bPng := mk("b.PNG")   // uppercase ext: must match case-insensitively
	txt := mk("note.txt") // non-image: filtered out when reached via dir walk
	cJpeg := mk("sub/c.jpeg")

	exts := []string{".jpg", ".jpeg", ".png"}

	// Dir root: recursive walk, case-insensitive, non-image filtered.
	got, err := LocateFiles([]string{dir}, exts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{aJpg, bPng, cJpeg} // sorted; note.txt excluded
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dir walk: got %v want %v", got, want)
	}

	// Explicit file arg bypasses the extension filter.
	got, err = LocateFiles([]string{txt}, exts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{txt}) {
		t.Fatalf("explicit file: got %v want %v", got, []string{txt})
	}

	// Dedup across overlapping roots (file also reachable via dir).
	got, err = LocateFiles([]string{dir, aJpg}, exts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedup: got %v want %v", got, want)
	}

	// Nonexistent root is a hard error.
	if _, err := LocateFiles([]string{filepath.Join(dir, "nope")}, exts); err == nil {
		t.Fatal("expected error for nonexistent root")
	}
}
