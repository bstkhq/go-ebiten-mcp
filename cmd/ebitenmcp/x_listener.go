package main

import (
	"net"
	"runtime"
	"time"
)

// Linux libX11/libxcb try the abstract Unix socket before the filesystem
// socket. Disabling only "unix" leaves that listener enabled, without touching
// WSLg's read-only /tmp/.X11-unix. Other X11 platforms use filesystem sockets.
func xListenerArgs() []string {
	args := []string{"-nolisten", "tcp"}
	if runtime.GOOS == "linux" {
		args = append(args, "-nolisten", "unix")
	}
	return args
}

func displayListening(display string) bool {
	address := socketPath(display)
	if runtime.GOOS == "linux" {
		address = "@" + address
	}
	conn, err := net.DialTimeout("unix", address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
