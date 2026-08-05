// Package wire holds the handful of things the library and the command line
// both have to agree on.
//
// They cannot share them the obvious way. Importing the library pulls in
// Ebitengine, whose package initialisation opens a window — so `ebitenmcp find`
// on a machine with no display would die before main ran, and `ebitenmcp x
// start`, whose entire job is to give it one, could never run at all. So the
// command line does not import the library, and until this package existed the
// contract between them was kept by copying: the route was spelled four ways
// inside one binary, two of them as bare literals, and the environment variables
// were declared twice with a comment on each saying it mirrored the other.
//
// Nothing here imports anything. That is the whole requirement.
package wire

// Path is the route the MCP endpoint is mounted on.
const Path = "/mcp"

// DefaultAddr is where a game listens when nobody says otherwise. Loopback,
// because there is no authentication and the address is the access control.
const DefaultAddr = "127.0.0.1:8384"

// AddrEnv turns the server on and says where to listen. Empty means no socket is
// opened and no goroutine is started.
const AddrEnv = "EBITEN_MCP_ADDR"

// CaptureEnv overrides which drawing stage captures come from.
const CaptureEnv = "EBITEN_MCP_CAPTURE"

// RendererEnv is how `ebitenmcp run` tells the game which renderer it ended up
// with, so a golden image can be tagged with it.
const RendererEnv = "EBITENMCP_RENDERER"

// DiscoveryDir is where a running game announces itself, under
// XDG_RUNTIME_DIR or the temporary directory. One file per process.
const DiscoveryDir = "ebitenmcp"

// Discovery is what a running game writes there, and what `ebitenmcp find`
// reads back. Written by the library, read by the command line, which is why it
// lives in neither.
type Discovery struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
	URL  string `json:"url"`
	CWD  string `json:"cwd"`
	Args string `json:"args"`
}
