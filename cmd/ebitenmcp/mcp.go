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

// `ebitenmcp mcp` is the control server: it starts displays and games, and
// nothing else.
//
// The tools that look at a game and drive it live in the game, served from its
// own endpoint, and this command does not proxy them. That split is deliberate.
// The normal way to debug is to attach to a game that is already running — on a
// kiosk, with a real screen — where the game's server is already there and its
// tools are already listed. Nothing needs starting, so nothing should be in the
// way.
//
// This server is for the other case: no display, no game, usually a test. Two
// entries in the client's configuration, each doing one job:
//
//	{"mcpServers": {
//	  "game":         {"type": "http", "url": "http://127.0.0.1:8384/mcp"},
//	  "game-control": {"command": "ebitenmcp",
//	                   "args": ["mcp", "--start", "go run ./cmd/mygame"]}
//	}}

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

	return server.Run(context.Background(), &mcpsdk.StdioTransport{})
}

// Path is the route the game serves MCP on, spelled out here rather than
// imported: importing the library would make this command open a window.
const Path = "/mcp"

type control struct {
	opts controlOptions

	mu   sync.Mutex
	game *exec.Cmd
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
	if alive(ctx, c.opts.url) {
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

	if err := c.launch(); err != nil {
		return nil, nil, err
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if alive(ctx, c.opts.url) {
			return nil, map[string]any{
				"url":     c.opts.url,
				"running": true,
				"note":    "the game's own tools are served from this URL",
			}, nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	return nil, nil, fmt.Errorf("the game started but never answered on %s", c.opts.url)
}

// launch runs the game through `ebitenmcp run`, so it gets its display and its
// server switched on by the same path a person would use.
func (c *control) launch() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	self, err := os.Executable()
	if err != nil {
		return err
	}

	args := []string{"run", "--screen", c.opts.screen, "--addr", c.opts.addr, "--x", c.opts.mode}
	if c.opts.gpu {
		args = append(args, "--gpu")
	}
	args = append(args, "--")
	args = append(args, strings.Fields(c.opts.start)...)

	cmd := exec.Command(self, args...)

	// Never stdout: that is this server's transport. The game's own output is
	// worth keeping, so it goes to stderr, and game_traces has it as well.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the game with %q: %w", c.opts.start, err)
	}

	c.game = cmd
	fmt.Fprintf(os.Stderr, "ebitenmcp: started the game: %s\n", c.opts.start)
	return nil
}

func (c *control) stop(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.game == nil || c.game.Process == nil {
		return nil, nil, fmt.Errorf("this server did not start a game, " +
			"and one that was already running is not ours to stop")
	}

	c.game.Process.Kill()
	c.game = nil

	return nil, map[string]any{"running": false}, nil
}

func (c *control) status(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	running := alive(ctx, c.opts.url)

	out := map[string]any{"url": c.opts.url, "running": running}
	if !running {
		out["note"] = "nothing is answering there; call game_start"
	}

	c.mu.Lock()
	out["started_by_us"] = c.game != nil
	c.mu.Unlock()

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
