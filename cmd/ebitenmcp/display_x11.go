//go:build linux || freebsd || netbsd || openbsd

package main

// platformUsesDisplay is whether Ebitengine opens its window through an X
// server here, and so whether `run` has anything to arrange before starting a
// game.
const platformUsesDisplay = true
