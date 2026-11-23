//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/wire"
)

// `ebitenmcp display` keeps one GPU display up for as many commands as want it.
//
//	ebitenmcp display [--screen 1280x720] DIR
//
// A compositor per command is half a second and a share of VRAM each time, and
// something that runs a test suite hundreds of times — mutation testing, a gate
// with several workers — pays it hundreds of times. This starts weston and
// Xwayland once, prints the display, and keeps it until it is told to stop
// (SIGTERM, SIGINT, SIGHUP), the process that started it goes, or the server
// dies. Then it takes the display down and removes what it made. Whoever starts
// it owns it: `ebitenmcp run` with EBITENMCP_DISPLAY set uses it and never
// stops it.
//
// The display is a socket file in DIR, unix:DIR/X<n>, as well as the abstract
// socket every display here has. An abstract socket belongs to one network
// namespace, so a command run in a private network of its own could not reach
// it; a file does not care which network its client is in.
//
// GPU only. A software display that outlives its command is exactly what the
// host policy exists to prevent, and a per-command one is still `ebitenmcp run`.

// DisplayEnvName is the display `run` reuses, as `ebitenmcp display` prints it.
const DisplayEnvName = wire.DisplayEnv

// sharedDisplayPrefix is how libxcb is given a socket by path rather than by
// number. libX11 passes the name through to it.
const sharedDisplayPrefix = "unix:"

// How often the display and its owner are looked at. A second is short enough
// that nothing is left drawing for long after its owner is gone, and long
// enough to cost nothing.
const displayWatchInterval = time.Second

func displayCommand(args []string) error {
	if !usesDisplay {
		return fmt.Errorf("there is no X display on this platform and Ebitengine does not want one")
	}

	screen := defaultScreen
	rest, err := parseFlags(args, map[string]flagSpec{
		"--screen": stringFlag(func(v string) { screen = v }),
	})
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("display needs one directory to put its socket in: ebitenmcp display [--screen WxH] DIR")
	}
	if _, err := hostGPU(true); err != nil {
		return err
	}

	owner := os.Getppid()
	display, stop, err := startSharedDisplay(rest[0], screen)
	if err != nil {
		return err
	}
	defer stop()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)

	fmt.Println(display)
	fmt.Fprintf(os.Stderr, "ebitenmcp: keeping %s up until told to stop; use it with %s=%s\n",
		display, DisplayEnvName, display)

	return watchDisplay(display, owner, signals, displayWatchInterval)
}

// watchDisplay returns when the display should go: nil when it was asked to,
// or its owner went, an error when the server stopped answering by itself.
func watchDisplay(display string, owner int, signals <-chan os.Signal, every time.Duration) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	misses := 0
	for {
		select {
		case <-signals:
			return nil
		case <-ticker.C:
		}
		// Its owner is gone when the parent is not the one it started with: an
		// orphan is handed to init or a subreaper. Killed outright, the owner
		// cannot stop it, and a display nobody owns is VRAM nobody frees.
		if os.Getppid() != owner {
			return nil
		}
		// Twice, because one missed dial on a loaded machine is not a dead server.
		if sharedDisplayListening(display) {
			misses = 0
			continue
		}
		if misses++; misses == 2 {
			return fmt.Errorf("the X server on %s stopped answering", display)
		}
	}
}

// startSharedDisplay starts the compositor with a socket file in dir and
// returns the display and how to stop it. It refuses a display that does not
// draw on the GPU.
func startSharedDisplay(dir, screen string) (string, func(), error) {
	width, height, err := splitScreen(screen)
	if err != nil {
		return "", nil, err
	}
	made, err := privateDirectory(dir)
	if err != nil {
		return "", nil, err
	}
	// A directory this made goes with the display; one it was given stays.
	unmake := func() {
		if made {
			os.Remove(dir)
		}
	}

	// The number is still reserved: Xwayland takes the abstract name and the
	// lock in /tmp for it, and another display must not pick the same one.
	number, reserved, err := reserveRunDisplay("")
	if err != nil {
		unmake()
		return "", nil, err
	}
	release := func() {
		reserved()
		unmake()
	}

	path := filepath.Join(dir, "X"+strings.TrimPrefix(number, ":"))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		release()
		return "", nil, fmt.Errorf("listening on %s: %w", path, err)
	}
	// The file is this process's to remove, once the server is gone.
	listener.SetUnlinkOnClose(false)
	socket, err := listener.File()
	listener.Close()
	if err != nil {
		os.Remove(path)
		release()
		return "", nil, fmt.Errorf("passing on %s: %w", path, err)
	}

	runtimeDir, err := os.MkdirTemp("", "ebitenmcp-runtime-")
	if err != nil {
		socket.Close()
		os.Remove(path)
		release()
		return "", nil, fmt.Errorf("creating private XDG_RUNTIME_DIR: %w", err)
	}

	x := newXOptions()
	x.gpu, x.display, x.listenFD = true, number, 3
	cmd := exec.Command("sh", "-c", westonCommand(x, width, height))
	cmd.Env = setEnvironment(gpuEnvironment(os.Environ()), "XDG_RUNTIME_DIR", runtimeDir)
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{socket}
	newProcessGroup(cmd)
	err = cmd.Start()
	socket.Close()
	if err != nil {
		os.RemoveAll(runtimeDir)
		os.Remove(path)
		release()
		return "", nil, fmt.Errorf("starting weston and Xwayland: %w", err)
	}

	var artifacts displayArtifacts
	stopProcess := displayProcessStopper(cmd, &artifacts, runtimeDir)
	stop := func() {
		stopProcess()
		os.Remove(path)
		release()
	}

	display := sharedDisplayPrefix + path
	if err := waitForSharedDisplay(display); err != nil {
		artifacts, _ = captureDisplayArtifacts(number)
		stop()
		return "", nil, err
	}
	if artifacts, err = captureDisplayArtifacts(number); err != nil {
		stop()
		return "", nil, err
	}

	found := rendererForGPU(display)
	if err := requireHardwareRenderer(found); err != nil {
		stop()
		return "", nil, err
	}
	fmt.Fprintf(os.Stderr, "ebitenmcp: started weston and Xwayland on %s, rendering with %s\n", display, found)
	return display, stop, nil
}

// privateDirectory makes dir, or accepts one that is already only this user's,
// and says whether it made it. The socket in it is a way into the display, and
// so into every window on it.
func privateDirectory(dir string) (bool, error) {
	if !filepath.IsAbs(dir) {
		return false, fmt.Errorf("the display's directory must be an absolute path, not %q: the display is named by it", dir)
	}
	made := true
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !os.IsExist(err) {
			return false, fmt.Errorf("creating %s: %w", dir, err)
		}
		made = false
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("%s is open to others (%v); the display's directory must be 0700", dir, info.Mode().Perm())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return false, fmt.Errorf("%s belongs to another user", dir)
	}
	return made, nil
}

// sharedDisplay checks the display EBITENMCP_DISPLAY names. A display that is
// named and not there is an error, never a reason to start another: whoever set
// it is counting on that one display, and on what it renders with.
func sharedDisplay(display string) (string, error) {
	path, ok := strings.CutPrefix(display, sharedDisplayPrefix)
	if !ok || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be unix:<absolute path to the socket>, as `ebitenmcp display` prints it, not %q",
			DisplayEnvName, display)
	}
	if !sharedDisplayListening(display) {
		return "", fmt.Errorf("%s names %s, and nothing is listening there", DisplayEnvName, display)
	}
	return display, nil
}

func sharedDisplayListening(display string) bool {
	conn, err := net.DialTimeout("unix", strings.TrimPrefix(display, sharedDisplayPrefix), 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitForSharedDisplay(display string) error {
	for i := 0; i < 150; i++ {
		if sharedDisplayListening(display) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("display %s never came up", display)
}
