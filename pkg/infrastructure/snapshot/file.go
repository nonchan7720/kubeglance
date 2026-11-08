// Package snapshot persists the store to one JSON file. It is the optional
// safety net for the case where every replica is down at once (a cluster
// reboot); the peer copy is what makes rolling updates lossless.
package snapshot

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// File stores snapshots at a path, gzip-compressed, written atomically
// (temp file + rename) so a crash mid-write leaves the previous snapshot.
type File struct {
	path string
}

// NewFile returns a File at path.
func NewFile(path string) *File { return &File{path: path} }

// Save implements services.SnapshotStore: write streams the snapshot into
// the gzip writer of a temporary file, which replaces the previous snapshot
// only once it is complete.
func (f *File) Save(write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best effort; gone after rename
	zw := gzip.NewWriter(tmp)
	if err := write(zw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// Load implements services.SnapshotStore: read gets the decompressed file.
// A missing file is ok=false, nil.
func (f *File) Load(read func(io.Reader) error) (bool, error) {
	fh, err := os.Open(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer fh.Close() //nolint:errcheck // read only
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return false, fmt.Errorf("%s: %w", f.path, err)
	}
	if err := read(zr); err != nil {
		return false, fmt.Errorf("%s: %w", f.path, err)
	}
	return true, nil
}
