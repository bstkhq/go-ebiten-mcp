//go:build !(linux || freebsd || netbsd || openbsd)

package main

// platformUsesDisplay is false where the window comes from the operating system
// itself — Cocoa on macOS, Win32 on Windows. DISPLAY is never set there and
// setting one would mean nothing, so there is nothing for `run` to arrange.
const platformUsesDisplay = false
