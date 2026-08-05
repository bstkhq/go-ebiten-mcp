//go:build !unix

package main

import "os/exec"

// Without process groups there is nothing better than killing what was started
// and hoping its children notice. See process_unix.go for what this is for.

func newProcessGroup(*exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func killGroup(cmd *exec.Cmd) { terminateGroup(cmd) }
