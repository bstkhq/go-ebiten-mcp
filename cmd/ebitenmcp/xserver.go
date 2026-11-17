package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// `ebitenmcp x` runs an X server in a container so that a machine without one
// can still draw.
//
// It is the fallback, not the main event. Debugging a game usually means
// attaching to one that is already running on a machine that already has a
// screen. This is for the other case: tests, CI, a server, an agent — where
// there is no display and often no way to install one.
//
// The game never runs in here. It runs wherever you build it and connects over
// abstract Unix socket, so nothing about your binary's libraries is
// this container's business.
//
//	ebitenmcp x start [--gpu]    # prints the DISPLAY to use
//	ebitenmcp x stop
//	ebitenmcp x status

//go:embed xserver.Containerfile
var xserverContainerfile []byte

// engineTimeout bounds every call to the container engine.
//
// Not one of them had a deadline, including the `logs` inside waitForXwayland's
// own sixty-second loop — which meant that loop could not time out either, since
// the thing it was polling with was what hung. All of it is reachable from
// x_start.
const engineTimeout = 30 * time.Second

// engineCommand builds a call to the container engine with a deadline, and the
// cancel that releases it. Every caller defers the cancel; a command that
// finished must not leave its timer standing until it would have fired.
func engineCommand(timeout time.Duration, engine string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return exec.CommandContext(ctx, engine, args...), cancel
}

// engineRun, engineOutput and engineCombined are the three shapes this file
// uses. They exist so that the deadline and its cleanup are written once rather
// than at each of a dozen call sites, where forgetting the cancel looks like
// nothing at all.
func engineRun(engine string, args ...string) error {
	cmd, cancel := engineCommand(engineTimeout, engine, args...)
	defer cancel()

	return cmd.Run()
}

func engineOutput(engine string, args ...string) ([]byte, error) {
	cmd, cancel := engineCommand(engineTimeout, engine, args...)
	defer cancel()

	return cmd.Output()
}

func engineCombined(engine string, args ...string) ([]byte, error) {
	cmd, cancel := engineCommand(engineTimeout, engine, args...)
	defer cancel()

	return cmd.CombinedOutput()
}

// buildTimeout is its own number because building the image is the one call
// here that legitimately takes minutes: it happens once, on a machine that has
// never run this, and it is downloading an X server.
const buildTimeout = 15 * time.Minute

// X servers hardcode this path. Linux also uses it as the name of an abstract
// socket, without accessing the filesystem directory.
const x11SocketDir = "/tmp/.X11-unix"

// renderNode is the only piece of the GPU the container is given. A render node
// is enough for rendering and grants nothing else — no modesetting, no other
// client's buffers — which is what makes this work without privileges.
const (
	renderNode           = "/dev/dri/renderD128"
	renderNodeVendorPath = "/sys/class/drm/renderD128/device/vendor"
)

type xOptions struct {
	gpu     bool
	screen  string
	image   string
	engine  string
	name    string
	display string
}

func newXOptions() xOptions {
	return xOptions{screen: defaultScreen, name: "ebitenmcp-x"}
}

func xCommand(args []string) error {
	opts := newXOptions()

	if !usesDisplay {
		return fmt.Errorf("there is no X display on this platform and Ebitengine does not want one: " +
			"a game opens its own window, so `ebitenmcp run` needs nothing arranged first")
	}

	if len(args) == 0 {
		return fmt.Errorf("x needs start, stop or status")
	}
	action := args[0]
	args = args[1:]

	if _, err := parseFlags(args, map[string]flagSpec{
		"--gpu":    boolFlag(func() { opts.gpu = true }),
		"--screen": stringFlag(func(v string) { opts.screen = v }),
		"--image":  stringFlag(func(v string) { opts.image = v }),
		"--name":   stringFlag(func(v string) { opts.name = v }),
	}); err != nil {
		return err
	}

	switch action {
	case "start":
		display, started, err := startXContainer(context.Background(), opts)
		if err != nil {
			return err
		}
		found := rendererForMode(display, opts.gpu)
		if opts.gpu {
			if err := requireHardwareRenderer(found); err != nil {
				if started {
					err = errors.Join(err, stopXContainer(opts))
				}
				return err
			}
		}

		what := "X server on"
		if !started {
			what = "reusing the X server already on"
		}

		fmt.Println(display)
		fmt.Fprintf(os.Stderr, "ebitenmcp: %s %s, rendering with %s\n"+
			"ebitenmcp: use it with DISPLAY=%s, and stop it with `ebitenmcp x stop`\n",
			what, display, found, display)
		return nil

	case "stop":
		return stopXContainer(opts)

	case "status":
		return xStatus(opts)

	default:
		return fmt.Errorf("x takes start, stop or status, not %q", action)
	}
}

// containerEngine finds something that can run a container.
func containerEngine(preferred string) (string, error) {
	candidates := []string{"podman", "docker"}
	if preferred != "" {
		candidates = []string{preferred}
	}

	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("neither podman nor docker is installed; " +
		"with weston and xwayland installed locally no container is needed, " +
		"and a machine that already has a display needs none of this")
}

// imageTag names the image after the contents of the Containerfile, so editing
// it rebuilds and leaving it alone does not.
func imageTag() string {
	sum := sha256.Sum256(xserverContainerfile)
	return "ebitenmcp-x:" + hex.EncodeToString(sum[:])[:12]
}

// ensureImage builds the image if it is not already there.
func ensureImage(ctx context.Context, engine string, opts xOptions) (string, error) {
	if opts.image != "" {
		return opts.image, nil
	}

	tag := imageTag()
	if engineRun(engine, "image", "exists", tag) == nil {
		return tag, nil
	}
	// docker has no `image exists`; inspect answers the same question.
	if engineRun(engine, "image", "inspect", tag) == nil {
		return tag, nil
	}

	dir, err := os.MkdirTemp("", "ebitenmcp-x")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(path, xserverContainerfile, 0o644); err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "ebitenmcp: building the X server image %s, once\n", tag)

	// Under the caller's context as well as its own deadline, so cancelling the
	// MCP call that triggered a fifteen-minute image build actually stops it.
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	build := exec.CommandContext(buildCtx, engine, "build", "-t", tag, "-f", path, dir)

	build.Stdout, build.Stderr = os.Stderr, os.Stderr

	if err := build.Run(); err != nil {
		return "", fmt.Errorf("building %s: %w", tag, err)
	}
	return tag, nil
}

var xwaylandDisplay = regexp.MustCompile(`xserver listening on display (:\d+)`)

// startXContainer brings up the server and returns the display to point at,
// along with whether this call is the one that started it.
//
// That second value matters: a caller that reused somebody else's server has no
// business stopping it on the way out.
func startXContainer(ctx context.Context, opts xOptions) (display string, started bool, err error) {
	engine, err := containerEngine(opts.engine)
	if err != nil {
		return "", false, err
	}

	if display, ok := runningDisplay(engine, opts); ok {
		// Reuse only a server that renders the way this run asked for.
		//
		// Reusing one regardless is how `make test-gpu` after `make test` ends
		// up on llvmpipe: everything passes, nothing was tested on the GPU, and
		// the only sign is a renderer name in a line nobody reads. The container
		// is this tool's own, by name, so replacing it is fair — and it says so.
		if hasGPU(engine, opts.name) == opts.gpu {
			return display, false, nil
		}

		fmt.Fprintf(os.Stderr, "ebitenmcp: the X server on %s renders the other way; replacing it\n", display)
		if err := stopXContainer(opts); err != nil {
			return "", false, err
		}
	}

	// Remove a container left behind by an earlier run whose display is gone.
	_ = removeXContainer(engine, opts.name)

	image, err := ensureImage(ctx, engine, opts)
	if err != nil {
		return "", false, err
	}

	width, height, err := splitScreen(opts.screen)
	if err != nil {
		return "", false, err
	}

	display, release, err := reserveRunDisplay(opts.display)
	if err != nil {
		return "", false, err
	}
	defer release()
	opts.display = display

	var run []string
	if opts.gpu {
		run = append(run, gpuContainerGlobalArgs(engine)...)
	}
	run = append(run,
		"run", "-d", "--name", opts.name,
		"--network", "host",
	)
	if runtime.GOOS != "linux" {
		// Non-Linux X11 clients need the filesystem transport.
		run = append(run, "-v", x11SocketDir+":"+x11SocketDir)
	}
	if opts.gpu {
		run = append(run, gpuContainerArgs(engine)...)
	}
	run = append(run, image, "sh", "-c", westonCommand(opts, width, height))

	out, err := engineCombined(engine, run...)
	if err != nil {
		return "", false, fmt.Errorf("starting the X server: %w: %s", err, strings.TrimSpace(string(out)))
	}

	display, err = waitForXwayland(engine, opts.name)
	if err != nil {
		logs, _ := engineCombined(engine, "logs", opts.name)
		_ = removeXContainer(engine, opts.name)
		return "", false, fmt.Errorf("%w\n%s", err, tail(string(logs), 15))
	}
	return display, true, nil
}

// Rootless podman otherwise drops the supplementary render group. The device
// still appears inside the container, so checking only for its path says GPU
// while Weston quietly falls back to software. keep-groups needs crun; podman
// reports that requirement directly on hosts still configured with runc.
func gpuContainerArgs(engine string) []string {
	args := []string{"--device", renderNode}
	if filepath.Base(engine) == "podman" {
		args = append(args, "--group-add", "keep-groups")
	}
	return args
}

func gpuContainerGlobalArgs(engine string) []string {
	if filepath.Base(engine) == "podman" {
		return []string{"--runtime", "crun"}
	}
	return nil
}

// westonCommand keeps Wayland state private and starts Xwayland itself so
// Weston cannot bind filesystem X sockets or select the desktop's X0. All
// interpolated values are validated numbers or internally chosen constants.
func westonCommand(opts xOptions, width, height int) string {
	renderer := "pixman"
	if opts.gpu {
		renderer = "gl"
	}
	return fmt.Sprintf(`set -eu
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"
export WAYLAND_DISPLAY=ebitenmcp-wayland
weston --no-config --backend=headless --renderer=%[1]s --socket="$WAYLAND_DISPLAY" --width=%[2]d --height=%[3]d &
weston_pid=$!
trap 'kill "$weston_pid" 2>/dev/null || true' EXIT
attempt=0
while [ ! -S "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY" ]; do
    kill -0 "$weston_pid"
    attempt=$((attempt + 1))
    [ "$attempt" -lt 150 ] || exit 1
    sleep 0.1
done
echo 'xserver listening on display %[4]s' >&2
exec Xwayland %[4]s %[5]s -ac -geometry %[2]dx%[3]d`,
		renderer, width, height, opts.display, strings.Join(xListenerArgs(), " "))
}

func waitForXwayland(engine, name string) (string, error) {
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		logs, err := engineCombined(engine, "logs", name)
		if err == nil {
			if m := xwaylandDisplay.FindStringSubmatch(string(logs)); m != nil {
				if err := waitForDisplay(m[1]); err != nil {
					return "", err
				}
				return m[1], nil
			}
			if strings.Contains(string(logs), "fatal:") {
				return "", fmt.Errorf("the X server failed to start")
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", fmt.Errorf("the X server never reported a display")
}

// runningDisplay reports the display of an already-running container, if its
// socket is still there.
func runningDisplay(engine string, opts xOptions) (string, bool) {
	state, err := engineOutput(engine, "inspect", "-f", "{{.State.Running}}", opts.name)
	if err != nil || strings.TrimSpace(string(state)) != "true" {
		return "", false
	}

	display, ok := containerDisplay(engine, opts.name)
	if !ok {
		return "", false
	}
	if !displayListening(display) {
		return "", false
	}
	return display, true
}

func containerDisplay(engine, name string) (string, bool) {
	logs, err := engineCombined(engine, "logs", name)
	if err != nil {
		return "", false
	}

	m := xwaylandDisplay.FindStringSubmatch(string(logs))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// hasGPU reports whether a running container can reach the render node, which
// is the whole difference between hardware and llvmpipe.
//
// It asks the container rather than the engine on purpose. `inspect` looks like
// the right way and is not: podman leaves HostConfig.Devices empty for a
// container started with --device and records it only in Config.CreateCommand,
// which docker does not have at all. Looking for the file is the same question
// asked of something that has to answer it honestly.
func hasGPU(engine, name string) bool {
	return engineRun(engine, "exec", name,
		"sh", "-c", "test -r "+renderNode+" && test -w "+renderNode) == nil
}

func stopXContainer(opts xOptions) error {
	engine, err := containerEngine(opts.engine)
	if err != nil {
		return err
	}

	if err := removeXContainer(engine, opts.name); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "ebitenmcp: X server stopped")
	return nil
}

// Container sockets and lock files live in its own filesystem. Never unlink
// a host artifact merely because a container log mentions its display number.
func removeXContainer(engine, name string) error {
	out, err := engineCombined(engine, "rm", "-f", name)
	if err != nil {
		return fmt.Errorf("stopping the X server: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func xStatus(opts xOptions) error {
	engine, err := containerEngine(opts.engine)
	if err != nil {
		return err
	}

	display, ok := runningDisplay(engine, opts)
	if !ok {
		fmt.Println("no X server running")
		return nil
	}

	fmt.Printf("display  %s\nsocket   %s\nrenderer %s\nimage    %s\n",
		display, socketPath(display), rendererForMode(display, hasGPU(engine, opts.name)), imageTag())
	return nil
}

func socketPath(display string) string {
	return filepath.Join(x11SocketDir, "X"+strings.TrimPrefix(display, ":"))
}

func lockPath(display string) string {
	return filepath.Join(filepath.Dir(x11SocketDir), ".X"+strings.TrimPrefix(display, ":")+"-lock")
}

func displayNumber(display string) (int, error) {
	if !strings.HasPrefix(display, ":") || strings.Contains(display, ".") {
		return 0, fmt.Errorf("expected : followed by a display number")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(display, ":"))
	if err != nil || n < 0 || display != fmt.Sprintf(":%d", n) {
		return 0, fmt.Errorf("expected : followed by a display number")
	}
	return n, nil
}

func displayPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !os.IsNotExist(err)
}

func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
