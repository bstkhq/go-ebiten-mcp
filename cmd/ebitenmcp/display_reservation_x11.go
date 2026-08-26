//go:build linux || freebsd || netbsd || openbsd

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// reserveDisplayNumber serialises the gap between choosing a display and Xvfb
// creating its own lock. The marker stays in /tmp: flock is released by the
// kernel even when the wrapper is killed, while unlinking the marker would
// introduce a second race between unlock and removal.
func reserveDisplayNumber(number int) (release func(), reserved bool, err error) {
	path := filepath.Join(filepath.Dir(x11SocketDir), fmt.Sprintf(".ebitenmcp-X%d", number))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("opening display reservation %s: %w", path, err)
	}

	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reserving display :%d: %w", number, err)
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			_ = file.Close()
		})
	}
	return release, true, nil
}
