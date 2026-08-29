package exif

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// corpusTimeout bounds each file's decode: some conformance files are
// deliberately malformed (IFD loops, oversized counts) to catch exactly the
// kind of hang a plain panic-recover can't.
const corpusTimeout = 5 * time.Second

// TestCorpusNoCrash walks $PICTOR_CORPUS_DIR (populated by
// scripts/setup-corpus.sh, see that script for attribution) and asserts that
// Read never panics or hangs on any file with a supported extension. It says
// nothing about correctness - Errs() faults are expected and fine, only a
// panic or timeout fails the test.
func TestCorpusNoCrash(t *testing.T) {
	dir := os.Getenv("PICTOR_CORPUS_DIR")
	if dir == "" {
		t.Skip("set PICTOR_CORPUS_DIR to a codec-corpus checkout to run this test (see scripts/setup-corpus.sh)")
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !supportedExtension(path) {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		t.Run(rel, func(t *testing.T) {
			readWithTimeout(t, path)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking corpus dir: %v", err)
	}
}

func supportedExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return slices.Contains(SupportedExtensions, ext)
}

// readWithTimeout runs Read on path in a goroutine so a hang (not just a
// panic) fails the test instead of wedging the whole run.
func readWithTimeout(t *testing.T, path string) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read file: %v", err)
			return
		}
		doc, err := ReadBytes(b)
		if err != nil {
			return // fatal decode error is a legitimate outcome, not a crash
		}
		doc.Tags()
		doc.Metadata()
		doc.Errs()
	}()

	select {
	case r := <-done:
		if r != nil {
			t.Fatalf("panic: %v", r)
		}
	case <-time.After(corpusTimeout):
		t.Fatalf("timed out after %s", corpusTimeout)
	}
}
