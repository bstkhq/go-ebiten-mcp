package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The game's own tools, offered through this server as well.
//
// They live in the game and are served from its endpoint over HTTP, which is the
// right place for them and where a client that speaks HTTP should go. But not
// every MCP client does: plenty only know how to launch a command and talk to it
// over a pipe. For those the game's endpoint cannot be configured at all, and
// without this they could start a game and then do nothing with it.
//
// So this forwards. One MCP session to the game, opened when it is first needed,
// and every call passed down it and the answer passed back. The images come back
// as they are — content is content — at the cost of one extra copy, which is why
// the direct entry is still the better one where it works.
//
// The list is where the care is. Announcing the tools only while a game happens
// to be running would mean a list that appears and disappears, and while MCP has
// notifications/tools/list_changed for exactly that, not every client acts on
// one — and a client that cached the empty list would never show them again. So
// the list is remembered on disk and announced whether or not anything is
// running. With nothing there the tools answer "the game is not running, call
// game_start", which is a sentence somebody can act on, unlike a tool that is
// simply missing.
type proxy struct {
	control *control
	server  *mcpsdk.Server
	url     string

	mu         sync.Mutex
	session    *mcpsdk.ClientSession
	registered map[string]bool
}

func newProxy(c *control, server *mcpsdk.Server) *proxy {
	return &proxy{
		control:    c,
		server:     server,
		url:        c.opts.url,
		registered: map[string]bool{},
	}
}

// toolCachePath is beside the game's own artifacts, because it belongs to this
// project rather than to the machine: two games have two sets of tools.
func toolCachePath() string {
	return filepath.Join(".ebitenmcp", "tools.json")
}

// restore announces whatever was learned last time, so a client that lists tools
// before anything is running still sees them.
func (p *proxy) restore() {
	data, err := os.ReadFile(toolCachePath())
	if err != nil {
		return
	}

	var tools []*mcpsdk.Tool
	if err := json.Unmarshal(data, &tools); err != nil {
		return
	}

	p.register(tools)
}

// refresh asks the running game what it has and remembers the answer.
//
// Called after the game comes up, which is the only moment the list can change:
// a game built from a newer version of the library may have tools this server
// has never heard of, and hard-coding them here would make this command
// something that has to be kept in step by hand.
func (p *proxy) refresh(ctx context.Context) {
	session, err := p.connect(ctx)
	if err != nil {
		return
	}

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return
	}

	p.register(result.Tools)

	if data, err := json.MarshalIndent(result.Tools, "", "  "); err == nil {
		if err := os.MkdirAll(filepath.Dir(toolCachePath()), 0o755); err == nil {
			os.WriteFile(toolCachePath(), data, 0o644)
		}
	}
}

// register adds tools this server has not offered before. Adding is all it
// does: a tool that stopped existing stays listed and says so when called,
// because a client that already knows about it is better served by an
// explanation than by a name that has silently gone.
func (p *proxy) register(tools []*mcpsdk.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, tool := range tools {
		if tool == nil || tool.Name == "" || p.registered[tool.Name] {
			continue
		}
		// The control server's own tools win. Nothing collides today, and if
		// something ever does, starting the game must keep working.
		if isControlTool(tool.Name) {
			continue
		}
		// AddTool panics without an object schema, and a cache file somebody
		// edited is not worth crashing the server over.
		if !objectSchema(tool.InputSchema) {
			continue
		}

		name := tool.Name
		p.registered[name] = true

		// AddTool sends notifications/tools/list_changed by itself, so a client
		// that does honour one is told the moment this is learned.
		p.server.AddTool(tool, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return p.forward(ctx, name, req)
		})
	}
}

func (p *proxy) forward(ctx context.Context, name string, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var arguments any
	if req.Params != nil {
		arguments = req.Params.Arguments
	}

	session, err := p.connect(ctx)
	if err != nil {
		return nil, err
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err == nil {
		return result, nil
	}

	// The game may have been restarted under us, in which case the session is a
	// pipe to a process that is gone. Drop it and give the call one more go
	// before deciding it really failed.
	p.disconnect()

	session, connectErr := p.connect(ctx)
	if connectErr != nil {
		return nil, connectErr
	}
	return session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
}

func (p *proxy) connect(ctx context.Context) (*mcpsdk.ClientSession, error) {
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()

	if session != nil {
		return session, nil
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ebitenmcp-control", Version: "1"}, nil)

	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: p.url}, nil)
	if err != nil {
		// Deliberately not the connection error. "connection refused" describes
		// a socket; this describes what to do about it.
		return nil, fmt.Errorf("the game is not running, so this tool has nothing to ask: "+
			"call game_start first. It belongs to the game and is forwarded to it at %s", p.url)
	}

	p.mu.Lock()
	p.session = session
	p.mu.Unlock()

	return session, nil
}

func (p *proxy) disconnect() {
	p.mu.Lock()
	session := p.session
	p.session = nil
	p.mu.Unlock()

	if session != nil {
		session.Close()
	}
}

func objectSchema(schema any) bool {
	if schema == nil {
		return false
	}

	var m map[string]any
	data, err := json.Marshal(schema)
	if err != nil || json.Unmarshal(data, &m) != nil {
		return false
	}
	return m["type"] == "object"
}

func isControlTool(name string) bool {
	switch name {
	case "game_start", "game_stop", "game_status", "x_start", "x_stop", "x_status":
		return true
	}
	return false
}
