// Command ebitenmcp is everything around a game that the game cannot do for
// itself: give it a display, start it, wire it into an agent, and talk to it
// from a shell.
//
//	ebitenmcp init             write .mcp.json and a note in CLAUDE.md
//	ebitenmcp mcp --start ...  the stdio server an MCP client launches
//	ebitenmcp x start [--gpu]  an X server in a container, for a machine with none
//	ebitenmcp run ./mygame     start a display if needed, run the game with MCP on
//	ebitenmcp find             the games running on this machine
//
// And, for a person at a terminal or a CI job that wants a screenshot without
// speaking the protocol, a small client for the game's own server:
//
//	ebitenmcp tools
//	ebitenmcp state
//	ebitenmcp call game_key '{"keys":["arrowdown"],"then_screenshot":true}'
//	ebitenmcp shot /tmp/frame.png
//
// Images that come back are written into the output directory and their paths
// printed, so a terminal never gets a screenful of base64. `--out` chooses the
// directory; it defaults to the working one.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultAddr = "127.0.0.1:8384"

	// modulePath is spelled out rather than taken from build info, which reports
	// an empty path for a binary built with `go build` from the module itself —
	// exactly how this is run while being worked on.
	modulePath = "github.com/bstkhq/go-ebiten-mcp"
	defaultURL = "http://" + defaultAddr + "/mcp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ebitenmcp:", err)
		os.Exit(1)
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, `usage: ebitenmcp [-url URL] [-out DIR] <command>

Wiring it into an agent:

  init [cmd...]            write .mcp.json and a note in CLAUDE.md, so an agent
                           finds the tools without being told they exist
  mcp [--start "cmd"]      the stdio server a client launches. Starts the game
                           and forwards the tools, so they are there from the
                           beginning of a session rather than only once somebody
                           has run the game by hand

The display, when there is none:

  x start [--gpu]          run an X server in a container and print its DISPLAY.
                           The game does not run in there; it connects over the
                           socket in /tmp/.X11-unix from wherever you built it
  x stop
  x status

Running a game:

  run [flags] <cmd>...     start a display if there is none, then run cmd with
                           the MCP server switched on. Works for a game binary,
                           for "go run ." and for "go test ./...".

      --gpu                render on the GPU instead of in software
      --x auto             where the display comes from: local, container, auto
      --screen 1280x720    size of the virtual display
      --display :99        use this display number instead of picking one
      --addr HOST:PORT     what the game should listen on (default `+defaultAddr+`)
      --keep-display       leave the display running after the command exits

Talking to one:

  tools                    list the tools the game offers
  call <name> [json]       call a tool with the given arguments
  state                    shorthand for: call game_state
  shot [file]              shorthand for: call game_screenshot, saved to file
  find                     list the games running on this machine

The server address defaults to `+defaultURL+`, or EBITEN_MCP_URL.
`)
	return fmt.Errorf("no command given")
}

func run(args []string) error {
	url := os.Getenv("EBITEN_MCP_URL")
	if url == "" {
		url = defaultURL
	}
	out := "."

	// The global flags, which come before the subcommand. A loop with a goto out
	// of its own switch is one way to write this; a loop that stops when it stops
	// recognising things is the same thing without the label.
	for len(args) >= 2 {
		switch args[0] {
		case "-url", "--url":
			url, args = args[1], args[2:]
			continue
		case "-out", "--out":
			out, args = args[1], args[2:]
			continue
		}
		break
	}

	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "find":
		return find()
	case "run":
		return runCommand(args[1:])
	case "x":
		return xCommand(args[1:])
	case "mcp":
		return mcpCommand(args[1:], url)
	case "init":
		return initCommand(args[1:])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	session, err := connect(ctx, url)
	if err != nil {
		return err
	}
	defer session.Close()

	switch args[0] {
	case "tools":
		return listTools(ctx, session)

	case "state":
		return call(ctx, session, out, "game_state", "")

	case "shot":
		file := ""
		if len(args) > 1 {
			file = args[1]
		}
		return shot(ctx, session, out, file)

	case "call":
		if len(args) < 2 {
			return fmt.Errorf("call needs a tool name")
		}
		arguments := ""
		if len(args) > 2 {
			arguments = args[2]
		}
		return call(ctx, session, out, args[1], arguments)

	default:
		return usage()
	}
}

func connect(ctx context.Context, url string) (*mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ebitenmcp-cli", Version: "1"}, nil)

	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w\nis the game running with EBITEN_MCP_ADDR set?", url, err)
	}
	return session, nil
}

func listTools(ctx context.Context, session *mcpsdk.ClientSession) error {
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}

	for _, tool := range result.Tools {
		fmt.Printf("%-22s %s\n", tool.Name, firstLine(tool.Description))
	}
	return nil
}

func shot(ctx context.Context, session *mcpsdk.ClientSession, out, file string) error {
	if file != "" {
		out = filepath.Dir(file)
	}
	return callNamed(ctx, session, out, file, "game_screenshot", "")
}

func call(ctx context.Context, session *mcpsdk.ClientSession, out, name, arguments string) error {
	return callNamed(ctx, session, out, "", name, arguments)
}

func callNamed(ctx context.Context, session *mcpsdk.ClientSession, out, file, name, arguments string) error {
	var parsed any
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
			return fmt.Errorf("arguments are not valid JSON: %w", err)
		}
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: parsed})
	if err != nil {
		return err
	}

	images := 0
	for _, content := range result.Content {
		switch c := content.(type) {
		case *mcpsdk.TextContent:
			// A tool that returns only structured output gets its content
			// filled in by the SDK with the same JSON. Printing both would
			// show every answer twice.
			if result.StructuredContent != nil && json.Valid([]byte(c.Text)) {
				continue
			}
			fmt.Println(c.Text)

		case *mcpsdk.ImageContent:
			path := file
			if path == "" {
				path = filepath.Join(out, fmt.Sprintf("%s-%d%s", name, images, extensionFor(c.MIMEType)))
			}
			if err := os.WriteFile(path, c.Data, 0o644); err != nil {
				return err
			}
			fmt.Println("image written to", path)
			images++
		}
	}

	if result.StructuredContent != nil {
		encoded, err := json.MarshalIndent(result.StructuredContent, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	}

	if result.IsError {
		return fmt.Errorf("the tool reported an error")
	}
	return nil
}

// find lists the games that announced themselves, for when there is more than
// one and their ports were assigned rather than chosen.
func find() error {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "ebitenmcp")

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("no games have announced themselves in %s", dir)
	}

	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		var d struct {
			Name string `json:"name"`
			PID  int    `json:"pid"`
			URL  string `json:"url"`
			CWD  string `json:"cwd"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}

		alive := " (gone)"
		if process, err := os.FindProcess(d.PID); err == nil && process.Signal(nil) == nil {
			alive = ""
		}
		fmt.Printf("%-16s pid %-8d %s%s\n    %s\n", d.Name, d.PID, d.URL, alive, d.CWD)
	}
	return nil
}

// extensionFor names a file after what is actually in it. Writing a gif as
// .png works in most viewers, which sniff the contents, and is wrong everywhere
// it is read by name.
func extensionFor(mime string) string {
	switch mime {
	case "image/gif":
		return ".gif"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i+1]
	}
	return s
}
