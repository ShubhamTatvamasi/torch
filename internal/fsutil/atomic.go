// Package fsutil contains small filesystem helpers shared by the runner.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteAtomic writes data to path through a sibling temp file and a rename.
// A crash leaves either the old contents or the new contents, never a partial
// write. The temp file is created in the destination directory so the rename
// stays on one filesystem.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
