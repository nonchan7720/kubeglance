// Package snapshot persists the store to one JSON file. It is the optional
// safety net for the case where every replica is down at once (a cluster
// reboot); the peer copy is what makes rolling updates lossless.
package snapshot

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nonchan7720/kubeglance/pkg/domain"
)

// File stores snapshots at a path, gzip-compressed, written atomically
// (temp file + rename) so a crash mid-write leaves the previous snapshot.
type File struct {
	path string
}

// NewFile returns a File at path.
func NewFile(path string) *File { return &File{path: path} }

// Save implements services.SnapshotStore.
func (f *File) Save(snap domain.Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best effort; gone after rename
	zw := gzip.NewWriter(tmp)
	if err := json.NewEncoder(zw).Encode(snap); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode snapshot: %w", err)
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

// Load implements services.SnapshotStore. A missing file is ok=false, nil.
func (f *File) Load() (domain.Snapshot, bool, error) {
	fh, err := os.Open(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return domain.Snapshot{}, false, nil
	}
	if err != nil {
		return domain.Snapshot{}, false, err
	}
	defer fh.Close() //nolint:errcheck // read only
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return domain.Snapshot{}, false, fmt.Errorf("%s: %w", f.path, err)
	}
	var snap domain.Snapshot
	if err := json.NewDecoder(zr).Decode(&snap); err != nil {
		return domain.Snapshot{}, false, fmt.Errorf("%s: decode: %w", f.path, err)
	}
	return snap, true, nil
}
