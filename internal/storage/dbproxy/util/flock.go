// Package util provides shared utilities for the db storage backends.
package util

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/lockfile"
)

// Unlocker is the interface for releasing an acquired lock.
type Unlocker interface {
	Unlock()
}

// Lock holds an exclusive flock on a file.
type Lock struct {
	f *os.File
}

// TryLock attempts to acquire a non-blocking exclusive flock on lockPath.
// The parent directory is created (mode 0700) if it does not exist. On
// contention returns an error that satisfies lockfile.IsLocked, so callers
// can detect "another holder is alive" and produce their own contextual
// error message.
func TryLock(lockPath string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("util: creating lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath comes from caller-derived dirs, not user input
	if err != nil {
		return nil, fmt.Errorf("util: opening lock file: %w", err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Unlock releases the flock and closes the underlying file. Panics on
// failure to prevent silent deadlocks.
//
// There is intentionally no accessor for the underlying fd: the fork+exec
// lock-fd handoff it would enable was never wired up — spawned children
// acquire their own lock and signal contention via proxy.LockHeldExitCode
// instead.
func (l *Lock) Unlock() {
	if err := lockfile.FlockUnlock(l.f); err != nil {
		panic(fmt.Sprintf("util: failed to release lock: %v", err))
	}
	if err := l.f.Close(); err != nil {
		panic(fmt.Sprintf("util: failed to close lock file: %v", err))
	}
}

// NoopLock is a lock that does nothing. Used when the caller wants to skip
// lock acquisition (e.g., server mode where the external server handles its
// own concurrency).
type NoopLock struct{}

// Unlock is a no-op.
func (NoopLock) Unlock() {}
