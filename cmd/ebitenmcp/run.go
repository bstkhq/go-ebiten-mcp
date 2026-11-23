package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/wire"
)

// parseRunFlags reads the flags and returns what is left, which is the command
// to run.
//
// Separate from runCommand because it was a loop containing two switches over
// the same variable and a goto out of one of them, in front of a function that
// then did four other jobs. The flags stop where `--` is, or at the first
// argument that is not a flag.
func parseRunFlags(args []string) (runOptions, []string, error) {
	opts := runOptions{screen: defaultScreen, addr: wire.DefaultAddr, mode: "auto"}

	rest, err := parseFlags(args, map[string]flagSpec{
		"--gpu":          boolFlag(func() { opts.gpu = true }),
		"--keep-display": boolFlag(func() { opts.keep = true }),
		"--screen":       stringFlag(func(v string) { opts.screen = v }),
		"--display":      stringFlag(func(v string) { opts.display = v }),
		"--addr":         stringFlag(func(v string) { opts.addr = v }),
		"--x":            checkedFlag(func(v string) error { return setXMode(&opts.mode, v) }),
	})
	if err != nil {
		return opts, nil, err
	}
	if opts.gpu, err = hostGPU(opts.gpu); err != nil {
		return opts, nil, err
	}
	return opts, rest, nil
}

// setXMode is shared by `run` and `mcp`, which take the same flag and used to
// validate it in one of the two.
func setXMode(mode *string, v string) error {
	switch v {
	case "local", "container", "auto":
		*mode = v
		return nil
	default:
		return fmt.Errorf("--x takes local, container or auto, not %q", v)
	}
}

// defaultScreen is the size of the private display. Big
// enough for a game to lay out normally, small enough to read back quickly.
const defaultScreen = "1280x720"

// `ebitenmcp run` starts a display and runs a command against it.
//
// This exists because the library cannot do it. Ebitengine opens a window while
// internal/ui initialises, which happens before main, so by the time any code in
// the game runs it has already panicked for want of a DISPLAY. Nothing inside
// the process can be early enough; it has to be a process in front.
//
//	ebitenmcp run ./mygame            # the game, with the server on
//	ebitenmcp run --gpu ./mygame      # the same, rendering on the GPU
//	ebitenmcp run go test ./...       # the package's tests, headless
//
// Each run owns its display, even when DISPLAY already names a desktop.

type runOptions struct {
	gpu     bool
	screen  string
	display string
	addr    string
	keep    bool

	// mode is local, container or auto. Auto prefers a local X server, because
	// it is one process rather than a container, and falls back to the
	// container when there is none installed — which is the case this whole
	// path exists for.
	mode string
}

// usesDisplay is the platform's answer, in a variable so that a test can ask
// the other one. The branch that matters most here is the branch this build
// cannot reach.
var usesDisplay = platformUsesDisplay

func runCommand(args []string) error {
	opts, args, err := parseRunFlags(args)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return fmt.Errorf("run needs a command, for example: ebitenmcp run ./mygame")
	}

	env := os.Environ()
	if opts.gpu {
		env = gpuEnvironment(env)
	}
	display := os.Getenv("DISPLAY")
	where := ""
	var stopDisplay func()

	// Never borrow the desktop (including WSLg's X0). Native-window platforms
	// still need no X server. A display somebody keeps up for many commands is
	// not borrowing: it was started for this, and its owner stops it.
	shared := os.Getenv(DisplayEnvName)
	if usesDisplay && shared != "" {
		if opts.display != "" {
			return fmt.Errorf("--display %s and %s=%s each name a display; give one", opts.display, DisplayEnvName, shared)
		}
		if display, err = sharedDisplay(shared); err != nil {
			return err
		}
		// A kept display is always a GPU one, and a game on it without the GPU's
		// environment draws on llvmpipe there all the same.
		if !opts.gpu {
			opts.gpu = true
			env = gpuEnvironment(env)
		}
		where = "reusing the display kept up"
	} else if usesDisplay {
		display, where, stopDisplay, err = startDisplay(opts)
		if err != nil {
			return fmt.Errorf("starting a private X display: %w; on WSL, leave WSLg's read-only /tmp/.X11-unix and X0 unchanged; Linux X servers must support abstract sockets (-nolisten unix)", err)
		}
		if !opts.keep {
			defer stopDisplay()
		}
	}

	if usesDisplay && display != "" {
		env = setEnvironment(env, "DISPLAY", display)
	}

	// A requested GPU run must be one, rather than a successful software run
	// with meaningless timings.
	if usesDisplay {
		found := rendererForMode(display, opts.gpu)
		fmt.Fprintf(os.Stderr, "ebitenmcp: %s on %s, rendering with %s\n", where, display, found)
		if opts.gpu {
			if err := requireHardwareRenderer(found); err != nil {
				if opts.keep && stopDisplay != nil {
					stopDisplay()
				}
				return err
			}
		}

		// Passed on so that anything downstream can record which rasteriser it
		// was looking at. A golden image is only comparable against one.
		env = setEnvironment(env, rendererEnv, found)
	}

	// The address is what turns the server on, so `run` sets it. Anything
	// already in the environment wins, including an empty value for somebody
	// who wants the display but not the server.
	if _, set := os.LookupEnv(AddrEnvName); !set && opts.addr != "" {
		env = append(env, AddrEnvName+"="+opts.addr)
		fmt.Fprintf(os.Stderr, "ebitenmcp: the game will serve MCP on http://%s/mcp\n", opts.addr)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Forward signals rather than dying and orphaning the game: a Ctrl-C is
	// meant for what is being run, not for this wrapper.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", args[0], err)
	}

	go func() {
		for s := range signals {
			cmd.Process.Signal(s)
		}
	}()

	return cmd.Wait()
}

// From internal/wire, which is where the library takes them from too, so they
// can no longer drift. What must not happen is importing the library itself:
// it, which would make this command need a display of its own to start.
const (
	AddrEnvName = wire.AddrEnv
	rendererEnv = wire.RendererEnv
)

// renderer asks the display what it will actually draw with.
//
// It is reported out loud because a run that quietly fell back to software
// still passes every test it is given, while every timing it produces is a lie.
// "llvmpipe" where "radeonsi" was expected is the single most useful line in
// the output.
// rendererTimeout bounds glxinfo, which x_start and x_status both reach. A dead
// display can leave it waiting on a connection that will never be answered, and
// a tool that hangs on a display probe is worse than one that says it could not
// tell.
const rendererTimeout = 10 * time.Second

func renderer(display string) string {
	return rendererWithEnvironment(display, os.Environ())
}

func rendererForGPU(display string) string {
	return rendererWithEnvironment(display, gpuEnvironment(os.Environ()))
}

func rendererForMode(display string, gpu bool) string {
	if gpu {
		return rendererForGPU(display)
	}
	return renderer(display)
}

func rendererWithEnvironment(display string, env []string) string {
	return rendererWithin(context.Background(), display, env)
}

// rendererWithin is the probe under a caller's context as well as its own
// deadline, so that a display told to stop while it is still being probed does
// not wait for glxinfo first.
func rendererWithin(parent context.Context, display string, env []string) string {
	path, err := exec.LookPath("glxinfo")
	if err != nil {
		return "unknown (install mesa-utils to find out)"
	}

	ctx, cancel := context.WithTimeout(parent, rendererTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-B")
	cmd.Env = setEnvironment(env, "DISPLAY", display)

	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "OpenGL renderer string: "); ok {
			return strings.TrimSpace(name)
		}
	}
	return "unknown"
}

func setEnvironment(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func requireHardwareRenderer(found string) error {
	lower := strings.ToLower(found)
	if strings.HasPrefix(lower, "unknown") {
		return fmt.Errorf("--gpu could not verify the renderer (%s); install glxinfo from mesa-utils", found)
	}

	for _, software := range []string{"llvmpipe", "softpipe", "swrast", "software rasterizer"} {
		if strings.Contains(lower, software) {
			return fmt.Errorf("--gpu reached the software renderer %q; refusing to report GPU results", found)
		}
	}
	return nil
}

// startDisplay brings up a display and returns it, a description of where it
// came from, and the way to stop it.
//
// A local server is preferred when one is installed: it is a process rather than
// a container, it starts in a fraction of the time, and it needs no image. The
// container is for the machine that has no X and no way to get one, which is the
// only reason any of this exists.
func startDisplay(opts runOptions) (string, string, func(), error) {
	local := "Xvfb"
	if opts.gpu {
		local = "weston"
	}

	_, err := exec.LookPath(local)
	haveLocal := err == nil

	useContainer := opts.mode == "container" || (opts.mode == "auto" && !haveLocal)
	if useContainer {
		x := newXOptions()
		x.gpu, x.screen, x.display = opts.gpu, opts.screen, opts.display
		x.name = "ebitenmcp-run-" + strings.ToLower(rand.Text())

		display, started, err := startXContainer(context.Background(), x)
		if err != nil {
			return "", "", nil, err
		}

		if !started {
			return "", "", nil, fmt.Errorf("private container name %s is already in use", x.name)
		}
		return display, "started an X server in a container", func() { stopXContainer(x) }, nil
	}

	if !haveLocal {
		return "", "", nil, fmt.Errorf("%s is not installed and --x=local was asked for; %s",
			local, installHint(local))
	}

	if opts.gpu {
		display, stop, err := startWeston(opts)
		return display, "started weston and Xwayland", stop, err
	}

	display, stop, err := startXvfb(opts)
	return display, "started Xvfb", stop, err
}

func installHint(name string) string {
	if name == "weston" {
		return "install weston and xwayland, or drop --gpu to render in software"
	}
	return "install it: apt-get install xvfb, or pacman -S xorg-server-xvfb"
}

func startXvfb(opts runOptions) (string, func(), error) {
	width, height, err := splitScreen(opts.screen)
	if err != nil {
		return "", nil, err
	}

	display, releaseReservation, err := reserveRunDisplay(opts.display)
	if err != nil {
		return "", nil, err
	}

	args := []string{display, "-screen", "0", fmt.Sprintf("%dx%dx24", width, height)}
	args = append(args, xListenerArgs()...)
	cmd := exec.Command("Xvfb", args...)
	cmd.Stderr = os.Stderr
	newProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		releaseReservation()
		return "", nil, fmt.Errorf("starting Xvfb: %w", err)
	}

	var artifacts displayArtifacts
	stopProcess := displayProcessStopper(cmd, &artifacts, "")
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			stopProcess()
			releaseReservation()
		})
	}

	if err := waitForDisplay(display); err != nil {
		artifacts, _ = captureDisplayArtifacts(display)
		stop()
		return "", nil, err
	}
	artifacts, err = captureDisplayArtifacts(display)
	if err != nil {
		stop()
		return "", nil, err
	}
	return display, stop, nil
}

// startWeston owns a compositor and a standalone Xwayland. Letting Weston
// launch Xwayland would let it create filesystem listeners and choose X0.
func startWeston(opts runOptions) (string, func(), error) {
	width, height, err := splitScreen(opts.screen)
	if err != nil {
		return "", nil, err
	}
	display, release, err := reserveRunDisplay(opts.display)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "ebitenmcp-runtime-")
	if err != nil {
		release()
		return "", nil, fmt.Errorf("creating private XDG_RUNTIME_DIR: %w", err)
	}
	x := newXOptions()
	x.gpu, x.display = opts.gpu, display
	cmd := exec.Command("sh", "-c", westonCommand(x, width, height))
	cmd.Env = setEnvironment(gpuEnvironment(os.Environ()), "XDG_RUNTIME_DIR", dir)
	cmd.Stderr = os.Stderr
	newProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		release()
		return "", nil, fmt.Errorf("starting weston and Xwayland: %w", err)
	}
	var artifacts displayArtifacts
	stopProcess := displayProcessStopper(cmd, &artifacts, dir)
	stop := func() { stopProcess(); release() }
	if err := waitForDisplay(display); err != nil {
		artifacts, _ = captureDisplayArtifacts(display)
		stop()
		return "", nil, err
	}
	artifacts, err = captureDisplayArtifacts(display)
	if err != nil {
		stop()
		return "", nil, err
	}
	return display, stop, nil
}

// A display server must get SIGTERM if it is to unlink its own socket and lock.
// The fallback cleanup remembers the inodes it made: if another server reuses a
// name between shutdown and cleanup, its new files are left alone.
const displayStopGrace = 2 * time.Second

type displayArtifact struct {
	path string
	info os.FileInfo
}

type displayArtifacts []displayArtifact

func captureDisplayArtifacts(display string) (displayArtifacts, error) {
	if _, err := displayNumber(display); err != nil {
		return nil, err
	}

	var artifacts displayArtifacts
	for _, path := range []string{socketPath(display), lockPath(display)} {
		info, err := os.Lstat(path)
		if err == nil {
			artifacts = append(artifacts, displayArtifact{path: path, info: info})
			continue
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspecting display artifact %s: %w", path, err)
		}
	}
	return artifacts, nil
}

func (artifacts displayArtifacts) remove() error {
	var errs []error
	for _, artifact := range artifacts {
		current, err := os.Lstat(artifact.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("inspecting %s during cleanup: %w", artifact.path, err))
			continue
		}
		// Some filesystems can recycle an inode immediately. The timestamp
		// metadata exposed as ModTime distinguishes that replacement in the case
		// os.SameFile alone cannot.
		if !os.SameFile(artifact.info, current) ||
			!artifact.info.ModTime().Equal(current.ModTime()) {
			continue
		}
		if err := os.Remove(artifact.path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("removing %s: %w", artifact.path, err))
		}
	}
	return errors.Join(errs...)
}

func displayProcessStopper(cmd *exec.Cmd, artifacts *displayArtifacts, runtimeDir string) func() {
	var once sync.Once

	return func() {
		once.Do(func() {
			terminateGroup(cmd)

			done := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(done)
			}()

			stopped := waitForChannel(done, displayStopGrace)
			if !stopped {
				killGroup(cmd)
				stopped = waitForChannel(done, displayStopGrace)
			}
			if !stopped {
				fmt.Fprintln(os.Stderr, "ebitenmcp: display process did not stop; preserving its files")
				return
			}

			if err := artifacts.remove(); err != nil {
				fmt.Fprintf(os.Stderr, "ebitenmcp: cleaning up the display: %v\n", err)
			}
			if runtimeDir != "" {
				if err := os.RemoveAll(runtimeDir); err != nil {
					fmt.Fprintf(os.Stderr, "ebitenmcp: removing %s: %v\n", runtimeDir, err)
				}
			}
		})
	}
}

func waitForChannel(done <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func waitForDisplay(display string) error {
	for i := 0; i < 150; i++ {
		if displayListening(display) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("display %s never came up", display)
}

func reserveRunDisplay(display string) (string, func(), error) {
	if display == "" {
		return reserveFreeDisplay()
	}
	number, err := displayNumber(display)
	if err != nil || number == 0 {
		return "", nil, fmt.Errorf("invalid private display %q: expected : followed by a positive number; X0 is reserved for the desktop", display)
	}
	release, reserved, err := reserveDisplayNumber(number)
	if err != nil {
		return "", nil, err
	}
	if !reserved {
		return "", nil, fmt.Errorf("display %s is reserved by another ebitenmcp process", display)
	}
	if displayOccupied(display) {
		release()
		return "", nil, fmt.Errorf("display %s is already occupied", display)
	}
	return display, release, nil
}

func displayOccupied(display string) bool {
	return displayPathExists(socketPath(display)) || displayPathExists(lockPath(display)) || displayListening(display)
}

// reserveFreeDisplay picks and reserves a number nothing is using, by socket
// and by lock file. The two disagree often enough to be worth checking both: a
// crashed server leaves a lock behind, and a server in another container leaves
// only a socket.
func reserveFreeDisplay() (string, func(), error) {
	for n := 99; n > 50; n-- {
		release, reserved, err := reserveDisplayNumber(n)
		if err != nil {
			return "", nil, err
		}
		if !reserved {
			continue
		}

		display := fmt.Sprintf(":%d", n)
		if displayOccupied(display) {
			release()
			continue
		}
		return display, release, nil
	}
	return "", nil, fmt.Errorf("no free display number between :51 and :99")
}

// splitScreen parses a screen size into two numbers.
//
// Numbers, not the two halves of the string. They end up interpolated into a
// shell script that runs inside the container — `sh -c "... --height=%s"` — so a
// screen of "1280x720; anything" used to be a command, and x_start takes this
// argument from an MCP call. Parsing it is the fix; quoting it would be a
// promise about somebody else's shell.
func splitScreen(screen string) (width, height int, err error) {
	w, h, ok := strings.Cut(screen, "x")
	if !ok {
		return 0, 0, fmt.Errorf("screen size %q should look like 1280x720", screen)
	}

	if width, err = screenAxis(w); err != nil {
		return 0, 0, fmt.Errorf("screen width in %q: %w", screen, err)
	}
	if height, err = screenAxis(h); err != nil {
		return 0, 0, fmt.Errorf("screen height in %q: %w", screen, err)
	}
	return width, height, nil
}

// maxScreenAxis is past any real display and well short of anything that would
// ask the X server for an unreasonable allocation.
const maxScreenAxis = 16384

func screenAxis(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if n < 1 || n > maxScreenAxis {
		return 0, fmt.Errorf("%d is out of range 1..%d", n, maxScreenAxis)
	}
	return n, nil
}
