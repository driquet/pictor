#!/usr/bin/env bash
# Populates a local checkout of imazen/codec-corpus for exif.TestCorpusNoCrash
# (exif/corpus_test.go), scoped to the datasets relevant to pictor's
# supported formats (tif/tiff/jpg/jpeg/png/webp): TIFF, JPEG, PNG (+APNG) and
# WebP decoder-conformance corpora. The full repo is ~670MB; this pulls only
# those directories via git sparse-checkout, ~90MB.
#
# Usage:
#   ./scripts/setup-corpus.sh [target-dir]   # default: .corpus (gitignored)
#   PICTOR_CORPUS_DIR=.corpus go test ./exif/... -run TestCorpusNoCrash -v
#
# Attribution & licensing: these files are not pictor's own; they belong to
# imazen/codec-corpus and its upstream sources. Each dataset carries its own
# license (MIT/CC0/libtiff/Freeware/etc.) documented in that dataset's own
# README/LICENSE inside the checkout - see
# https://github.com/imazen/codec-corpus#format-conformance--edge-cases for
# the current per-dataset table before redistributing anything pulled here.
set -euo pipefail

TARGET="${1:-.corpus}"
REPO="https://github.com/imazen/codec-corpus.git"
DATASETS=(tiff-conformance jpeg-conformance pngsuite apng-conformance webp-conformance)

if [ -d "$TARGET/.git" ]; then
  echo "updating existing checkout at $TARGET"
  git -C "$TARGET" pull --depth 1
else
  echo "cloning codec-corpus (sparse) into $TARGET"
  git clone --depth 1 --filter=blob:none --sparse "$REPO" "$TARGET"
fi

git -C "$TARGET" sparse-checkout set "${DATASETS[@]}"

echo "done. point PICTOR_CORPUS_DIR at $TARGET (or a subdirectory) to run the crash-corpus test."
