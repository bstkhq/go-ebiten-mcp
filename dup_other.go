//go:build !unix

package ebitenmcp

import "errors"

// Without descriptor duplication there is no way to catch what a C library
// writes, so trace capture is simply unavailable rather than partial and
// misleading.
var errNoFDCapture = errors.New("ebitenmcp: capturing stdout and stderr needs a unix-like platform")

func dupFD(int) (int, error) { return 0, errNoFDCapture }

func dupTo(int, int) error { return errNoFDCapture }
