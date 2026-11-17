package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A server killed with SIGKILL cannot unlink its socket or lock. The wrapper
// used to do exactly that on every successful run, so a busy machine eventually
// exhausted every display number it was willing to try.
func TestXvfbStopRemovesTheDisplayItCreated(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display paths asserted below are Linux X11 paths")
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}

	// Stay outside the range normal runs use, and reuse the same small range on
	// every test invocation. Reservation marker files intentionally persist so
	// that unlock cannot race unlink; a PID-derived range would grow /tmp forever.
	var display string
	for n := 100; n < 150; n++ {
		candidate := fmt.Sprintf(":%d", n)
		if _, err := os.Lstat(socketPath(candidate)); !os.IsNotExist(err) {
			continue
		}
		if _, err := os.Lstat(lockPath(candidate)); !os.IsNotExist(err) {
			continue
		}
		display = candidate
		break
	}
	if display == "" {
		t.Fatal("could not find an unused display for the test")
	}

	got, stop, err := startXvfb(runOptions{display: display, screen: "64x64"})
	if err != nil {
		t.Fatalf("starting Xvfb: %v", err)
	}
	if got != display {
		t.Fatalf("started on %s, want %s", got, display)
	}

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})

	stop()
	stopped = true

	for _, path := range []string{socketPath(display), lockPath(display)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s remains after stop (stat error %v)", path, err)
		}
	}
}

// Choosing a number and starting Xvfb are separate operations. Without a
// reservation two agents can both choose :99, and one can mistake the other's
// socket for its own successful start.
func TestConcurrentXvfbStartsReserveDifferentDisplays(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Xvfb is exercised by Linux CI")
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}

	type result struct {
		display string
		stop    func()
		err     error
	}
	ready := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-ready
			display, stop, err := startXvfb(runOptions{screen: "64x64"})
			results <- result{display: display, stop: stop, err: err}
		}()
	}
	close(ready)

	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.err != nil {
			t.Fatalf("concurrent start: %v", got.err)
		}
		t.Cleanup(got.stop)
	}
	if first.display == second.display {
		t.Errorf("both Xvfb processes were given %s", first.display)
	}

	first.stop()
	second.stop()
	for _, display := range []string{first.display, second.display} {
		for _, path := range []string{socketPath(display), lockPath(display)} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Errorf("%s remains after concurrent stop (stat error %v)", path, err)
			}
		}
	}
}

func TestGPURefusesAnUnverifiedOrSoftwareRenderer(t *testing.T) {
	for _, renderer := range []string{
		"unknown",
		"unknown (install mesa-utils to find out)",
		"llvmpipe (LLVM 19.1.7, 256 bits)",
		"softpipe",
		"Mesa software rasterizer",
	} {
		if err := requireHardwareRenderer(renderer); err == nil {
			t.Errorf("accepted %q as a hardware renderer", renderer)
		}
	}

	if err := requireHardwareRenderer("NVIDIA GeForce RTX 2060/PCIe/SSE2"); err != nil {
		t.Errorf("refused a hardware renderer: %v", err)
	}
}

func TestNvidiaGPUSelectsTheNvidiaGLXClient(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"__GLX_VENDOR_LIBRARY_NAME=mesa",
		"__GLX_VENDOR_LIBRARY_NAME=stale-duplicate",
	}
	got := gpuEnvironmentForVendor(env, nvidiaRenderVendor)
	joined := strings.Join(got, "\n")
	if strings.Count(joined, "__GLX_VENDOR_LIBRARY_NAME=") != 1 ||
		!strings.Contains(joined, "__GLX_VENDOR_LIBRARY_NAME=nvidia") {
		t.Errorf("GLX vendor environment is %q", got)
	}
	if !strings.Contains(joined, "__NV_PRIME_RENDER_OFFLOAD=1") {
		t.Errorf("render offload environment is %q", got)
	}
	if !strings.Contains(joined, "PATH=/bin") {
		t.Errorf("unrelated environment was lost: %q", got)
	}

	amd := []string{"PATH=/bin"}
	if got := gpuEnvironmentForVendor(amd, "0x1002"); len(got) != 1 || got[0] != amd[0] {
		t.Errorf("changed an AMD environment to %q", got)
	}
}

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

// Inheriting a desktop display must never make a run borrow it.
func TestRunStartsItsOwnDisplayWhenOneIsAlreadySet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display server in this test is Linux Xvfb")
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell here")
	}

	desktop, stop, err := startXvfb(runOptions{screen: "64x64"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv("DISPLAY", desktop)
	t.Setenv(AddrEnvName, "")

	restore := usesDisplay
	usesDisplay = true
	t.Cleanup(func() { usesDisplay = restore })

	out := filepath.Join(t.TempDir(), "env")
	if err := runCommand([]string{"sh", "-c", "printf '%s' \"$DISPLAY\" > " + out}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, _ := os.ReadFile(out)
	if string(got) == desktop || len(got) == 0 {
		t.Errorf("the game got DISPLAY=%q, want its own display distinct from %s", got, desktop)
	}
	if !displayListening(desktop) {
		t.Error("run stopped the inherited display")
	}
	if displayListening(string(got)) {
		t.Error("run left its private display running")
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

func TestXvfbUsesOnlyTheAbstractSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract Unix sockets are Linux-specific")
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}
	display, stop, err := startXvfb(runOptions{screen: "64x64"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if displayPathExists(socketPath(display)) {
		t.Fatal("Xvfb created a filesystem socket")
	}
	if !displayListening(display) {
		t.Fatal("abstract socket is not listening")
	}
	if _, err := exec.LookPath("glxinfo"); err != nil {
		t.Skip("glxinfo is not installed; abstract listener verified")
	}
	if got := renderer(display); strings.HasPrefix(got, "unknown") {
		t.Fatalf("GLX client could not connect: %s", got)
	}
}

func TestReservationRejectsAnAbstractSocketWithoutALock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract Unix sockets are Linux-specific")
	}
	display, release, err := reserveFreeDisplay()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	listener, err := net.Listen("unix", "@"+socketPath(display))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release()
	if got, stop, err := reserveRunDisplay(display); err == nil {
		stop()
		t.Fatalf("reserved occupied abstract display %s", got)
	}
	other, stop, err := reserveFreeDisplay()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if other == display {
		t.Fatal("automatic reservation reused an abstract listener")
	}
}

func TestPrivateDisplayRejectsX0(t *testing.T) {
	if _, release, err := reserveRunDisplay(":0"); err == nil {
		release()
		t.Fatal("private runs must never take X0")
	}
}
