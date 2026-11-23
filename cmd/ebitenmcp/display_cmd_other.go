//go:build !unix

package main

import (
	"fmt"

	"github.com/bstkhq/go-ebiten-mcp/internal/wire"
)

// DisplayEnvName is the display `run` reuses; there is none to keep up here.
const DisplayEnvName = wire.DisplayEnv

func displayCommand([]string) error {
	return fmt.Errorf("there is no X display on this platform and Ebitengine does not want one")
}

func sharedDisplay(display string) (string, error) {
	return "", fmt.Errorf("%s names %s, and this platform has no X display", DisplayEnvName, display)
}
