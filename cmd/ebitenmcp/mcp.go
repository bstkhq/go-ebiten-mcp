package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// `ebitenmcp mcp` is the control server: it starts the display and the game, and
// forwards the game's own tools once there is a game to forward them to.
//
// The tools that look at a game and drive it belong to the game and are served
// from its own endpoint, which is where a client that can reach it should go —
// one hop instead of two, and no images copied through anything. So the normal
// setup is still two entries, and the normal way to debug is still to attach to
// a game that is already running on a machine that already has a screen:
//
//	{"mcpServers": {
//	  "game":         {"type": "http", "url": "http://127.0.0.1:8384/mcp"},
//	  "game-control": {"command": "go",
//	                   "args": ["run", "github.com/bstkhq/go-ebiten-mcp/cmd/ebitenmcp@latest",
//	                            "mcp", "--start", "go run ./cmd/mygame"]}
//	}}
//
// The forwarding (proxy.go) is for the clients that cannot do that. Plenty only
// know how to launch a command and talk over a pipe, and for those the game's
// endpoint cannot be configured at all — without it they could start a game and
// then do nothing with it. One entry, both jobs, at the cost of a hop.

type controlOptions struct {
	url    string
	start  string
	gpu    bool
	screen string
	addr   string
	mode   string
}

func mcpCommand(args []string, url string) error {
	opts := controlOptions{url: url, screen: "1280x720", addr: defaultAddr, mode: "auto"}

	for len(args) >= 1 && strings.HasPrefix(args[0], "-") {
		flag := args[0]
		args = args[1:]

		if flag == "--gpu" {
			opts.gpu = true
			continue
		}
		if len(args) == 0 {
			return fmt.Errorf("%s needs a value", flag)
		}
		value := args[0]
		args = args[1:]

		switch flag {
		case "--start":
			opts.start = value
		case "--screen":
			opts.screen = value
		case "--x":
			opts.mode = value
		case "--addr":
			opts.addr = value
			opts.url = "http://" + value + Path
		default:
			return fmt.Errorf("unknown flag %q", flag)
		}
	}

	c := &control{opts: opts}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:  "go-ebiten-mcp-control",
		Title: "Ebitengine game control",
		Description: "Start and stop an Ebitengine game and the display it needs. " +
			"The tools that look at the game and drive it are served by the game itself.",
		Version: "1",
	}, nil)

	c.addTools(server)

	// The game's own tools, forwarded, so that a client which only speaks stdio
	// is not left able to start a game and do nothing with it. See proxy.go.
	c.proxy = newProxy(c, server)
	c.proxy.restore()

	ctx := context.Background()

	// A game that is already up is the normal case, and asking it now is what
	// makes the list right from the first request rather than after the first
	// game_start.
	if alive(ctx, c.opts.url) {
		c.proxy.refresh(ctx)
	}

	return server.Run(ctx, &mcpsdk.StdioTransport{})
}

// Path is the route the game serves MCP on, spelled out here rather than
// imported: importing the library would make this command open a window.
const Path = "/mcp"

type control struct {
	opts  controlOptions
	proxy *proxy

	// starting serialises game_start end to end, including the wait.
	//
	// Without it two concurrent calls both find nothing answering and both
	// launch: c.game keeps whichever finished last and the other is orphaned —
	// still running, holding a window, and impossible to stop, because
	// game_stop only knows about c.game. It does not even collide loudly, since
	// the game falls back to a free port when its own is taken. A client that
	// retries is enough to cause it.
	starting sync.Mutex

	mu      sync.Mutex
	game    *exec.Cmd
	out     *tailWriter
	exited  bool
	exitErr error
}

func (c *control) addTools(server *mcpsdk.Server) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "game_start",
		Description: "Start the game, with a display if the machine has none. Once it is up, its " +
			"own tools — screenshots, input, state — are served from the URL this returns. A game " +
			"that is already running is left alone.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Start the game"},
	}, c.start)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "game_stop",
		Description: "Stop the game this server started.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Stop the game"},
	}, c.stop)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "game_status",
		Description: "Whether a game is answering, and where. Ask this first when the game's own " +
			"tools are not responding.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Game status", ReadOnlyHint: true},
	}, c.status)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "x_start",
		Description: "Run an X server in a container, for a machine with no display, and return " +
			"the DISPLAY to use. game_start does this by itself; this is for pointing something " +
			"else at the same display.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Start a display"},
	}, c.xStart)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "x_stop",
		Description: "Stop the containerised X server.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Stop the display"},
	}, c.xStop)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "x_status",
		Description: "Whether a containerised X server is running, on which display, and with which renderer.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Display status", ReadOnlyHint: true},
	}, c.xStatus)
}

type emptyInput struct{}

func (c *control) start(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	c.starting.Lock()
	defer c.starting.Unlock()

	// Checked after the wait, not before it: a second caller that queued behind
	// a start now finds the game up and says so, rather than launching another.
	if alive(ctx, c.opts.url) {
		c.refreshProxy(ctx)
		return nil, map[string]any{
			"url":     c.opts.url,
			"running": true,
			"note":    "a game was already answering there and was left alone",
		}, nil
	}

	if c.opts.start == "" {
		return nil, nil, fmt.Errorf("nothing is running at %s and no --start command was configured; "+
			"either start the game yourself with EBITEN_MCP_ADDR set, or configure this server with "+
			`--start "go run ./cmd/yourgame"`, c.opts.url)
	}

	exit, out, err := c.launch()
	if err != nil {
		return nil, nil, err
	}

	deadline := time.After(startTimeout)
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		// Watching the process is what turns three very different failures —
		// does not compile, panics at init, wrong flag — from ninety seconds of
		// the same unhelpful sentence into an answer, usually in about one.
		case err := <-exit:
			return nil, nil, fmt.Errorf("the game %s before it answered on %s.\nWhat it printed:\n%s",
				exitReason(err), c.opts.url, out.Text(startOutputLines))

		case <-poll.C:
			if alive(ctx, c.opts.url) {
				c.refreshProxy(ctx)
				return nil, map[string]any{
					"url":     c.opts.url,
					"running": true,
					"note": "the game's own tools are served from this URL, and are also " +
						"forwarded through this server for a client that cannot reach it",
				}, nil
			}

		case <-deadline:
			return nil, nil, fmt.Errorf("the game is still running but never answered on %s within %s.\nWhat it printed:\n%s",
				c.opts.url, startTimeout, out.Text(startOutputLines))

		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

const (
	startTimeout     = 90 * time.Second
	startOutputLines = 40
)

// launch runs the game through `ebitenmcp run`, so it gets its display and its
// server switched on by the same path a person would use.
//
// It returns the channel the exit status arrives on and the buffer holding what
// the process printed, because those two together are the whole diagnosis when
// it does not come up.
func (c *control) launch() (<-chan error, *tailWriter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}

	command, err := splitCommand(c.opts.start)
	if err != nil {
		return nil, nil, err
	}

	args := []string{"run", "--screen", c.opts.screen, "--addr", c.opts.addr, "--x", c.opts.mode}
	if c.opts.gpu {
		args = append(args, "--gpu")
	}
	args = append(args, "--")
	args = append(args, command...)

	cmd := exec.Command(self, args...)

	// Never stdout: that is this server's transport. Kept as well as passed on,
	// so that whatever the game says on its way down is something a tool can
	// answer with rather than something in a log file.
	out := newTail(200, os.Stderr)
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting the game with %q: %w", c.opts.start, err)
	}

	exit := make(chan error, 1)
	go func() {
		err := cmd.Wait()

		c.mu.Lock()
		c.exited, c.exitErr = true, err
		c.mu.Unlock()

		exit <- err
	}()

	c.game, c.out = cmd, out
	c.exited, c.exitErr = false, nil

	fmt.Fprintf(os.Stderr, "ebitenmcp: started the game: %s\n", c.opts.start)
	return exit, out, nil
}

// exitReason turns a wait error into something worth reading. A game that ran
// and stopped cleanly is a different problem from one that never compiled, and
// the exit status is what tells them apart.
func exitReason(err error) string {
	if err == nil {
		return "exited cleanly"
	}
	return "failed with " + err.Error()
}

func (c *control) stop(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.game == nil || c.game.Process == nil {
		return nil, nil, fmt.Errorf("this server did not start a game, " +
			"and one that was already running is not ours to stop")
	}

	c.game.Process.Kill()
	c.game, c.out = nil, nil
	c.exited, c.exitErr = false, nil

	// The forwarding session points at a process that is being killed. Letting
	// go of it now means the next call reconnects rather than failing once on a
	// dead pipe.
	if c.proxy != nil {
		c.proxy.disconnect()
	}

	return nil, map[string]any{"running": false}, nil
}

// refreshProxy re-reads the game's tool list, which is the one moment it can
// change. Never fatal: forwarding what was already known is better than
// refusing to say a game is up.
func (c *control) refreshProxy(ctx context.Context) {
	if c.proxy != nil {
		c.proxy.refresh(ctx)
	}
}

func (c *control) status(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	running := alive(ctx, c.opts.url)

	out := map[string]any{"url": c.opts.url, "running": running}
	if !running {
		out["note"] = "nothing is answering there; call game_start"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Ours only while it is still alive. Reporting a process that died a minute
	// ago as one we started is how "the game is running" and "the game is
	// answering" drift apart, and then nothing anybody reads is true.
	out["started_by_us"] = c.game != nil && !c.exited

	if c.game != nil && c.exited {
		out["note"] = "the game this server started has since " + exitReason(c.exitErr)
		if c.out != nil {
			out["output"] = c.out.Tail(20)
		}
	}

	return nil, out, nil
}

// alive asks the game's endpoint whether it is there without speaking MCP: a
// connection and a status code answer the question, and the status page the
// server already serves is exactly the right thing to ask for.
func alive(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(url, Path)+"/", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()

	return resp.StatusCode < 500
}

type xStartInput struct {
	GPU    bool   `json:"gpu,omitempty" jsonschema:"render on the GPU rather than in software"`
	Screen string `json:"screen,omitempty" jsonschema:"display size, for example 1280x720"`
}

func (c *control) xStart(_ context.Context, _ *mcpsdk.CallToolRequest, in xStartInput) (*mcpsdk.CallToolResult, any, error) {
	opts := newXOptions()
	opts.gpu = in.GPU || c.opts.gpu
	if in.Screen != "" {
		opts.screen = in.Screen
	}

	display, started, err := startXContainer(opts)
	if err != nil {
		return nil, nil, err
	}

	return nil, map[string]any{
		"display":  display,
		"renderer": renderer(display),
		"started":  started,
	}, nil
}

func (c *control) xStop(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	if err := stopXContainer(newXOptions()); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"running": false}, nil
}

func (c *control) xStatus(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	engine, err := containerEngine("")
	if err != nil {
		return nil, nil, err
	}

	display, ok := runningDisplay(engine, newXOptions())
	if !ok {
		return nil, map[string]any{"running": false}, nil
	}

	return nil, map[string]any{
		"running":  true,
		"display":  display,
		"socket":   socketPath(display),
		"renderer": renderer(display),
	}, nil
}
