//go:build unix && !linux

package ebitenmcp

import "syscall"

func dupFD(fd int) (int, error) { return syscall.Dup(fd) }

func dupTo(from, to int) error { return syscall.Dup2(from, to) }
