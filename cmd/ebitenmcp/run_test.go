package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `run` decides whether to arrange a display, and it used to decide on DISPLAY
// alone. On macOS and Windows that variable is never set, so it went looking
// for an X server on a platform that has none and wants none: no Xvfb, so the
// container path, so "neither podman nor docker is installed" — for a game that
// would have opened its own window and run.
//
// The branch cannot be reached by a build on this machine, which is why
// usesDisplay is a variable.

func TestRunArrangesNothingWherePlatformsOpenTheirOwnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child below is a shell script")
	}

	t.Setenv("DISPLAY", "")
	t.Setenv(AddrEnvName, "")

	restore := usesDisplay
	usesDisplay = false
	t.Cleanup(func() { usesDisplay = restore })

	// The child records the environment it was given, which is the only way to
	// see from out here what run decided.
	out := filepath.Join(t.TempDir(), "env")
	script := "printf '%s' \"$DISPLAY\" > " + out

	if err := runCommand([]string{"sh", "-c", script}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the child never ran: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("it gave the game DISPLAY=%q on a platform that has none", got)
	}
}

// And where the platform does use one, nothing changes: a display already set
// is left alone rather than replaced.
func TestRunLeavesADisplayThatIsAlreadyThere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child below is a shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell here")
	}

	t.Setenv("DISPLAY", ":42")
	t.Setenv(AddrEnvName, "")

	restore := usesDisplay
	usesDisplay = true
	t.Cleanup(func() { usesDisplay = restore })

	out := filepath.Join(t.TempDir(), "env")
	if err := runCommand([]string{"sh", "-c", "printf '%s' \"$DISPLAY\" > " + out}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, _ := os.ReadFile(out)
	if string(got) != ":42" {
		t.Errorf("the game got DISPLAY=%q, want the :42 that was already set", got)
	}
}

// TestXSaysSoWherePlatformsOpenTheirOwnWindows: the containerised X server is
// the one part of this command that is only ever right on a platform with an X
// display, and asking for it elsewhere used to end in a message about podman.
func TestXSaysSoWherePlatformsOpenTheirOwnWindows(t *testing.T) {
	restore := usesDisplay
	usesDisplay = false
	t.Cleanup(func() { usesDisplay = restore })

	err := xCommand([]string{"start"})
	if err == nil {
		t.Fatal("it tried to start an X server on a platform that has none")
	}
	if strings.Contains(err.Error(), "podman") || strings.Contains(err.Error(), "docker") {
		t.Errorf("the answer is about container engines rather than the platform: %v", err)
	}
	if !strings.Contains(err.Error(), "own window") {
		t.Errorf("it does not say what to do instead: %v", err)
	}
}
