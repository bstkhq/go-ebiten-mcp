package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// defaultScreen is the size of the display started when there is none. Big
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
// A display that already exists is used as it is, so this is a no-op on a
// desktop and does the work on a server or in CI.

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

	// Only start something if the platform wants one and there is nothing to
	// use. Running this on a desktop should change nothing about how the game
	// looks — and on macOS or Windows there is nothing to change: the window
	// comes from the operating system, DISPLAY is never set, and looking at it
	// would send `run` hunting for an X server to answer a question nobody
	// asked. It used to, and the game never started.
	if usesDisplay && os.Getenv("DISPLAY") == "" {
		display, where, stop, err := startDisplay(opts)
		if err != nil {
			return err
		}
		if !opts.keep {
			defer stop()
		}

		found := renderer(display)
		fmt.Fprintf(os.Stderr, "ebitenmcp: %s on %s, rendering with %s\n", where, display, found)

		env = append(env, "DISPLAY="+display)

		// Passed on so that anything downstream can record which rasteriser it
		// was looking at. A golden image is only comparable against one.
		env = append(env, rendererEnv+"="+found)
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
	path, err := exec.LookPath("glxinfo")
	if err != nil {
		return "unknown (install mesa-utils to find out)"
	}

	ctx, cancel := context.WithTimeout(context.Background(), rendererTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-B")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)

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
		x.gpu, x.screen = opts.gpu, opts.screen

		display, started, err := startXContainer(context.Background(), x)
		if err != nil {
			return "", "", nil, err
		}

		if !started {
			// Somebody else's, and theirs to stop.
			return display, "reusing the X server in a container", func() {}, nil
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
	display := opts.display
	if display == "" {
		var err error
		if display, err = freeDisplay(); err != nil {
			return "", nil, err
		}
	}

	width, height, err := splitScreen(opts.screen)
	if err != nil {
		return "", nil, err
	}

	cmd := exec.Command("Xvfb", display,
		"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
		"-nolisten", "tcp")
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting Xvfb: %w", err)
	}

	stop := func() { cmd.Process.Kill(); cmd.Wait() }

	if err := waitForDisplay(display); err != nil {
		stop()
		return "", nil, err
	}
	return display, stop, nil
}

var westonDisplay = regexp.MustCompile(`xserver listening on display (:\d+)`)

// startWeston brings up a headless compositor with Xwayland in front of it,
// which is the only unprivileged way to get hardware rendering without a
// screen.
//
// --renderer=gl is not optional. Weston's headless backend defaults to software
// and Xwayland on top of that falls back to llvmpipe, which looks like it
// worked and is the exact thing the GPU path exists to avoid.
func startWeston(opts runOptions) (string, func(), error) {
	// Weston needs XDG_RUNTIME_DIR to exist, not merely to be named. A
	// container image that declares the variable without creating the directory
	// is common, and weston's complaint about it — "failed to add socket: No
	// such file or directory" — points nowhere near the cause.
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("ebitenmcp-%d", os.Getpid()))
		os.Setenv("XDG_RUNTIME_DIR", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("creating XDG_RUNTIME_DIR %s: %w", dir, err)
	}
	os.Chmod(dir, 0o700)

	width, height, err := splitScreen(opts.screen)
	if err != nil {
		return "", nil, err
	}

	cmd := exec.Command("weston",
		"--backend=headless", "--renderer=gl", "--xwayland",
		fmt.Sprintf("--width=%d", width), fmt.Sprintf("--height=%d", height))

	logs, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, err
	}

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting weston: %w", err)
	}
	stop := func() { cmd.Process.Kill(); cmd.Wait() }

	// Weston picks the display number itself and only announces it in its log,
	// so read it from there rather than guessing.
	display, err := readDisplay(logs)
	if err != nil {
		stop()
		return "", nil, err
	}

	if err := waitForDisplay(display); err != nil {
		stop()
		return "", nil, err
	}
	return display, stop, nil
}

func readDisplay(logs io.Reader) (string, error) {
	found := make(chan string, 1)

	go func() {
		scanner := bufio.NewScanner(logs)
		for scanner.Scan() {
			line := scanner.Text()
			if m := westonDisplay.FindStringSubmatch(line); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
			fmt.Fprintln(os.Stderr, "weston:", line)
		}
	}()

	select {
	case display := <-found:
		return display, nil
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("weston never reported an X display; is xwayland installed?")
	}
}

func waitForDisplay(display string) error {
	socket := filepath.Join("/tmp/.X11-unix", "X"+strings.TrimPrefix(display, ":"))

	for i := 0; i < 150; i++ {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("display %s never came up", display)
}

// freeDisplay picks a number nothing is using, by socket and by lock file. The
// two disagree often enough to be worth checking both: a crashed server leaves a
// lock behind, and a server in another container leaves only a socket.
func freeDisplay() (string, error) {
	taken := takenDisplays()

	for n := 99; n > 50; n-- {
		if taken[n] {
			continue
		}
		if _, err := os.Stat(filepath.Join(os.TempDir(), fmt.Sprintf(".X%d-lock", n))); err == nil {
			continue
		}
		return fmt.Sprintf(":%d", n), nil
	}
	return "", fmt.Errorf("no free display number between :51 and :99")
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
