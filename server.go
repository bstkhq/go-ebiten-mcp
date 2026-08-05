package ebitenmcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/wire"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionTimeout is how long an MCP session survives with nothing arriving on
// it. See routes.
const sessionTimeout = 30 * time.Minute

// Path is /mcp, the route the MCP endpoint is mounted on.
const Path = wire.Path

// Server exposes one running game over MCP.
type Server struct {
	rt     *Runtime
	opts   *Options
	media  *media
	traces *traceRing

	addr string
	ln   net.Listener
	http *http.Server

	// The MCP server, built once. See mcp.
	once sync.Once
	srv  *mcpsdk.Server
}

// Serve starts the MCP server for a wrapped game.
//
// The listener is bound before returning, so the address in the result is the
// real one even when the requested port was taken.
func Serve(rt *Runtime, opts *Options) (*Server, error) {
	ln, err := listen(opts.Addr)
	if err != nil {
		return nil, err
	}

	addr := ln.Addr().String()

	s, err := newServer(rt, opts, MediaDir, "http://"+addr)
	if err != nil {
		ln.Close()
		return nil, err
	}
	s.addr, s.ln = addr, ln

	// Capturing the process's own output starts here rather than at
	// construction: a game with no server has no reason to have its
	// descriptors rewired.
	s.traces.capture(rt.Tick)

	s.http = &http.Server{Handler: s.routes()}
	go s.http.Serve(ln)

	s.announce()
	return s, nil
}

// newServer assembles everything a tool needs, and nothing a socket does.
//
// Splitting it out is what makes the tools testable at all: a handler is the
// interesting part of a tool and not one of them needs a listener, an HTTP
// server or the process's descriptors rewired. Serve adds those on top.
func newServer(rt *Runtime, opts *Options, mediaDir, baseURL string) (*Server, error) {
	m, err := newMedia(mediaDir, baseURL)
	if err != nil {
		return nil, err
	}

	return &Server{rt: rt, opts: opts, media: m, traces: newTraceRing()}, nil
}

// listen binds the requested address, falling back to a free port on the same
// host when it is taken.
//
// Several instances of the same game is a normal situation — a video wall, two
// checkouts side by side — and refusing to start the debug server in that case
// would be exactly backwards.
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}

	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}

	ln, fallbackErr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if fallbackErr != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// A session per client, and none kept for a client that walked away.
	//
	// The handler was built with nil options, which leaves idle sessions in
	// place for ever: an agent that disconnects without saying so — a crashed
	// editor, a dropped network — leaves its session behind, and a game that
	// runs for days collects one for every one of them. Half an hour is far
	// longer than any gap between calls in a debugging session and far shorter
	// than a working day.
	mux.Handle(Path, mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return s.mcp() },
		&mcpsdk.StreamableHTTPOptions{SessionTimeout: sessionTimeout}))

	// Artifacts are served from the same listener the tools already advertise,
	// so a client that can render a URL gets the video for free.
	mux.Handle("/media/", http.StripPrefix("/media/",
		http.FileServer(http.Dir(s.media.dir))))

	mux.HandleFunc("/", s.status)

	return mux
}

// status is for a person with curl rather than for a client.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "go-ebiten-mcp\n\ngame     %s\npid      %d\ntick     %d\nlast tick %s ago\nmcp      %s%s\nmedia    %s/media/\n",
		s.opts.Name, os.Getpid(), s.rt.Tick(),
		time.Since(s.rt.LastTick()).Round(time.Millisecond),
		"http://"+s.addr, Path, "http://"+s.addr)
}

// mcp is the MCP server, assembled and registered on the first ask and shared
// from then on.
//
// Shared because registering a tool derives a JSON schema from the Go type its
// handler returns, and there are twenty-three of them. This used to run per
// HTTP request — the handler below was handed a function that built a whole new
// server every time one arrived — which cost about 1.6 MB a call and did not
// come back: after fifteen calls the game's heap had grown by 24 MB, and a
// third of it was schema derivation. An agent session is hundreds of calls.
//
// One server is what the SDK expects here; its own documentation says it is
// fine for the lookup to return the same one every time. A server serves as
// many sessions as connect to it, and nothing about a session changes the tools.
func (s *Server) mcp() *mcpsdk.Server {
	s.once.Do(func() { s.srv = s.buildMCP() })
	return s.srv
}

// instructions reach every client in the initialize response, so they are in
// context for a whole session whether anybody wanted them there or not. That is
// what keeps this short: only the handful of things that are true of every
// session, that no single tool's description can say on its own, and that cost
// somebody a wasted call to find out.
//
// Everything else belongs to the tool that needs it — the descriptions already
// carry when to reach for each one and which to prefer — or to the skill that
// ebitenmcp init installs, which is loaded when it is relevant instead of
// always.
const instructions = `This game is running and can be looked at while it runs.

Four things worth knowing before the first call:

- Start with game_state. It says whether the loop is running, paused, or has
  panicked, and it keeps answering when the game itself has stopped — as do
  game_traces and game_goroutines. A tool that hangs is not the same as a game
  that crashed, and these tell you which you have.
- game_frames is retrospective, but it has to be switched on before the thing
  you want to see. Enable it, reproduce the problem, then ask; asking first
  gets you nothing.
- Captures come from the final screen by default, which is what the player
  sees. Ask for stage: "offscreen" for anything you are going to compare
  between runs — the final screen is the size of the window, so it changes with
  the monitor.
- Showing a change is a screenshot, not a description of one. The input tools
  take then_wait_ticks and then_screenshot, so acting and looking is one call.`

func (s *Server) buildMCP() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:        "go-ebiten-mcp",
		Title:       "Ebitengine game: " + s.opts.Name,
		Description: "See, drive and inspect a running Ebitengine game: frames and video, loop control, state, traces and synthetic input.",
		Version:     "1",
	}, &mcpsdk.ServerOptions{Instructions: instructions})

	s.addViewTools(srv)
	s.addLoopTools(srv)
	s.addStateTools(srv)
	s.addInputTools(srv)
	s.addGamepadTools(srv)
	s.addProfileTools(srv)
	s.addScriptTools(srv)
	s.addFrameTools(srv)

	return srv
}

// Addr is the address the server actually bound.
func (s *Server) Addr() string { return s.addr }

// URL is the MCP endpoint to put in an .mcp.json.
func (s *Server) URL() string { return "http://" + s.addr + Path }

// Close stops serving. The game keeps running.
//
// Nil-safe on the HTTP server because newServer builds one without a listener —
// that is the whole point of it — and closing such a server has to be a no-op
// rather than the one thing in this package that panics on tidying up.
func (s *Server) Close() error {
	s.discoveryFile(true)

	if s.http == nil {
		return nil
	}
	return s.http.Close()
}

// announce makes the server findable: a line on stdout for a person, and a file
// for a client that has to pick between several running games.
func (s *Server) announce() {
	fmt.Printf("ebitenmcp: serving %s on %s (media at http://%s/media/)\n", s.opts.Name, s.URL(), s.addr)

	if !loopback(s.addr) {
		fmt.Fprintf(os.Stderr, "ebitenmcp: WARNING %s is reachable from the network and there is no "+
			"authentication.\n"+
			"ebitenmcp: anyone who can open that port can read this game's memory, including "+
			"unexported fields,\n"+
			"ebitenmcp: watch its screen, and type into it. Bind 127.0.0.1 and forward the port over "+
			"ssh instead.\n", s.addr)
	}

	s.discoveryFile(false)
}

// loopback reports whether an address is only reachable from this machine.
//
// Worth saying out loud when it is not. There is no authentication here — none
// of these tools asks who is calling — so the address is the entire access
// control, and a game bound to 0.0.0.0 is a game anyone on the network can drive
// and read the memory of. That is a reasonable thing to do on a private network
// with your eyes open, and a very bad thing to do by accident.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	// An empty host is what "" and ":8384" mean: every interface.
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	// A name rather than an address. Resolving it here would put a DNS lookup in
	// the startup path, so only the two spellings that need no lookup count.
	return host == "localhost"
}

func (s *Server) discoveryFile(remove bool) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, wire.DiscoveryDir)

	path := filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	if remove {
		os.Remove(path)
		return
	}

	// 0700, and the file written with O_EXCL. Without XDG_RUNTIME_DIR this lands
	// in /tmp — which is the case in a container, in CI, and under a systemd
	// unit with no user session, exactly where this runs. There it would
	// otherwise be world-readable, handing anyone with a shell the port of a
	// server that has no authentication; and a directory another user got to
	// first could hold a symlink for this write to follow.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	cwd, _ := os.Getwd()
	data, err := json.MarshalIndent(wire.Discovery{
		Name: s.opts.Name,
		PID:  os.Getpid(),
		Addr: s.addr,
		URL:  s.URL(),
		CWD:  cwd,
		Args: strings.Join(os.Args, " "),
	}, "", "  ")
	if err != nil {
		return
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Already there, or something is in the way. Either way this file is a
		// convenience for `ebitenmcp find` and not worth insisting on.
		return
	}
	defer f.Close()

	f.Write(data)
}

// readOnly marks a tool that only looks.
func readOnly(title string) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{
		Title:        title,
		ReadOnlyHint: true,
	}
}

// mutating marks a tool that changes what the game does. Pressing a key or
// stepping the loop is not a read, and a client should be able to tell.
func mutating(title string) *mcpsdk.ToolAnnotations {
	no := false
	return &mcpsdk.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: &no,
	}
}
