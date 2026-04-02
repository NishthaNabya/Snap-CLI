//go:build windows

package lock

import (
	"fmt"
	"os"
)

// Acquire attempts to take an exclusive lock on the file path.
// On Windows, syscall.Flock doesn't exist, so we use an atomic file creation
// fallback (O_CREATE | O_EXCL).
func Acquire(path string) (*Lock, error) {
	// O_EXCL guarantees atomic creation. If the file already exists, it fails.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("lock: another snap operation is in progress (%s)", path)
		}
		return nil, fmt.Errorf("lock: open: %w", err)
	}

	return &Lock{file: f, path: path}, nil
}

// Release closes the file and deletes the lockfile on Windows to free the lock.
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	os.Remove(l.path) // Required on Windows since we use a physical lock file
	l.file = nil
	return err
}
