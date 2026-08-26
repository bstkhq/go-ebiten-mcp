//go:build !(linux || freebsd || netbsd || openbsd)

package main

// No supported platform outside X11 starts Xvfb, so there is nothing to
// serialise. Keeping the stub makes the launcher compile everywhere it claims.
func reserveDisplayNumber(int) (func(), bool, error) {
	return func() {}, true, nil
}
