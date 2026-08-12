//go:build !unix

package ebitenmcp

func dupFD(int) (int, error) { return 0, errNoFDCapture }

func dupTo(int, int) error { return errNoFDCapture }
