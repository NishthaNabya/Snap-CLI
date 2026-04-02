// Package lock provides filesystem-based advisory locking.
// This prevents concurrent Snap operations from corrupting the store.
package lock

import "os"

// Lock represents an acquired file-system advisory lock.
type Lock struct {
	file *os.File
	path string
}
