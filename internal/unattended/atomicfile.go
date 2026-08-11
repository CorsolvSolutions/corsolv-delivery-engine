package unattended

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a temporary file and a rename.
//
// Every durable file this package writes — owner evidence, the run journal's
// checkpoint, the published heartbeat — is read by something else while a run
// is in flight. A partially written one would be read as a corrupt fact rather
// than as a write in progress, so no reader is ever shown one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("syncing %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", tmpName, err)
	}
	// Windows will not rename onto an existing file that another handle has
	// open; removing first keeps the operation portable. The window this opens
	// is bounded by the same directory lock every writer here already holds.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replacing %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %q to %q: %w", tmpName, path, err)
	}
	return nil
}
