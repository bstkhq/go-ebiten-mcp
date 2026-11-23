//go:build unix

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Whoever sets EBITENMCP_DISPLAY is counting on that display and on what it
// renders with. One that is named and not there must stop the command, not
// start a display of its own behind the owner's back.
func TestAKeptDisplayIsUsedOnlyWhenItIsAnAbsoluteSocketThatAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "X99")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if got, err := sharedDisplay("unix:" + path); err != nil || got != "unix:"+path {
		t.Errorf("sharedDisplay(listening) = %q, %v; want it used", got, err)
	}
	for _, display := range []string{path, "unix:X99", ":99", "unix:" + path + "-gone"} {
		if _, err := sharedDisplay(display); err == nil {
			t.Errorf("sharedDisplay(%q) was accepted", display)
		}
	}
}

// --display and a kept display each choose where the game draws; silently
// preferring one would put a run on a display nobody meant.
func TestRunRefusesTwoNamedDisplays(t *testing.T) {
	if !usesDisplay {
		t.Skip("this platform has no X display")
	}
	t.Setenv(DisplayEnvName, "unix:/nowhere/X99")

	err := runCommand([]string{"--display", ":98", "--", "true"})
	if err == nil || !strings.Contains(err.Error(), "give one") {
		t.Errorf("runCommand with both = %v; want it refused", err)
	}
}

// The socket opens every window on the display to whoever can reach it.
func TestTheDisplaysDirectoryIsThisUsersAlone(t *testing.T) {
	base := t.TempDir()

	fresh := filepath.Join(base, "fresh")
	if made, err := privateDirectory(fresh); err != nil || !made {
		t.Errorf("privateDirectory(new) = %v, %v; want it made", made, err)
	}
	if made, err := privateDirectory(fresh); err != nil || made {
		t.Errorf("privateDirectory(existing 0700) = %v, %v; want it accepted, not made", made, err)
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := privateDirectory(open); err == nil {
		t.Error("a directory others can enter was accepted")
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(fresh, link); err != nil {
		t.Fatal(err)
	}
	if _, err := privateDirectory(link); err == nil {
		t.Error("a symlink was accepted as the display's directory")
	}

	if _, err := privateDirectory("relative"); err == nil {
		t.Error("a relative directory was accepted")
	}
}

// A display nobody owns is VRAM nobody frees, and one whose server died must
// not be reported as kept up.
func TestAKeptDisplayGoesWhenAskedWhenItsOwnerGoesAndSaysSoWhenItDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "X99")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	display := "unix:" + path

	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	if err := watchWithin(t, display, os.Getppid(), signals); err != nil {
		t.Errorf("asked to stop: %v; want nil", err)
	}

	if err := watchWithin(t, display, -1, make(chan os.Signal)); err != nil {
		t.Errorf("owner gone: %v; want nil", err)
	}

	listener.Close()
	err = watchWithin(t, display, os.Getppid(), make(chan os.Signal))
	if err == nil || !strings.Contains(err.Error(), "stopped answering") {
		t.Errorf("server gone: %v; want it said", err)
	}
}

// watchWithin fails the test rather than hang it when the watch never returns.
func watchWithin(t *testing.T, display string, owner int, signals chan os.Signal) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- watchDisplay(display, owner, signals, time.Millisecond) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the watch kept the display up")
		return nil
	}
}

// The reason the display is a file at all: an abstract socket belongs to one
// network namespace, and commands that run in a private network could not
// reach a display started outside it. Only on a GPU, the one place a kept
// display can be: `make test-gpu`.
func TestAKeptDisplayDrawsOnTheGPUForAPrivateNetworkAndLeavesNothing(t *testing.T) {
	if requireHardwareRenderer(os.Getenv(rendererEnv)) != nil {
		t.Skip("not running on a GPU display; make test-gpu runs this")
	}
	for _, tool := range []string{"weston", "Xwayland", "glxinfo", "unshare"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if exec.Command("unshare", "-rn", "true").Run() != nil {
		t.Skip("unprivileged network namespaces are not allowed here")
	}

	dir := filepath.Join(t.TempDir(), "display")
	display, stop, err := startSharedDisplay(dir, "64x64")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})

	probe := exec.Command("unshare", "-rn", "glxinfo", "-B")
	probe.Env = setEnvironment(gpuEnvironment(os.Environ()), "DISPLAY", display)
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("glxinfo in a private network: %v\n%s", err, out)
	}
	found := "unknown"
	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "OpenGL renderer string: "); ok {
			found = name
		}
	}
	if err := requireHardwareRenderer(found); err != nil {
		t.Errorf("from a private network: %v", err)
	}

	// A command that did not ask for the GPU still draws on it here, and leaves
	// the display up for the next one.
	t.Setenv(GPUEnvName, "")
	t.Setenv(DisplayEnvName, display)
	seen := filepath.Join(t.TempDir(), "renderer")
	if err := runCommand([]string{"--addr", "", "--", "sh", "-c", "glxinfo -B > " + seen}); err != nil {
		t.Fatalf("run on the kept display: %v", err)
	}
	report, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "OpenGL renderer string: "+found) {
		t.Errorf("a command run on the kept display saw another renderer than %q:\n%s", found, report)
	}
	if !sharedDisplayListening(display) {
		t.Fatal("run took the kept display down")
	}

	stop()
	stopped = true
	if sharedDisplayListening(display) {
		t.Error("the display still answers after stop")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory the display made is still there: %v", err)
	}
}
