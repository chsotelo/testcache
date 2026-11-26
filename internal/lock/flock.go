package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Guard represents an exclusive advisory file lock.
type Guard struct {
	file *os.File
}

// Release unlocks and closes the underlying lock file.
func (g *Guard) Release() error {
	if g == nil || g.file == nil {
		return nil
	}
	f := g.file
	g.file = nil
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock %s: %w", f.Name(), unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close lock %s: %w", f.Name(), closeErr)
	}
	return nil
}

// Acquire acquires an exclusive lock on path. If the lock is held by another process,
// onWait is called once before blocking until the lock is granted.
func Acquire(path string, onWait func()) (*Guard, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	if err := tryFlock(f); err == nil {
		return &Guard{file: f}, nil
	} else if !isWouldBlock(err) {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}

	if onWait != nil {
		onWait()
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			_ = f.Close()
			return nil, fmt.Errorf("blocking flock %s: %w", path, err)
		}
		return &Guard{file: f}, nil
	}
}

// AcquireSlot acquires one of maxSlots semaphore lock files in dir.
func AcquireSlot(dir, prefix string, maxSlots int, onWait func()) (*Guard, error) {
	if maxSlots <= 1 {
		return Acquire(filepath.Join(dir, prefix+"-0.lock"), onWait)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create semaphore dir: %w", err)
	}

	notified := false
	for {
		for i := 0; i < maxSlots; i++ {
			p := filepath.Join(dir, fmt.Sprintf("%s-%d.lock", prefix, i))
			g, ok, err := tryAcquire(p)
			if err != nil {
				return nil, err
			}
			if ok {
				return g, nil
			}
		}
		if !notified && onWait != nil {
			notified = true
			onWait()
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TryAcquire attempts to acquire an exclusive non-blocking lock on path.
// It returns (guard, true, nil) on success, or (nil, false, nil) if held elsewhere.
func TryAcquire(path string) (*Guard, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("create lock dir: %w", err)
	}
	return tryAcquire(path)
}

func tryAcquire(path string) (*Guard, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := tryFlock(f); err == nil {
		return &Guard{file: f}, true, nil
	} else if isWouldBlock(err) {
		_ = f.Close()
		return nil, false, nil
	} else {
		_ = f.Close()
		return nil, false, fmt.Errorf("try flock %s: %w", path, err)
	}
}

func tryFlock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}

func isWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
