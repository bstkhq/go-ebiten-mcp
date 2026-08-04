//go:build linux

package ebitenmcp

import "syscall"

// Linux dropped dup2 on newer architectures, so dup3 is the one that exists
// everywhere.
func dupFD(fd int) (int, error) { return syscall.Dup(fd) }

func dupTo(from, to int) error { return syscall.Dup3(from, to, 0) }
