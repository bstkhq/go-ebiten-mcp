//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// Killing the wrapper is not killing the game.
//
// game_start runs `ebitenmcp run`, which runs the game as a child of its own and
// takes a display down on the way out. A Process.Kill on the wrapper is SIGKILL,
// which cannot be caught, so the wrapper cannot forward it and its deferred
// cleanup never runs: game_stop answered "running: false" while the game went on
// serving MCP and the X server went on running. Several start/stop cycles left
// several of each.
//
// So the wrapper gets its own process group and the whole group is signalled.

// newProcessGroup makes cmd the leader of a group of its own, so that its
// children can be signalled with it.
func newProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateGroup asks the group to stop and then insists.
//
// SIGTERM first, because that is what lets `ebitenmcp run` stop the display it
// started; the negative pid is the group. SIGKILL only if the group is still
// there when the grace runs out, since a display left running is a worse outcome
// than waiting a second for one to be tidied away.
func terminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// No group — the wrapper never started one, or it is already gone.
		cmd.Process.Signal(syscall.SIGTERM)
	}
}

// killGroup is the fallback when the group ignored the polite request.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
		cmd.Process.Kill()
	}
}
