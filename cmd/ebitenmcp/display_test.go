//go:build unix

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Whoever sets EBITENMCP_DISPLAY is counting on that display and on what it
// renders with. One that is named and not there must stop the command, not
// start a display of its own behind the owner's back.
func TestAKeptDisplayIsUsedOnlyWhenItIsAnAbsoluteSocketThatAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "X:99")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if got, err := sharedDisplay(path); err != nil || got != path {
		t.Errorf("sharedDisplay(listening) = %q, %v; want it used", got, err)
	}
	for _, display := range []string{"unix:" + path, "X:99", ":99", path + "0"} {
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
	t.Setenv(DisplayEnvName, "/nowhere/X:99")

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
	display := filepath.Join(t.TempDir(), "X:99")
	listener, err := net.Listen("unix", display)
	if err != nil {
		t.Fatal(err)
	}

	stopped, stop := context.WithCancel(context.Background())
	stop()
	if err := watchWithin(t, stopped, display, os.Getppid()); err != nil {
		t.Errorf("asked to stop: %v; want nil", err)
	}

	if err := watchWithin(t, context.Background(), display, -1); err != nil {
		t.Errorf("owner gone: %v; want nil", err)
	}

	listener.Close()
	err = watchWithin(t, context.Background(), display, os.Getppid())
	if err == nil || !strings.Contains(err.Error(), "stopped answering") {
		t.Errorf("server gone: %v; want it said", err)
	}
}

// watchWithin fails the test rather than hang it when the watch never returns.
func watchWithin(t *testing.T, ctx context.Context, display string, owner int) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- watchDisplay(ctx, display, owner, time.Millisecond) }()
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
	display, stop, err := startSharedDisplay(context.Background(), dir, "64x64")
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

	// A game, not a probe: Ebitengine opens the display twice, through libX11 for
	// GLFW and through jezek/xgb to measure the monitor, and the second read the
	// first form this display was given as a panic before any test ran.
	game := exec.Command("unshare", "-rn", "go", "test", "-count=1", "-run", "^TestTicksAdvance$", modulePath)
	game.Env = setEnvironment(gpuEnvironment(os.Environ()), "DISPLAY", display)
	if out, err := game.CombinedOutput(); err != nil {
		t.Fatalf("an Ebitengine test on the kept display, from a private network: %v\n%s", err, out)
	}

	// A command that did not ask for the GPU still draws on it here, and leaves
	// the display up for the next one.
	t.Setenv(GPUEnvName, "")
	t.Setenv(DisplayEnvName, display)
	// The suite runs under `ebitenmcp run --gpu`, whose variables it inherits;
	// without this a run that forgot them would pass on its parent's.
	for _, entry := range gpuEnvironment(nil) {
		unsetForTest(t, strings.SplitN(entry, "=", 2)[0])
	}
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

func unsetForTest(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		}
	})
}

// helperDisplayEnv makes this test binary run `ebitenmcp display` itself, for
// the tests that have to signal a real one. Every process it starts inherits
// it, which is how a test finds whatever a display left behind.
const helperDisplayEnv = "EBITENMCP_TEST_DISPLAY_HELPER"

func TestDisplayProcessHelper(t *testing.T) {
	dir := os.Getenv(helperDisplayEnv)
	if dir == "" {
		t.Skip("run by the tests that signal a display")
	}
	if err := displayCommand([]string{"--screen", "64x64", dir}); err != nil {
		fmt.Fprintln(os.Stderr, "display:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// A stop that arrived while the compositor was still starting took the
// default action: the display process died alone, and weston and Xwayland, in
// a process group of their own, went on drawing for nobody. The renderer probe
// is made slow so that the signal lands inside the start, after both are up.
func TestADisplayToldToStopWhileStartingLeavesNothing(t *testing.T) {
	if requireHardwareRenderer(os.Getenv(rendererEnv)) != nil {
		t.Skip("not running on a GPU display; make test-gpu runs this")
	}
	glxinfo, err := exec.LookPath("glxinfo")
	if err != nil {
		t.Skip("glxinfo is not installed")
	}

	base := t.TempDir()
	marker := filepath.Join(base, "probing")
	slow := filepath.Join(base, "bin")
	if err := os.Mkdir(slow, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ntouch " + marker + "\nsleep 3\nexec " + glxinfo + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(slow, "glxinfo"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(base, "display")
	helper := exec.Command(os.Args[0], "-test.run=^TestDisplayProcessHelper$")
	helper.Env = append(setEnvironment(os.Environ(), "PATH", slow+string(filepath.ListSeparator)+os.Getenv("PATH")),
		helperDisplayEnv+"="+dir)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			helper.Process.Kill()
			t.Fatal("the display never got as far as probing its renderer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := helper.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}

	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		helper.Process.Kill()
		t.Fatal("the display did not stop")
	}
	if left := processesWith(helperDisplayEnv + "=" + dir); len(left) > 0 {
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("processes left running after the display stopped: %v", left)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the display's directory is still there: %v", err)
	}
}

// processesWith lists the processes whose environment holds entry.
func processesWith(entry string) []int {
	var found []int
	all, _ := os.ReadDir("/proc")
	for _, p := range all {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", p.Name(), "environ"))
		if err != nil {
			continue
		}
		for _, e := range strings.Split(string(env), "\x00") {
			if e == entry {
				found = append(found, pid)
				break
			}
		}
	}
	return found
}
